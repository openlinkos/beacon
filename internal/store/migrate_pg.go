package store

// The move from SQLite to PostgreSQL (`beacon storage migrate --to postgres`): everything the
// SQLite database holds — the key-value space, the sequences, the event tables with their chains,
// checkpoints and signer keys — is copied across in one PostgreSQL transaction, the counts are
// checked against the source before that transaction commits, and a genesis record notes what came
// over. The SQLite file is not touched: the service that runs on SQLite keeps working until the
// configuration names the PostgreSQL DSN, and rolling back is running on the SQLite file again
// (what was written to PostgreSQL after the migration is not in it, as the plan's §10.3 says of
// the bbolt move).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/kv"
)

// MigrateToPostgres copies the SQLite database at src.Path into the PostgreSQL database dst names,
// when the source is there and the destination holds nothing yet. It reports whether it migrated;
// a missing source database or a destination with anything in it is no error, simply nothing to do.
func MigrateToPostgres(src, dst Options) (bool, error) {
	if !src.isSQLite() {
		return false, errors.New("the source must be a SQLite database: leave storage.driver at sqlite")
	}
	if _, err := os.Stat(src.Path); errors.Is(err, os.ErrNotExist) {
		return false, nil // no SQLite database to copy
	} else if err != nil {
		return false, err
	}
	source, err := kv.OpenSQLite(kv.SQLiteOptions{Path: src.Path, JournalMode: src.JournalMode,
		Synchronous: src.Synchronous, ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer func() { _ = source.Close() }()
	pg, err := kv.OpenPostgres(kv.PostgresOptions{DSN: dst.DSN, PoolSize: dst.PoolSize, StatementTimeout: dst.StatementTimeout})
	if err != nil {
		return false, err
	}
	// opening the logs makes the tables and a signer key of the destination's own; the copy then
	// adopts the source's keys (in event_keys), so the checkpoints that come over still verify —
	// and no checkpoint is written while the move runs (an interval nothing can reach), for the
	// first one after the move covers everything, signed with the adopted key
	migOpts := events.Options{Dialect: events.PostgreSQL, SignKey: dst.SignKey, CheckpointInterval: never, Now: time.Now}
	audit, err := events.Open(pg.Handle(), pg.WriteHandle(), "audit_events", migOpts)
	if err != nil {
		_ = pg.Close()
		return false, fmt.Errorf("%w: %w", ErrMigrating, err)
	}
	if _, err := events.Open(pg.Handle(), pg.WriteHandle(), "secret_uses", migOpts); err != nil {
		_ = pg.Close()
		return false, fmt.Errorf("%w: %w", ErrMigrating, err)
	}
	empty, err := nothingIn(pg)
	if err != nil {
		_ = pg.Close()
		return false, err
	}
	if !empty {
		_ = pg.Close() // migrated already, or someone else's database
		return false, nil
	}
	reader := &sqlReader{db: source}
	err = pg.UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		genesis, err := copyBuckets(tx, reader)
		if err != nil {
			return err
		}
		if err := copyEventTables(source, raw); err != nil {
			return err
		}
		// the genesis record continues the chain the copy just laid down
		event := events.Event{Actor: "beacon", Via: "system", Action: "migrate", Detail: genesis}
		if err := audit.AppendTx(raw, &event); err != nil {
			return err
		}
		return checkMigratedCounts(tx, raw, reader, source)
	})
	if err != nil {
		_ = pg.Close()
		return false, fmt.Errorf("%w: %w", ErrMigrating, err)
	}
	if err := pg.Close(); err != nil {
		return false, fmt.Errorf("%w: the database is in place, but closing it failed: %w", ErrMigrating, err)
	}
	return true, nil
}

// sqlReader reads a SQLite database the way the bbolt migration reads its source: the store's
// buckets in key order, with counts and sequences from the database itself.
type sqlReader struct {
	db *kv.SQLite
}

func (r *sqlReader) Buckets() [][]byte { return Buckets }

func (r *sqlReader) View(fn func(kv.ROTx) error) error {
	return r.db.View(func(tx kv.Tx) error { return fn(roTx{tx}) })
}

