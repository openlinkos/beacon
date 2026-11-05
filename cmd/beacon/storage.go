package main

// The storage itself: where it lives (the storage section of the configuration), and the checks
// the command line runs against the database file — the audit chain's verification and the
// database's health. Both open read-only, so they work while the service runs.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/store"
)

// storagePath is the database the configuration names, or the default beside the data directory's
// other files.
func storagePath(cfg *config.Config) string {
	if cfg.Storage.Path != "" {
		return cfg.Storage.Path
	}
	return filepath.Join(cfg.DataDir, "beacon.db")
}

// storeOptions is how the service opens the database: the storage section of the configuration
// over the plan's defaults.
func storeOptions(cfg *config.Config) store.Options {
	return store.Options{
		Path:               storagePath(cfg),
		JournalMode:        cfg.Storage.SQLite.JournalMode,
		Synchronous:        cfg.Storage.SQLite.Synchronous,
		BusyTimeout:        cfg.Storage.SQLite.BusyTimeout.D(),
		CheckpointInterval: cfg.Storage.Audit.CheckpointInterval.D(),
		Retention:          cfg.Storage.Audit.Retention.D(),
	}
}

// openRead opens the database for the checks: read-only, so the service may be running; a missing
// database or one left mid-write by a killed service says so rather than pretending.
func openRead(stderr io.Writer, cfg *config.Config) (*store.Store, bool) {
	path := storagePath(cfg)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(filepath.Join(cfg.DataDir, "status.db")); err == nil {
			fmt.Fprintf(stderr, "no SQL database at %s yet; the service migrates the bbolt file there when it next starts\n", path)
		} else {
			fmt.Fprintf(stderr, "no database at %s\n", path)
		}
		return nil, false
	}
	st, err := store.OpenReadWith(store.Options{Path: path, Retention: cfg.Storage.Audit.Retention.D()})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, false
	}
	return st, true
}

const auditUsage = "audit verify [-from-seq N]"

// auditCmd verifies the audit log's hash chain: from the checkpoint at -from-seq, or over
// everything still present (anchored at the checkpoint that covers it).
func auditCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "verify" {
		fmt.Fprintln(stderr, auditUsage)
		return 2
	}
	fs := flag.NewFlagSet("audit verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", defaultConfig, "configuration file")
	from := fs.Uint64("from-seq", 0, "verify from the checkpoint covering this record (default: all)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	st, ok := openRead(stderr, cfg)
	if !ok {
		return 1
	}
	defer func() { _ = st.Close() }()
	var res store.Result
	if *from > 0 {
		res, err = st.VerifyAuditSince(*from)
	} else {
		res, err = st.VerifyAudit()
	}
	if err != nil {
		fmt.Fprintf(stdout, "audit chain broken at %d: %v\n", res.BrokenAt, err)
		return 1
	}
	cps, cpErr := st.AuditCheckpoints()
	last := "none yet"
	if cpErr == nil && len(cps) > 0 {
		last = fmt.Sprintf("%d records, %s", cps[len(cps)-1].Seq, cps[len(cps)-1].Time.Format(time.RFC3339))
	}
	fmt.Fprintf(stdout, "audit chain OK: %d records from %d to %d, %d checkpoints (last covers %s)\n",
		res.Events, res.FirstSeq, res.LastSeq, res.Checkpoints, last)
	if res.Events == 0 && res.Head == "" {
		fmt.Fprintln(stdout, "the audit log is empty")
	}
	return 0
}

const storageUsage = "storage check"

// storageCmd checks the database's health: SQLite's own integrity check, what the buckets hold,
// and the audit chain's verification.
func storageCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(stderr, storageUsage)
		return 2
	}
	fs := flag.NewFlagSet("storage check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", defaultConfig, "configuration file")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	st, ok := openRead(stderr, cfg)
	if !ok {
		return 1
	}
	defer func() { _ = st.Close() }()
	db := st.Storage()
	failed := false
	say := func(format string, args ...any) { fmt.Fprintf(stdout, format+"\n", args...) }
	say("storage: %s at %s", cfg.Storage.Driver, storagePath(cfg))
	say("journal %s, %d bytes", db.JournalMode(), db.Size())
	health, err := db.IntegrityCheck()
	if err != nil {
		say("integrity: could not check: %v", err)
		failed = true
	} else if health != "ok" {
		say("integrity: %s", strings.ReplaceAll(health, "\n", "; "))
		failed = true
	} else {
		say("integrity: ok")
	}
	var names []string
	counts := map[string]int{}
	_ = db.View(func(tx kv.Tx) error {
		for _, b := range store.Buckets {
			if n := tx.Count(b); n > 0 {
				names = append(names, string(b))
				counts[string(b)] = n
			}
		}
		return nil
	})
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s %d", n, counts[n]))
	}
	say("buckets: %s", strings.Join(parts, ", "))
	entries, total, err := st.AuditQuery(store.AuditFilter{Limit: 1})
	if err != nil {
		say("audit: could not read: %v", err)
		failed = true
	} else {
		what := "empty"
		if total > 0 {
			what = fmt.Sprintf("%d entries, newest %s", total, entries[0].Time.Format(time.RFC3339))
		}
		say("audit: %s", what)
	}
	if res, err := st.VerifyAudit(); err != nil {
		say("audit chain: broken at %d: %v", res.BrokenAt, err)
		failed = true
	} else {
		say("audit chain: OK, %d records, head %s…", res.Events, shortHash(res.Head))
	}
	if failed {
		return 1
	}
	return 0
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
