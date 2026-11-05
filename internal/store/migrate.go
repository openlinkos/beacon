package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/kv"
)

// The move from the bbolt file to the SQL database (the plan's §10.3): when the service first
// starts with a bbolt file beside an empty (or absent) SQL database, everything in it is copied
// across in one transaction — the buckets become the key-value space as they are, the audit
// bucket becomes the event table's first records — a genesis record notes what came over, the
// counts are checked before the transaction commits, and the old file is kept as
// <name>.pre-sql for a rollback. Rolling back means running the old version on that file again;
// what was written to the SQL database after the migration is not in it.

// ErrMigrating wraps what went wrong mid-migration; nothing is left half-moved: the SQL database
// a failed migration was filling is removed, so the next start tries again.
var ErrMigrating = errors.New("the migration did not complete")

// ErrDiverged means a bbolt file and a SQL database both exist and the bbolt file is not the one
// this SQL database was migrated from. Starting would ignore one of them.
var ErrDiverged = errors.New("the bbolt file and the SQL database both exist and they do not match")

// migrateFailAt is set by tests to fail the next Migrate at a named step ("inside" after the
// copy, "after-commit" after the SQL transaction is in place). Production never sets it.
var migrateFailAt string

// openBolt is kv.OpenBolt; tests replace it to count opens. Production never sets it.
var openBolt = kv.OpenBolt

// syncDirectory is syncDir; tests replace it to count the directory fsync. Production never sets it.
var syncDirectory = syncDir

// Migrate copies the bbolt database at boltPath into the SQL database o names, when there is a
// bbolt file to copy and the SQL database holds nothing yet. It reports whether it migrated; a
// missing bbolt file or a database with anything in it is no error, simply nothing to do.
func Migrate(boltPath string, o Options) (bool, error) {
	if _, err := os.Stat(boltPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil // never had a bbolt file
		}
		return false, err
	}
	db, err := kv.OpenSQLite(kv.SQLiteOptions{Path: o.Path, JournalMode: o.JournalMode, Synchronous: o.Synchronous, BusyTimeout: o.BusyTimeout})
	if err != nil {
		return false, err
	}
	// the event tables first: the emptiness check reads them
	audit, err := events.Open(db.Handle(), db.WriteHandle(), "audit_events",
		events.Options{SignKey: o.SignKey, CheckpointInterval: o.CheckpointInterval, Now: time.Now})
	if err != nil {
		_ = db.Close()
		return false, fmt.Errorf("%w: %w", ErrMigrating, err)
	}
	if _, err := events.Open(db.Handle(), db.WriteHandle(), "secret_uses",
		events.Options{SignKey: o.SignKey, CheckpointInterval: o.CheckpointInterval, Now: time.Now}); err != nil {
		_ = db.Close()
		return false, fmt.Errorf("%w: %w", ErrMigrating, err)
	}
	empty, err := nothingIn(db)
	if err != nil {
		_ = db.Close()
		return false, err
	}
	if !empty {
		finished, err := finishOrRefuse(db, o.Path, boltPath)
		_ = db.Close()
		return finished, err
	}
	done := false
	defer func() {
		if !done {
			_ = db.Close()
			_ = os.Remove(o.Path) // a failed migration leaves no half-moved database behind
			_ = os.Remove(o.Path + "-wal")
			_ = os.Remove(o.Path + "-shm")
			_ = os.Remove(o.Path + ".lock")
			_ = os.Remove(boltPath + ".pre-sql")
		}
	}()
	src, err := openBolt(kv.BoltOptions{Path: boltPath})
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrMigrating, err)
	}
	// the backup first: a consistent copy of the same open the copy will read
	if err := writeBoltBackup(src, boltPath+".pre-sql"); err != nil {
		_ = src.Close()
		return false, fmt.Errorf("%w: %w", ErrMigrating, err)
	}

	// everything in one transaction: the buckets, the sequences, the genesis record, the audit
	// entries — and the count check before it commits
	migErr := db.UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		genesis, err := copyBuckets(tx, src)
		if err != nil {
			return err
		}
		decoded, skipped, err := copyAudit(audit, raw, src, genesis)
		if err != nil {
			return err
		}
		if err := checkCounts(tx, raw, src, decoded, skipped); err != nil {
			return err
		}
		if migrateFailAt == "inside" {
			return errors.New("injected failure")
		}
		return nil
	})
	if migErr != nil {
		_ = src.Close()
		return false, fmt.Errorf("%w: %w", ErrMigrating, migErr)
	}
	if err := db.Close(); err != nil {
		_ = src.Close()
		return false, fmt.Errorf("%w: %w", ErrMigrating, err)
	}
	if err := src.Close(); err != nil {
		done = true // the SQL database is in place; do not delete it
		return false, fmt.Errorf("%w: the database is in place, but the old file's handle stays: %w", ErrMigrating, err)
	}
	done = true
	if migrateFailAt == "after-commit" {
		return false, fmt.Errorf("%w: injected crash after commit", ErrMigrating)
	}
	// the .pre-sql copy is the record to roll back to; the file the old version watched is free
	// to go, so the old version does not start on stale state by accident
	if err := os.Remove(boltPath); err != nil {
		return false, fmt.Errorf("%w: the database is in place, but the old file stays: %w", ErrMigrating, err)
	}
	return true, nil
}

