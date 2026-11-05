package kv

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// PostgresOptions is how a PostgreSQL 14+ database is opened: a DSN (libpq's key=value form or a
// URL), the read pool's size and a statement timeout. The write side always takes one connection of
// its own, as SQLite's single writer does, so a deployment's writes serialise in order whatever the
// pool size; the timeout bounds a statement that hangs (a lock elsewhere, a lost connection) before
// it holds the writer.
type PostgresOptions struct {
	// DSN is the connection string, read from the file the configuration names
	// (storage.dsn_file). Everything pgx understands goes: host, port, user, password, dbname,
	// sslmode, and more.
	DSN string
	// PoolSize is how many connections the read pool holds. Default 4.
	PoolSize int
	// StatementTimeout bounds each statement; 15s by default. Zero turns it off.
	StatementTimeout time.Duration
	// ReadOnly opens for reading only (the CLI's checks): no write pool, Update fails, and the
	// schema is not made. There is no lock to take — PostgreSQL is a server — so this is only
	// about not writing.
	ReadOnly bool
}

// Postgres is a kv.SQL on one PostgreSQL database: the same tables as SQLite's (kv, sequences),
// bytea keys and values, byte order everywhere SQLite's blobs had it.
type Postgres struct {
	write   *sql.DB
	read    *sql.DB
	where   string
	timeout time.Duration
	guard   func(context.Context, *sql.Tx) error
}

// OpenPostgres opens or creates the database o names. The DSN never appears in errors or logs: a
// connection that fails names the host and database alone.
func OpenPostgres(o PostgresOptions) (*Postgres, error) {
	if o.DSN == "" {
		return nil, errors.New("postgres: a DSN is needed")
	}
	if o.PoolSize <= 0 {
		o.PoolSize = 4
	}
	cfg, err := pgx.ParseConfig(o.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: the DSN is not valid")
	}
	if o.StatementTimeout > 0 {
		cfg.RuntimeParams["statement_timeout"] = strconv.FormatInt(o.StatementTimeout.Milliseconds(), 10)
		applySessionGuards(cfg, o.StatementTimeout)
	}
	cfg.RuntimeParams["application_name"] = "beacon"
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}
	open := func(max int) (*sql.DB, error) {
		db := stdlib.OpenDB(*cfg)
		db.SetMaxOpenConns(max)
		db.SetMaxIdleConns(max)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("postgres %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Database, redact(err))
		}
		return db, nil
	}
	p := &Postgres{where: fmt.Sprintf("postgres %s:%d/%s", cfg.Host, cfg.Port, cfg.Database), timeout: o.StatementTimeout}
	if o.ReadOnly {
		if p.read, err = open(o.PoolSize); err != nil {
			return nil, err
		}
		return p, nil
	}
	// the writer: one connection, as SQLite's write pool
	if p.write, err = open(1); err != nil {
		return nil, err
	}
	if p.read, err = open(o.PoolSize); err != nil {
		_ = p.write.Close()
		return nil, err
	}
	if err := p.migrate(); err != nil {
		_ = p.Close()
		return nil, err
	}
	return p, nil
}

// applySessionGuards bounds an idle-in-transaction session, a lock wait, and a dead TCP
// connection by timeout, so a paused or partitioned writer cannot hold a row lock until the
// server's keepalive gives up (about two hours by default).
func applySessionGuards(cfg *pgx.ConnConfig, timeout time.Duration) {
	if timeout <= 0 || cfg == nil {
		return
	}
	ms := timeout.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	cfg.RuntimeParams["idle_in_transaction_session_timeout"] = strconv.FormatInt(ms, 10)
	cfg.RuntimeParams["lock_timeout"] = strconv.FormatInt(ms, 10)
	sec := int((timeout + time.Second - 1) / time.Second)
	if sec < 1 {
		sec = 1
	}
	cfg.RuntimeParams["tcp_keepalives_idle"] = strconv.Itoa(sec)
	cfg.RuntimeParams["tcp_keepalives_interval"] = "1"
	cfg.RuntimeParams["tcp_keepalives_count"] = "3"
}

