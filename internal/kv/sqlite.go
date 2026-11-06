package kv

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go, no cgo: keeps the static cross-builds
)

// SQLiteOptions is how a SQLite database is opened. The defaults are what a deployment should
// keep: WAL (readers never wait for the writer), synchronous FULL (every commit is on the disk
// before it answers), BEGIN IMMEDIATE for writes (the lock is taken before the first statement,
// never upgraded mid-transaction). A WAL database is one main file plus its -wal and -shm side
// files, and cannot live on a network file system.
type SQLiteOptions struct {
	// Path is the database file. Default beside the caller's choice: there is none here, the
	// caller always names it (the service uses <data_dir>/beacon.db).
	Path string
	// JournalMode is SQLite's journal_mode: WAL (the default), DELETE, TRUNCATE, PERSIST or
	// MEMORY (a test may want MEMORY, which is not durable and needs no side files).
	JournalMode string
	// Synchronous is SQLite's synchronous: FULL (the default), NORMAL or OFF.
	Synchronous string
	// BusyTimeout is how long a statement waits for a lock another connection holds (a writer
	// outside Beacon, or a checkpoint). Default 15s.
	BusyTimeout time.Duration
	// ReadOnly opens for reading only (the CLI's checks): no lock file, Update fails, and the
	// database must exist. Only meaningful when the service is either running (the -shm file is
	// there) or was stopped cleanly (the -wal file is empty).
	//
	// A writable open always takes, beside the database, an exclusive lock on the lock file
	// (path + ".lock") for this process alone: another process — or a second handle in this one —
	// fails with ErrLocked, as the bbolt file's own lock did, and the lock is the operating
	// system's to take back when the process dies. Readers are not held back: in WAL they read
	// without the write lock, which is why the CLI's checks work while the service runs.
	ReadOnly bool
}

// SQLite is a kv.DB on one SQLite file: a write pool of one connection (BEGIN IMMEDIATE, so the
// write lock is taken before the first statement, never upgraded mid-transaction) and a read pool
// for views.
type SQLite struct {
	write *sql.DB
	read  *sql.DB
	lock  *os.File
	path  string
}

// OpenSQLite opens or creates the database at o.Path.
func OpenSQLite(o SQLiteOptions) (*SQLite, error) {
	switch strings.ToUpper(o.JournalMode) {
	case "":
		o.JournalMode = "WAL"
	case "WAL", "DELETE", "TRUNCATE", "PERSIST", "MEMORY":
		o.JournalMode = strings.ToUpper(o.JournalMode)
	default:
		return nil, fmt.Errorf("sqlite journal_mode %q: WAL, DELETE, TRUNCATE, PERSIST or MEMORY", o.JournalMode)
	}
	switch strings.ToUpper(o.Synchronous) {
	case "":
		o.Synchronous = "FULL"
	case "FULL", "NORMAL", "OFF":
		o.Synchronous = strings.ToUpper(o.Synchronous)
	default:
		return nil, fmt.Errorf("sqlite synchronous %q: FULL, NORMAL or OFF", o.Synchronous)
	}
	if o.BusyTimeout == 0 {
		o.BusyTimeout = 15 * time.Second
	}
	if o.Path == "" {
		return nil, errors.New("sqlite: a path is needed")
	}
	if !o.ReadOnly {
		if err := os.MkdirAll(filepath.Dir(o.Path), 0o700); err != nil {
			return nil, err
		}
	}
	dsn := func(kv ...[2]string) string {
		q := url.Values{}
		for _, p := range kv {
			q.Add(p[0], p[1])
		}
		return "file:" + filepath.ToSlash(o.Path) + "?" + q.Encode()
	}
	s := &SQLite{path: o.Path}
	if o.ReadOnly {
		db, err := sql.Open("sqlite", dsn([2]string{"mode", "ro"},
			[2]string{"_pragma", "busy_timeout(" + busyMS(o.BusyTimeout) + ")"}))
		if err != nil {
			return nil, err
		}
		if err := db.PingContext(context.Background()); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("open %s read-only: %w", o.Path, err)
		}
		db.SetMaxOpenConns(4)
		s.read = db
		return s, nil
	}
	var err error
	if s.lock, err = lockSidecar(o.Path); err != nil {
		return nil, err
	}
	if err := ownerOnly(o.Path); err != nil {
		_ = s.Close()
		return nil, err
	}
	// the writer: one connection, BEGIN IMMEDIATE, WAL and synchronous FULL
	if s.write, err = sql.Open("sqlite", dsn(
		[2]string{"_txlock", "immediate"},
		[2]string{"_pragma", "busy_timeout(" + busyMS(o.BusyTimeout) + ")"},
		[2]string{"_pragma", "journal_mode(" + o.JournalMode + ")"},
		[2]string{"_pragma", "synchronous(" + o.Synchronous + ")"},
	)); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.write.SetMaxOpenConns(1)
	if s.read, err = sql.Open("sqlite", dsn(
		[2]string{"_pragma", "busy_timeout(" + busyMS(o.BusyTimeout) + ")"},
	)); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.read.SetMaxOpenConns(4)
	if err := s.migrate(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// lockSidecar takes the exclusive lock on the lock file beside the database.
func lockSidecar(path string) (*os.File, error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // the operator owns the directory
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s", ErrLocked, path+".lock")
	}
	return f, nil
}