// nothingIn reports whether the SQL database holds nothing of Beacon's: no key-value pairs, no
// events, no checkpoints.
func nothingIn(db *kv.SQLite) (bool, error) {
	var n int
	if err := db.Handle().QueryRowContext(context.Background(), `SELECT
		(SELECT COUNT(*) FROM kv) + (SELECT COUNT(*) FROM sequences)
		+ (SELECT COUNT(*) FROM audit_events) + (SELECT COUNT(*) FROM secret_uses)
		+ (SELECT COUNT(*) FROM event_checkpoints)`).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// finishOrRefuse runs when a bbolt file and a non-empty SQL database both exist: crash between
// commit and removing the old file, or a rollback that then ran the old version. The import hash
// of the bbolt file is compared to the genesis record. Matching means the SQL database is this
// bbolt file's migration and the leftover file can go; mismatch means the two have diverged and
// starting would drop one of them.
func finishOrRefuse(db *kv.SQLite, sqlPath, boltPath string) (bool, error) {
	genesis, err := readGenesis(db)
	if err != nil {
		return false, fmt.Errorf("%w: the SQL database at %s is not empty and has no migration genesis, while %s is still there. If the SQL database is the one to keep, move the bbolt file aside. To remigrate, move the SQL database aside and start again",
			ErrDiverged, sqlPath, boltPath)
	}
	src, err := openBolt(kv.BoltOptions{Path: boltPath})
	if err != nil {
		return false, err
	}
	_, hash, err := hashSource(src)
	_ = src.Close()
	if err != nil {
		return false, err
	}
	if hash != genesis {
		return false, fmt.Errorf("%w: %s was not the file this database was migrated from (or it changed after a rollback). Keep running the old version on the bbolt file, or restore the SQL database from a backup taken before the rollback. If the SQL database is the one to keep, move the bbolt file aside",
			ErrDiverged, boltPath)
	}
	if _, err := os.Stat(boltPath + ".pre-sql"); errors.Is(err, os.ErrNotExist) {
		src, err := openBolt(kv.BoltOptions{Path: boltPath})
		if err != nil {
			return false, err
		}
		werr := writeBoltBackup(src, boltPath+".pre-sql")
		_ = src.Close()
		if werr != nil {
			return false, werr
		}
	}
	if err := os.Remove(boltPath); err != nil {
		return false, err
	}
	return true, nil
}

func readGenesis(db *kv.SQLite) (string, error) {
	var detail string
	err := db.Handle().QueryRowContext(context.Background(),
		`SELECT detail FROM audit_events WHERE seq = 1 AND action = 'migrate'`).Scan(&detail)
	if err != nil {
		return "", err
	}
	_, hash, ok := strings.Cut(detail, "import hash ")
	if !ok {
		return "", fmt.Errorf("the genesis record has no import hash: %s", detail)
	}
	hash, _, _ = strings.Cut(hash, ";")
	return strings.TrimSpace(hash), nil
}

func writeBoltBackup(src *kv.Bolt, dest string) error {
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // the operator names the file
	if err != nil {
		return err
	}
	if _, err := src.WriteTo(f); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return syncDirectory(dest)
}

func syncDir(path string) error {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

// hashSource is the import hash of a bbolt file: every bucket, including audit, length-prefixed.
// finishOrRefuse compares it to the genesis record.
func hashSource(src kv.Reader) (copied int, hash string, err error) {
	h := sha256.New()
	err = src.View(func(ro kv.ROTx) error {
		for _, name := range src.Buckets() {
			ro.Range(name, kv.Range{}, func(k, v []byte) bool {
				var l [4]byte
				binary.BigEndian.PutUint32(l[:], uint32(len(k)))
				h.Write(l[:])
				h.Write(k)
				binary.BigEndian.PutUint32(l[:], uint32(len(v)))
				h.Write(l[:])
				h.Write(v)
				copied++
				return true
			})
		}
		return nil
	})
	return copied, hex.EncodeToString(h.Sum(nil)), err
}

// copyBuckets copies every bucket as it is, and the sequences with them. It returns what the
// genesis record says: how many entries came over and the hash of all of them.
func copyBuckets(tx kv.Tx, src kv.Reader) (string, error) {
	copied, hash, err := hashSource(src)
	if err != nil {
		return "", err
	}
	err = src.View(func(ro kv.ROTx) error {
		for _, name := range src.Buckets() {
			if string(name) == string(bucketAudit) {
				continue // the event table takes these
			}
			var putErr error
			ro.Range(name, kv.Range{}, func(k, v []byte) bool {
				if err := tx.Put(name, k, v); err != nil {
					putErr = err
					return false
				}
				return true
			})
			if putErr != nil {
				return putErr
			}
			if seq := ro.Sequence(name); seq > 0 {
				if err := tx.SetSequence(name, seq); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d entries from bbolt, import hash %s", copied, hash), nil
}

// copyAudit turns the audit bucket into the event table's first records: a genesis record of the
// migration, then the entries in their order, chained. (bbolt's audit keys are 8-byte big-endian
// sequence numbers, so byte order is record order.)
func copyAudit(audit *events.Log, raw *sql.Tx, src kv.Reader, genesis string) (decoded, skipped int, err error) {
	var entries []AuditEntry
	_ = src.View(func(ro kv.ROTx) error {
		ro.Range(bucketAudit, kv.Range{}, func(_, v []byte) bool {
			var e AuditEntry
			if json.Unmarshal(v, &e) != nil {
				skipped++
				return true
			}
			entries = append(entries, e)
			decoded++
			return true
		})
		return nil
	})
	if skipped > 0 {
		genesis += fmt.Sprintf("; skipped %d audit entries that did not decode", skipped)
	}
	genesisEvent := events.Event{Actor: "beacon", Via: "system", Action: "migrate", Detail: genesis}
	if err := audit.AppendTx(raw, &genesisEvent); err != nil {
		return decoded, skipped, err
	}
	if genesisEvent.Seq != 1 {
		return decoded, skipped, fmt.Errorf("the genesis record is not first: seq %d", genesisEvent.Seq)
	}
	for _, e := range entries {
		ev := events.Event{
			Time: e.Time, Actor: e.Actor, Via: e.Via, Action: e.Action, Object: e.Target, Detail: e.Detail,
		}
		if ev.Time.IsZero() {
			ev.Time = time.Now().UTC() // entries from before times were kept
		}
		if err := audit.AppendTx(raw, &ev); err != nil {
			return decoded, skipped, err
		}
	}
	return decoded, skipped, nil
}

// checkCounts holds the migration to what it copied, before the transaction commits: every
// bucket's count and sequence, and the audit entries the event table took.
func checkCounts(tx kv.Tx, raw *sql.Tx, src kv.Reader, decoded, skipped int) error {
	ctx := context.Background()
	err := src.View(func(ro kv.ROTx) error {
		for _, name := range src.Buckets() {
			want := ro.Count(name)
			if string(name) == string(bucketAudit) {
				var got int64
				if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events`).Scan(&got); err != nil {
					return err
				}
				if decoded+skipped != want {
					return fmt.Errorf("audit: decoded %d and skipped %d of %d entries", decoded, skipped, want)
				}
				if got != int64(decoded)+1 { // the genesis record makes one more
					return fmt.Errorf("audit: the event table took %d of %d entries", got-1, decoded)
				}
				continue
			}
			if got := tx.Count(name); got != want {
				return fmt.Errorf("bucket %s: copied %d of %d entries", name, got, want)
			}
			if seq := ro.Sequence(name); tx.Sequence(name) != seq {
				return fmt.Errorf("bucket %s: sequence %d, not %d", name, tx.Sequence(name), seq)
			}
		}
		return nil
	})
	return err
}