// timeoutCtx bounds a client-side wait a little past the server's statement_timeout, so a
// blackholed connection cannot hold the writer until TCP gives up.
func (p *Postgres) timeoutCtx() (context.Context, context.CancelFunc) {
	d := p.timeout
	switch {
	case d > 0:
		d += time.Second
	default:
		d = 30 * time.Second
	}
	return context.WithTimeout(context.Background(), d)
}

// SetWriteGuard runs fn at the start of every write transaction (the HA lease check). A nil fn
// clears it. Set it before the first Update; it is not itself synchronised with in-flight writes.
func (p *Postgres) SetWriteGuard(fn func(context.Context, *sql.Tx) error) {
	p.guard = fn
}

// redact keeps the DSN's secret out of errors: pgx quotes the password when it says why a
// connection failed.
func redact(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return err // a server's message carries no credential
	}
	msg := err.Error()
	if i := strings.Index(msg, "password="); i >= 0 {
		msg = msg[:i] + "password=…"
	}
	return errors.New(msg)
}

// SchemaLock is the advisory lock every CREATE TABLE IF NOT EXISTS on the database takes: the two
// instances of a primary/standby pair, starting together, would otherwise race the check and one
// would lose to PostgreSQL's duplicate-type error. The lock lives in the transaction that holds it.
const SchemaLock = 784221

// migrate makes the schema, in one transaction. WITHOUT ROWID is SQLite's; PostgreSQL's tables are
// heaps with primary-key indexes, which is what they are anyway.
func (p *Postgres) migrate() error {
	ctx, cancel := p.timeoutCtx()
	defer cancel()
	tx, err := p.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, SchemaLock); err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS kv (
			bucket TEXT NOT NULL,
			k BYTEA NOT NULL,
			v BYTEA NOT NULL,
			PRIMARY KEY (bucket, k)
		)`,
		`CREATE TABLE IF NOT EXISTS sequences (
			bucket TEXT NOT NULL PRIMARY KEY,
			seq BIGINT NOT NULL
		)`,
	} {
		if _, err = tx.ExecContext(ctx, ddl); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// View runs fn in a read-only Repeatable Read transaction, so several statements see one snapshot
// (Config reads the entries and the version together).
func (p *Postgres) View(fn func(Tx) error) error {
	ctx, cancel := p.timeoutCtx()
	defer cancel()
	tx, err := p.read.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	st := &pgTx{tx: tx, ctx: ctx}
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

func (p *Postgres) beginWrite() (context.Context, *sql.Tx, context.CancelFunc, error) {
	if p.write == nil {
		return nil, nil, func() {}, errors.New("the database is open read-only")
	}
	ctx, cancel := p.timeoutCtx()
	tx, err := p.write.BeginTx(ctx, nil)
	if err != nil {
		cancel()
		return nil, nil, func() {}, err
	}
	if p.guard != nil {
		if err := p.guard(ctx, tx); err != nil {
			_ = tx.Rollback()
			cancel()
			return nil, nil, func() {}, err
		}
	}
	return ctx, tx, cancel, nil
}

func finishWrite(tx *sql.Tx, st *pgTx, err error) error {
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

// Update runs fn in a read-write transaction on the single write connection.
func (p *Postgres) Update(fn func(Tx) error) error {
	ctx, tx, cancel, err := p.beginWrite()
	if err != nil {
		return err
	}
	defer cancel()
	st := &pgTx{tx: tx, write: true, ctx: ctx}
	return finishWrite(tx, st, fn(st))
}

// UpdateBoth runs fn in one write transaction, giving it the key-value space and the database/sql
// transaction under it, as SQLite's does for the migrations.
func (p *Postgres) UpdateBoth(fn func(tx Tx, raw *sql.Tx) error) error {
	ctx, tx, cancel, err := p.beginWrite()
	if err != nil {
		return err
	}
	defer cancel()
	st := &pgTx{tx: tx, write: true, ctx: ctx}
	return finishWrite(tx, st, fn(st, tx))
}

// Close closes both pools.
func (p *Postgres) Close() error {
	var err error
	if p.write != nil {
		err = p.write.Close()
	}
	if p.read != nil {
		rerr := p.read.Close()
		if err == nil {
			err = rerr
		}
	}
	return err
}

// Health reads the tables this backend keeps (kv, sequences); PostgreSQL has no single-file
// check, but every table answering a count, with none missing, is the health of its own. The
// event tables beside them are the store's: the command line's checks read them through the
// store, whose chain verification says at once when one is missing.
func (p *Postgres) Health() (string, error) {
	ctx, cancel := p.timeoutCtx()
	defer cancel()
	var n int
	err := p.read.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM kv) + (SELECT COUNT(*) FROM sequences)`).Scan(&n)
	if err != nil {
		return "", err
	}
	return "ok", nil
}

