package backup

// The database half of a backup: an online-consistent snapshot of the SQLite database
// (VACUUM INTO, as the plan's §11 names it), the checks made on it before it is encrypted
// (PRAGMA integrity_check, every table's row count, the audit chain), and the same chain check
// on a restored database. All of this runs on read-only connections, so a backup works while
// the service holds the database, and a restore's verification opens what it just wrote.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/openlinkos/beacon/internal/events"

	_ "modernc.org/sqlite" // pure Go, no cgo: the same driver the store uses
)

// openRO opens a read-only connection. A WAL database left mid-write by a killed service cannot
// be recovered read-only; that is the caller's error to explain (run the service once, or
// restore instead). The journal mode is the file's own property and is never set here: setting
// it would be a write, and a read-only connection refuses one.
func openRO(ctx context.Context, path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(filepath.Clean(path)),
		RawQuery: "mode=ro&_pragma=busy_timeout(15000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	return db, db.PingContext(ctx)
}

// Snapshot writes an online-consistent copy of the database at dbPath to the file at out
// (which must not exist) and reports what the copy holds: its schema version and every table's
// row count. The counts come from the snapshot itself, so what the manifest says is what the
// backup carries, not what the live database happened to hold a moment later.
func Snapshot(ctx context.Context, dbPath, out string) (schema int, tables map[string]int64, err error) {
	db, err := openRO(ctx, dbPath)
	if err != nil {
		return 0, nil, fmt.Errorf("open %s read-only: %w", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	if _, err := os.Stat(out); err == nil {
		return 0, nil, fmt.Errorf("%s already exists", out)
	}
	// VACUUM INTO writes a new file from a consistent read; its target cannot be a bound
	// parameter, and the path is the backup's own temporary file, never input.
	if _, err := db.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(out, "'", "''")+"'"); err != nil { //nolint:gosec // the path is the backup's own temporary file
		return 0, nil, fmt.Errorf("vacuum into: %w", err)
	}
	return Inspect(ctx, out)
}

// Inspect reports a database's schema version and every table's row count. The key-value space
// is one table over many buckets, so its buckets are counted too, as kv/<bucket>: the
// manifest's "how much each table holds" should read the way the store does.
func Inspect(ctx context.Context, path string) (schema int, tables map[string]int64, err error) {
	db, err := openRO(ctx, path)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = db.Close() }()
	if err := db.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&schema); err != nil {
		return 0, nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return 0, nil, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return 0, nil, err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, nil, err
	}
	_ = rows.Close()
	tables = map[string]int64{}
	for _, n := range names {
		var c int64
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+n+`"`).Scan(&c); err != nil {
			return 0, nil, err
		}
		tables[n] = c
	}
	kvRows, err := db.QueryContext(ctx, `SELECT bucket, COUNT(*) FROM kv GROUP BY bucket ORDER BY bucket`)
	if err == nil {
		for kvRows.Next() {
			var b string
			var c int64
			if err := kvRows.Scan(&b, &c); err != nil {
				break
			}
			tables["kv/"+b] = c
		}
		_ = kvRows.Close()
	}
	return schema, tables, nil
}

// IntegrityCheck runs PRAGMA integrity_check on the database at path and fails unless it says ok.
func IntegrityCheck(ctx context.Context, path string) error {
	db, err := openRO(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var res string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("integrity_check: %s", res)
	}
	return nil
}

// VerifyAudit opens the audit log's tables in the database at path read-only and walks the hash
// chain exactly as the service does (events.Open with no write pool), from the checkpoint that
// anchors the records still present. The signer key is read from the database's own event_keys
// table, which the snapshot carries, so a restored database verifies with nothing else. A
// database without the tables yet (nothing ever audited, or a pre-migration copy) verifies as
// empty rather than failing: the chain check is against tampering, not against emptiness.
func VerifyAudit(ctx context.Context, path string) (events.Result, error) {
	db, err := openRO(ctx, path)
	if err != nil {
		return events.Result{}, err
	}
	defer func() { _ = db.Close() }()
	var one int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'audit_events'`).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return events.Result{}, nil
	}
	if err != nil {
		return events.Result{}, err
	}
	log, err := events.Open(db, nil, "audit_events", events.Options{})
	if err != nil {
		return events.Result{}, err
	}
	return log.Verify()
}