// ownerOnly makes the database readable by its owner alone, as the bbolt file was: it holds
// token and password hashes and the audit log's signing key. SQLite creates a database with the
// process's umask (0644 under the usual 022) and gives its -wal and -shm files the database's
// mode, so the database is created 0600 before SQLite opens it, and a database (or side file)
// made by an earlier version with a wider mode is narrowed.
func ownerOnly(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // the operator names the path
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if fi.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(p, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func busyMS(d time.Duration) string { return fmt.Sprintf("%d", d.Milliseconds()) }

// migrate makes the schema, in the first write transaction.
func (s *SQLite) migrate() error {
	ctx := context.Background()
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return lockError(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS kv (
			bucket TEXT NOT NULL,
			k BLOB NOT NULL,
			v BLOB NOT NULL,
			PRIMARY KEY (bucket, k)
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS sequences (
			bucket TEXT NOT NULL PRIMARY KEY,
			seq INTEGER NOT NULL
		) WITHOUT ROWID`,
	} {
		if _, err = tx.ExecContext(ctx, ddl); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// lockError turns SQLite's "database is locked" (a busy write lock) into ErrLocked.
func lockError(err error) error {
	if err == nil || !strings.Contains(err.Error(), "locked") && !strings.Contains(err.Error(), "busy") {
		return err
	}
	return fmt.Errorf("%w: %s", ErrLocked, strings.TrimSpace(err.Error()))
}

// View runs fn in a read-only transaction.
func (s *SQLite) View(fn func(Tx) error) error {
	tx, err := s.read.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	st := &sqliteTx{tx: tx}
	err = fn(st)
	if st.err != nil {
		_ = tx.Rollback()
		return st.err
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Rollback() // nothing to commit; releases the snapshot
}

// Update runs fn in a read-write transaction that began by taking the write lock.
func (s *SQLite) Update(fn func(Tx) error) error {
	if s.write == nil {
		return errors.New("the database is open read-only")
	}
	tx, err := s.write.BeginTx(context.Background(), nil)
	if err != nil {
		return lockError(err)
	}
	st := &sqliteTx{tx: tx, write: true}
	err = fn(st)
	if st.err != nil {
		_ = tx.Rollback()
		return st.err
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// UpdateBoth runs fn in one write transaction, giving it the key-value space and the
// database/sql transaction under it, for what spans the two (the migration copies buckets and
// writes the event tables' first records together).
func (s *SQLite) UpdateBoth(fn func(tx Tx, raw *sql.Tx) error) error {
	if s.write == nil {
		return errors.New("the database is open read-only")
	}
	tx, err := s.write.BeginTx(context.Background(), nil)
	if err != nil {
		return lockError(err)
	}
	st := &sqliteTx{tx: tx, write: true}
	err = fn(st, tx)
	if st.err != nil {
		_ = tx.Rollback()
		return st.err
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Close folds the WAL into the database file, closes both pools and lets the lock file go.
func (s *SQLite) Close() error {
	var err error
	if s.write != nil {
		if s.JournalMode() == "wal" {
			_, _ = s.write.ExecContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)")
		}
		err = s.write.Close()
	}
	if s.read != nil {
		rerr := s.read.Close()
		if err == nil {
			err = rerr
		}
	}
	if s.lock != nil {
		lerr := s.lock.Close() // releases the operating system's lock with it
		if err == nil {
			err = lerr
		}
	}
	return err
}

// JournalMode reports the database's journal mode (lower case, as SQLite names it).
func (s *SQLite) JournalMode() string {
	var mode string
	db := s.read
	if db == nil {
		db = s.write
	}
	if db == nil {
		return ""
	}
	_ = db.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode)
	return strings.ToLower(mode)
}

// IntegrityCheck runs SQLite's own check of the whole database file and returns what it said
// ("ok", or the findings, one per line).
func (s *SQLite) IntegrityCheck() (string, error) {
	db := s.read
	if db == nil {
		db = s.write
	}
	rows, err := db.QueryContext(context.Background(), "PRAGMA integrity_check")
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var lines []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return "", err
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n"), rows.Err()
}

// Size is the database's size in bytes, page count by page size.
func (s *SQLite) Size() int64 {
	db := s.read
	if db == nil {
		db = s.write
	}
	var count, size int64
	if err := db.QueryRowContext(context.Background(), "PRAGMA page_count").Scan(&count); err != nil {
		return 0
	}
	if err := db.QueryRowContext(context.Background(), "PRAGMA page_size").Scan(&size); err != nil {
		return 0
	}
	return count * size
}

// Health is SQLite's own check of the whole database file: "ok", or the findings.
func (s *SQLite) Health() (string, error) { return s.IntegrityCheck() }

// Where names the database for the command line: the driver, the file, and how full it is.
func (s *SQLite) Where() string {
	return fmt.Sprintf("sqlite %s (%d bytes, journal %s)", s.path, s.Size(), s.JournalMode())
}

// Handle is the database behind the pools, for what lives beside the key-value space (the event
// tables): a query on it sees the committed state, not what an open Update holds.
func (s *SQLite) Handle() *sql.DB { return s.read }

// WriteHandle is the write pool, whose single connection holds the write lock; a transaction on
// it can span the key-value space and the event tables at once.
func (s *SQLite) WriteHandle() *sql.DB { return s.write }

type sqliteTx struct {
	tx    *sql.Tx
	write bool
	err   error // first read or write that failed; later calls no-op and Update/View return it
}

func (t *sqliteTx) fail(err error) error {
	if t.err == nil {
		t.err = err
	}
	return t.err
}

func (t *sqliteTx) Get(bucket, key []byte) []byte {
	if t.err != nil {
		return nil
	}
	ctx := context.Background()
	var v []byte
	err := t.tx.QueryRowContext(ctx, "SELECT v FROM kv WHERE bucket = ? AND k = ?", string(bucket), key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		_ = t.fail(err)
		return nil
	}
	if v == nil {
		return []byte{} // an empty blob is a value, not a missing key
	}
	return v
}

func (t *sqliteTx) Put(bucket, key, value []byte) error {
	if t.err != nil {
		return t.err
	}
	if value == nil {
		value = []byte{}
	}
	ctx := context.Background()
	_, err := t.tx.ExecContext(ctx, "INSERT INTO kv (bucket, k, v) VALUES (?, ?, ?) ON CONFLICT (bucket, k) DO UPDATE SET v = excluded.v",
		string(bucket), key, value)
	if err != nil {
		return t.fail(err)
	}
	return nil
}

func (t *sqliteTx) Delete(bucket, key []byte) error {
	if t.err != nil {
		return t.err
	}
	ctx := context.Background()
	_, err := t.tx.ExecContext(ctx, "DELETE FROM kv WHERE bucket = ? AND k = ?", string(bucket), key)
	if err != nil {
		return t.fail(err)
	}
	return nil
}

// prefixEnd is the key just past every key with this prefix; nil when the prefix covers to the end.
func prefixEnd(prefix []byte) []byte {
	end := bytes.Clone(prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] != 0xFF {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}

func (t *sqliteTx) Range(bucket []byte, r Range, fn func(k, v []byte) bool) {
	if t.err != nil {
		return
	}
	ctx := context.Background()
	q := "SELECT k, v FROM kv WHERE bucket = ?"
	args := []any{string(bucket)}
	if len(r.Prefix) > 0 {
		q += " AND k >= ?"
		args = append(args, r.Prefix)
		if end := prefixEnd(r.Prefix); end != nil {
			q += " AND k < ?"
			args = append(args, end)
		}
	}
	if len(r.Start) > 0 {
		if r.Reverse {
			q += " AND k < ?"
		} else {
			q += " AND k >= ?"
		}
		args = append(args, r.Start)
	}
	if r.Reverse {
		q += " ORDER BY k DESC"
	} else {
		q += " ORDER BY k ASC"
	}
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		_ = t.fail(err)
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, v []byte
		if err := rows.Scan(&k, &v); err != nil {
			_ = t.fail(err)
			return
		}
		if v == nil {
			v = []byte{}
		}
		if !fn(k, v) {
			return
		}
	}
	if err := rows.Err(); err != nil {
		_ = t.fail(err)
	}
}

func (t *sqliteTx) Count(bucket []byte) int {
	if t.err != nil {
		return 0
	}
	ctx := context.Background()
	var n int
	if err := t.tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM kv WHERE bucket = ?", string(bucket)).Scan(&n); err != nil {
		_ = t.fail(err)
		return 0
	}
	return n
}

func (t *sqliteTx) NextSequence(bucket []byte) (uint64, error) {
	if t.err != nil {
		return 0, t.err
	}
	ctx := context.Background()
	if !t.write {
		return 0, errors.New("a sequence advances only in an Update")
	}
	var seq uint64
	err := t.tx.QueryRowContext(ctx, `INSERT INTO sequences (bucket, seq) VALUES (?, 1)
		ON CONFLICT (bucket) DO UPDATE SET seq = seq + 1 RETURNING seq`, string(bucket)).Scan(&seq)
	if err != nil {
		return 0, t.fail(err)
	}
	return seq, nil
}

func (t *sqliteTx) SetSequence(bucket []byte, seq uint64) error {
	if t.err != nil {
		return t.err
	}
	ctx := context.Background()
	if !t.write {
		return errors.New("a sequence is set only in an Update")
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO sequences (bucket, seq) VALUES (?, ?)
		ON CONFLICT (bucket) DO UPDATE SET seq = excluded.seq`, string(bucket), seq)
	if err != nil {
		return t.fail(err)
	}
	return nil
}

func (t *sqliteTx) Sequence(bucket []byte) uint64 {
	if t.err != nil {
		return 0
	}
	ctx := context.Background()
	var seq uint64
	err := t.tx.QueryRowContext(ctx, "SELECT seq FROM sequences WHERE bucket = ?", string(bucket)).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		_ = t.fail(err)
		return 0
	}
	return seq
}

func (t *sqliteTx) CompareAndSwap(bucket, key, old, value []byte) error {
	if t.err != nil {
		return t.err
	}
	ctx := context.Background()
	if !t.write {
		return errors.New("a compare-and-swap runs only in an Update")
	}
	switch {
	case old == nil && value == nil:
		_, err := t.tx.ExecContext(ctx, "DELETE FROM kv WHERE bucket = ? AND k = ?", string(bucket), key)
		if err != nil {
			return t.fail(err)
		}
		return nil // absent already: nothing to hold
	case old == nil:
		if value == nil {
			value = []byte{}
		}
		_, err := t.tx.ExecContext(ctx, "INSERT INTO kv (bucket, k, v) VALUES (?, ?, ?)", string(bucket), key, value)
		if err != nil { // the key is there: not the absent value the caller expected
			return fmt.Errorf("%w: %s", ErrCompareFailed, "the key exists")
		}
		return nil
	case value == nil:
		res, err := t.tx.ExecContext(ctx, "DELETE FROM kv WHERE bucket = ? AND k = ? AND v = ?", string(bucket), key, old)
		if err != nil {
			return t.fail(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrCompareFailed
		}
		return nil
	default:
		res, err := t.tx.ExecContext(ctx, "UPDATE kv SET v = ? WHERE bucket = ? AND k = ? AND v = ?", value, string(bucket), key, old)
		if err != nil {
			return t.fail(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrCompareFailed
		}
		return nil
	}
}