// Where names the database for the command line: host, port and database, never the DSN.
func (p *Postgres) Where() string { return p.where }

// Handle is the read pool.
func (p *Postgres) Handle() *sql.DB { return p.read }

// WriteHandle is the write pool, whose single connection serialises the writes.
func (p *Postgres) WriteHandle() *sql.DB { return p.write }

type pgTx struct {
	tx    *sql.Tx
	write bool
	ctx   context.Context
	err   error // first read or write that failed; later calls no-op and Update/View return it
}

func (t *pgTx) fail(err error) error {
	if t.err == nil {
		t.err = err
	}
	return t.err
}

func (t *pgTx) context() context.Context {
	if t.ctx != nil {
		return t.ctx
	}
	return context.Background()
}

// query builds the statement with PostgreSQL's $N placeholders from one written with ?, and keeps
// the arguments beside it.
func (t *pgTx) query(sql string, args ...any) (string, []any) {
	if len(args) == 0 {
		return sql, args
	}
	var b strings.Builder
	n := 0
	for {
		i := strings.IndexByte(sql, '?')
		if i < 0 {
			b.WriteString(sql)
			return b.String(), args
		}
		n++
		b.WriteString(sql[:i])
		fmt.Fprintf(&b, "$%d", n)
		sql = sql[i+1:]
	}
}

func (t *pgTx) Get(bucket, key []byte) []byte {
	if t.err != nil {
		return nil
	}
	q, args := t.query("SELECT v FROM kv WHERE bucket = ? AND k = ?", string(bucket), key)
	var v []byte
	err := t.tx.QueryRowContext(t.context(), q, args...).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		_ = t.fail(err)
		return nil
	}
	if v == nil {
		return []byte{}
	}
	return v
}

func (t *pgTx) Put(bucket, key, value []byte) error {
	if t.err != nil {
		return t.err
	}
	if value == nil {
		value = []byte{}
	}
	q, args := t.query("INSERT INTO kv (bucket, k, v) VALUES (?, ?, ?) ON CONFLICT (bucket, k) DO UPDATE SET v = excluded.v",
		string(bucket), key, value)
	_, err := t.tx.ExecContext(t.context(), q, args...)
	if err != nil {
		return t.fail(err)
	}
	return nil
}

func (t *pgTx) Delete(bucket, key []byte) error {
	if t.err != nil {
		return t.err
	}
	q, args := t.query("DELETE FROM kv WHERE bucket = ? AND k = ?", string(bucket), key)
	_, err := t.tx.ExecContext(t.context(), q, args...)
	if err != nil {
		return t.fail(err)
	}
	return nil
}