func (r *sqlReader) Close() error { return nil }

// roTx is a write-shaped kv.Tx read as the migration's read-only one.
type roTx struct{ tx kv.Tx }

func (t roTx) Get(bucket, key []byte) []byte                         { return t.tx.Get(bucket, key) }
func (t roTx) Range(b []byte, r kv.Range, fn func(k, v []byte) bool) { t.tx.Range(b, r, fn) }
func (t roTx) Count(bucket []byte) int                               { return t.tx.Count(bucket) }
func (t roTx) Sequence(bucket []byte) uint64                         { return t.tx.Sequence(bucket) }

// never is a checkpoint interval nothing can reach: the migration writes no checkpoint of its own.
var never = 87600 * 24 * time.Hour

// eventCols is the event tables' shared column list.
const eventCols = "seq, time, actor, on_behalf, via, action, object, detail, reason, context, ip, client, hash, prev_hash"

// copyEventTables takes the event tables' rows as they are: the records with their chain, the
// checkpoints with their signatures, the signer keys. The chain a copy leaves behind verifies, and
// anything appended (the genesis record above) continues from it.
func copyEventTables(src *kv.SQLite, raw *sql.Tx) error {
	ctx := context.Background()
	copyRows := func(table, cols string) error {
		rows, err := src.Handle().QueryContext(ctx, `SELECT `+cols+` FROM `+table)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		n := countCommas(cols) + 1
		ins := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`, table, cols, repeatMarks(n)) //nolint:gosec // the table and columns are this file's own constants
		vals := make([]any, n)
		scan := make([]any, n)
		for i := range scan {
			scan[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(scan...); err != nil {
				return err
			}
			if _, err := raw.ExecContext(ctx, ins, vals...); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	for _, t := range []string{"audit_events", "secret_uses"} {
		if err := copyRows(t, eventCols); err != nil {
			return err
		}
	}
	if err := copyRows("event_checkpoints", "log, seq, first_seq, count, head, time, prev, hash, signer, signature"); err != nil {
		return err
	}
	return copyKeys(src, raw)
}

// copyKeys adopts the source's signer keys over the ones the destination generated when it opened:
// the checkpoints that came over were signed with them, and the chain verifies only with the same.
func copyKeys(src *kv.SQLite, raw *sql.Tx) error {
	ctx := context.Background()
	rows, err := src.Handle().QueryContext(ctx, `SELECT log, key FROM event_keys`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type pair struct {
		log string
		key []byte
	}
	var keys []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.log, &p.key); err != nil {
			return err
		}
		keys = append(keys, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range keys {
		if _, err := raw.ExecContext(ctx, `INSERT INTO event_keys (log, key) VALUES ($1, $2)
			ON CONFLICT (log) DO UPDATE SET key = excluded.key`, p.log, p.key); err != nil {
			return err
		}
	}
	return nil
}

func countCommas(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			n++
		}
	}
	return n
}

// repeatMarks writes the PostgreSQL placeholders the destination takes: $1, $2, …
func repeatMarks(n int) string {
	out := ""
	for i := 1; i <= n; i++ {
		if i > 1 {
			out += ", "
		}
		out += "$" + strconv.Itoa(i)
	}
	return out
}

// checkMigratedCounts holds the copy to what the source held, before the transaction commits: every
// bucket's count and sequence, and the event tables' rows (the audit's with the genesis record's
// one more).
func checkMigratedCounts(tx kv.Tx, raw *sql.Tx, src kv.Reader, sqlite *kv.SQLite) error {
	if err := checkCounts(tx, raw, src); err != nil {
		return err
	}
	ctx := context.Background()
	for _, table := range []string{"audit_events", "secret_uses", "event_checkpoints", "event_keys"} {
		var want, got int64
		if err := sqlite.Handle().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&want); err != nil {
			return err
		}
		if err := raw.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
			return err
		}
		if table == "audit_events" && got == want+1 { // the genesis record makes one more
			continue
		}
		if got != want {
			return fmt.Errorf("%s: copied %d of %d rows", table, got, want)
		}
	}
	return nil
}