func (t *pgTx) Range(bucket []byte, r Range, fn func(k, v []byte) bool) {
	if t.err != nil {
		return
	}
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
	stmt, args := t.query(q, args...)
	rows, err := t.tx.QueryContext(t.context(), stmt, args...)
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

func (t *pgTx) Count(bucket []byte) int {
	if t.err != nil {
		return 0
	}
	q, args := t.query("SELECT COUNT(*) FROM kv WHERE bucket = ?", string(bucket))
	var n int
	if err := t.tx.QueryRowContext(t.context(), q, args...).Scan(&n); err != nil {
		_ = t.fail(err)
		return 0
	}
	return n
}

func (t *pgTx) NextSequence(bucket []byte) (uint64, error) {
	if t.err != nil {
		return 0, t.err
	}
	if !t.write {
		return 0, errors.New("a sequence advances only in an Update")
	}
	// sequences.seq names the row's own value (a bare seq would read as the new row's in
	// PostgreSQL, and as ambiguous outright)
	q, args := t.query(`INSERT INTO sequences (bucket, seq) VALUES (?, 1)
		ON CONFLICT (bucket) DO UPDATE SET seq = sequences.seq + 1 RETURNING sequences.seq`, string(bucket))
	var seq uint64
	err := t.tx.QueryRowContext(t.context(), q, args...).Scan(&seq)
	if err != nil {
		return 0, t.fail(err)
	}
	return seq, nil
}

func (t *pgTx) SetSequence(bucket []byte, seq uint64) error {
	if t.err != nil {
		return t.err
	}
	if !t.write {
		return errors.New("a sequence is set only in an Update")
	}
	q, args := t.query(`INSERT INTO sequences (bucket, seq) VALUES (?, ?)
		ON CONFLICT (bucket) DO UPDATE SET seq = excluded.seq`, string(bucket), seq)
	_, err := t.tx.ExecContext(t.context(), q, args...)
	if err != nil {
		return t.fail(err)
	}
	return nil
}

func (t *pgTx) Sequence(bucket []byte) uint64 {
	if t.err != nil {
		return 0
	}
	q, args := t.query("SELECT seq FROM sequences WHERE bucket = ?", string(bucket))
	var seq uint64
	err := t.tx.QueryRowContext(t.context(), q, args...).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		_ = t.fail(err)
		return 0
	}
	return seq
}

func (t *pgTx) CompareAndSwap(bucket, key, old, value []byte) error {
	if t.err != nil {
		return t.err
	}
	if !t.write {
		return errors.New("a compare-and-swap runs only in an Update")
	}
	ctx := t.context()
	switch {
	case old == nil && value == nil:
		q, args := t.query("DELETE FROM kv WHERE bucket = ? AND k = ?", string(bucket), key)
		_, err := t.tx.ExecContext(ctx, q, args...)
		if err != nil {
			return t.fail(err)
		}
		return nil // absent already: nothing to hold
	case old == nil:
		if value == nil {
			value = []byte{}
		}
		// ON CONFLICT DO NOTHING keeps the transaction alive: a unique-violation INSERT would
		// abort it, and every later Get would look like a missing key.
		q, args := t.query("INSERT INTO kv (bucket, k, v) VALUES (?, ?, ?) ON CONFLICT (bucket, k) DO NOTHING",
			string(bucket), key, value)
		res, err := t.tx.ExecContext(ctx, q, args...)
		if err != nil {
			return t.fail(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: %s", ErrCompareFailed, "the key exists")
		}
		return nil
	case value == nil:
		q, args := t.query("DELETE FROM kv WHERE bucket = ? AND k = ? AND v = ?", string(bucket), key, old)
		res, err := t.tx.ExecContext(ctx, q, args...)
		if err != nil {
			return t.fail(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrCompareFailed
		}
		return nil
	default:
		q, args := t.query("UPDATE kv SET v = ? WHERE bucket = ? AND k = ? AND v = ?", value, string(bucket), key, old)
		res, err := t.tx.ExecContext(ctx, q, args...)
		if err != nil {
			return t.fail(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrCompareFailed
		}
		return nil
	}
}
