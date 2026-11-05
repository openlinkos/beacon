package events

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

type fixture struct {
	db  kv.SQL
	log *Log
	now time.Time
}

func open(t *testing.T, o Options) *fixture {
	t.Helper()
	f := &fixture{now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	if o.Now == nil {
		o.Now = func() time.Time { return f.now }
	}
	// SQLite, or PostgreSQL when BEACON_TEST_POSTGRES_DSN names one: the same chain tests run on
	// both (CI runs the suite twice), with the dialect doing no more than the placeholder spelling
	if dsn := os.Getenv("BEACON_TEST_POSTGRES_DSN"); dsn != "" {
		pg, err := kv.OpenPostgres(kv.PostgresOptions{DSN: testDatabase(t, dsn)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pg.Close() })
		f.db = pg
		o.Dialect = PostgreSQL
	} else {
		db, err := kv.OpenSQLite(kv.SQLiteOptions{Path: filepath.Join(t.TempDir(), "beacon.db")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		f.db = db
	}
	log, err := Open(f.db.Handle(), f.db.WriteHandle(), "audit_events", o)
	if err != nil {
		t.Fatal(err)
	}
	f.log = log
	return f
}

// testDatabase makes a database of the test's own on the server the DSN names, dropped when the
// test ends. It mirrors internal/kv's helper (test packages cannot share code).
func testDatabase(t *testing.T, dsn string) string {
	t.Helper()
	raw := make([]byte, 4)
	_, _ = rand.Read(raw)
	name := "beacon_test_" + hex.EncodeToString(raw)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	t.Cleanup(func() {
		// the store's pools close asynchronously; FORCE drops through what is still open, but a
		// connection in flight can beat it — try again before giving up
		for range 3 {
			dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, err := admin.ExecContext(dctx, "DROP DATABASE "+name+" WITH (FORCE)")
			dcancel()
			if err == nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	})
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" && u.Host != "" {
		u.Path = "/" + name
		return u.String()
	}
	for _, k := range []string{"dbname", "database"} {
		if i := strings.Index(dsn, k+"="); i >= 0 {
			rest := dsn[i+len(k)+1:]
			if j := strings.IndexAny(rest, " 	"); j >= 0 {
				rest = rest[j:]
			} else {
				rest = ""
			}
			return dsn[:i] + k + "=" + name + rest
		}
	}
	return dsn + " dbname=" + name
}

func TestAppendAndQuery(t *testing.T) {
	f := open(t, Options{})
	base := f.now
	for i := range 5 {
		e, err := f.log.Append(Event{Actor: "alice", Action: "ack", Object: fmt.Sprintf("g%d", i%2), Detail: fmt.Sprintf("note %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if e.Seq != uint64(i+1) || e.Hash == "" || e.PrevHash == "" && i > 0 {
			t.Fatalf("append %d: %+v", i, e)
		}
		f.now = base.Add(time.Duration(i+1) * time.Minute)
	}
	// the chain runs
	got, total, err := f.log.Query(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(got) != 5 || got[0].Seq != 5 {
		t.Fatalf("query: %d %d first %d", total, len(got), got[0].Seq)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Hash != got[i-1].PrevHash {
			t.Fatalf("chain: %d does not follow %d", got[i].Seq, got[i-1].Seq)
		}
	}
	// filters
	got, total, _ = f.log.Query(Filter{Actor: "alice", Action: "ack", Object: "g1"})
	if total != 2 || len(got) != 2 { // g1 is the second and the fourth
		t.Fatalf("filtered: %d %d", total, len(got))
	}
	got, total, _ = f.log.Query(Filter{From: base.Add(90 * time.Second), To: base.Add(3 * time.Minute)})
	if total != 2 || got[0].Seq != 4 || got[1].Seq != 3 { // the third and the fourth minute
		t.Fatalf("time window: %d %+v", total, got)
	}
	// pagination
	got, total, _ = f.log.Query(Filter{Limit: 2, Offset: 2})
	if total != 5 || len(got) != 2 || got[0].Seq != 3 {
		t.Fatalf("page: %d %+v", total, got)
	}
	// a wrong actor matches nothing
	_, total, _ = f.log.Query(Filter{Actor: "bob"})
	if total != 0 {
		t.Fatalf("bob: %d", total)
	}
}

func TestVerify(t *testing.T) {
	f := open(t, Options{})
	for range 4 {
		if _, err := f.log.Append(Event{Actor: "alice", Action: "ack"}); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Minute)
	}
	res, err := f.log.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != 4 || res.LastSeq != 4 || res.Head == "" {
		t.Fatalf("verify: %+v", res)
	}
	// tamper with one record: the hash no longer matches what it says of itself
	tamper(t, f.db, `UPDATE audit_events SET detail = 'gone fishing' WHERE seq = 2`)
	if _, err := f.log.Verify(); !errors.Is(err, ErrBrokenChain) {
		t.Fatalf("a changed record: %v", err)
	}
	// delete a middle record: the chain no longer runs
	restore(t, f)
	if _, err := f.log.Verify(); err != nil {
		t.Fatalf("after restore: %v", err)
	}
	tamper(t, f.db, `DELETE FROM audit_events WHERE seq = 3`)
	if _, err := f.log.Verify(); !errors.Is(err, ErrBrokenChain) {
		t.Fatalf("a deleted record: %v", err)
	}
	restore(t, f)
	// rewrite the tail without checkpoints is invisible to the walk alone, so make one and try
	if _, err := f.log.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	tamper(t, f.db, `DELETE FROM audit_events WHERE seq = 4`) // the checkpoint at 4 stands
	if _, err := f.log.Verify(); !errors.Is(err, ErrBrokenChain) {
		t.Fatalf("a deleted record under a checkpoint: %v", err)
	}
	restore(t, f)
}

// tamper runs SQL straight against the tables, as whoever gets the database file would.
func tamper(t *testing.T, db kv.SQL, q string) {
	t.Helper()
	if _, err := db.WriteHandle().Exec(q); err != nil {
		t.Fatal(err)
	}
}

// restore rebuilds the fixture: a tampered database stays tampered, so each break has its own.
// What the new fixture's log uses as its clock stays the new fixture's; set f2.now, not f's.
func restore(t *testing.T, f *fixture) {
	t.Helper()
	_ = f.db.Close()
	f2 := open(t, Options{})
	for range 4 {
		if _, err := f2.log.Append(Event{Actor: "alice", Action: "ack"}); err != nil {
			t.Fatal(err)
		}
		f2.now = f2.now.Add(time.Minute)
	}
	*f = *f2
}

func TestCheckpoints(t *testing.T) {
	f := open(t, Options{CheckpointInterval: time.Hour})
	for range 3 {
		if _, err := f.log.Append(Event{Actor: "alice", Action: "a"}); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(30 * time.Minute)
	}
	cps, err := f.log.Checkpoints()
	if err != nil {
		t.Fatal(err)
	}
	// the first record is checkpointed at once, then one every hour: after 12:00, 12:30 and
	// 13:00 that is a checkpoint at 1 and one at 3
	if len(cps) != 2 || cps[0].Seq != 1 || cps[1].Seq != 3 || cps[1].Count != 2 || cps[1].FirstSeq != 2 {
		t.Fatalf("checkpoints after an hour: %+v", cps)
	}
	// the checkpoint matches the chain
	res, err := f.log.Verify()
	if err != nil || res.Events != 3 {
		t.Fatalf("verify with a checkpoint: %v %+v", err, res)
	}
	// a changed checkpoint is seen: its own hash covers it
	tamper(t, f.db, `UPDATE event_checkpoints SET head = '`+cps[1].Head[:8]+`0000' WHERE seq = 3`)
	if _, err := f.log.Verify(); !errors.Is(err, ErrBrokenChain) {
		t.Fatalf("a changed checkpoint: %v", err)
	}
	// a forged signature is seen
	restore(t, f)
	if _, err := f.log.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	cps, _ = f.log.Checkpoints()
	tamper(t, f.db, `UPDATE event_checkpoints SET signature = '`+cps[len(cps)-1].Signature[:8]+`00' WHERE seq = 4`)
	if _, err := f.log.Verify(); !errors.Is(err, ErrBrokenChain) {
		t.Fatalf("a forged signature: %v", err)
	}
}

func TestPruneKeepsTheChain(t *testing.T) {
	f := open(t, Options{})
	base := f.now
	for i := range 10 {
		if _, err := f.log.Append(Event{Actor: "alice", Action: "a", Detail: fmt.Sprintf("n%d", i)}); err != nil {
			t.Fatal(err)
		}
		f.now = base.Add(time.Duration(i) * time.Hour)
	}
	if _, err := f.log.Verify(); err != nil {
		t.Fatal(err)
	}
	// eight hours of retention takes the first three (they are older than the cutoff)
	n, err := f.log.Prune(8 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("pruned %d", n)
	}
	if res, err := f.log.Verify(); err != nil || res.Events != 7 || res.FirstSeq != 4 {
		t.Fatalf("verify after prune: %v %+v", err, res)
	}
	// and the chain continues from where it was
	e, err := f.log.Append(Event{Actor: "alice", Action: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Seq != 11 || e.PrevHash == "" {
		t.Fatalf("append after prune: %+v", e)
	}
	if _, err := f.log.Verify(); err != nil {
		t.Fatal(err)
	}
	// pruning nearly everything still leaves a verifiable chain: the record at the clock's now
	// (seq 11) stays, anchored by the checkpoint the prune wrote at 10
	n, err = f.log.Prune(time.Nanosecond) // the cutoff falls before every record but that one
	if err != nil || n != 7 {
		t.Fatalf("prune all: %d %v", n, err)
	}
	if res, err := f.log.Verify(); err != nil || res.Events != 1 || res.Head == "" {
		t.Fatalf("verify after pruning the rest: %v %+v", err, res)
	}
	// verify from any checkpoint, including one whose records were pruned: the prune mark
	// re-anchors the walk
	cps, _ := f.log.Checkpoints()
	if len(cps) == 0 {
		t.Fatal("the prune left no checkpoints")
	}
	last := cps[len(cps)-1]
	if res, err := f.log.VerifySince(last.Seq); err != nil {
		t.Fatalf("verify since %d: %v %+v", last.Seq, err, res)
	}
	if res, err := f.log.VerifySince(cps[0].Seq); err != nil {
		t.Errorf("verify since a pruned checkpoint %d: %v %+v", cps[0].Seq, err, res)
	}
	if _, err := f.log.VerifySince(999); err == nil {
		t.Error("verify since a seq that is no checkpoint should say so")
	}
	// events that vanish without a checkpoint break the chain
	tamper(t, f.db, `DELETE FROM audit_events`)
	tamper(t, f.db, `DELETE FROM event_checkpoints WHERE seq > 3`)
	tamper(t, f.db, `INSERT INTO audit_events (seq, time, actor, via, action, hash, prev_hash) VALUES (5, 0, 'x', '', 'a', 'x', 'x')`)
	if _, err := f.log.Verify(); !errors.Is(err, ErrBrokenChain) {
		t.Fatalf("records without an anchor: %v", err)
	}
}

// TestVerifySinceFromAMidChainCheckpoint: a log that has never been pruned still verifies
// from a checkpoint in the middle. Walking from the first record present with that
// checkpoint's head as the expected previous hash is the regression: record 1 "does not
// continue the chain".
func TestVerifySinceFromAMidChainCheckpoint(t *testing.T) {
	f := open(t, Options{})
	for range 3 {
		if _, err := f.log.Append(Event{Actor: "alice", Action: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	at3, err := f.log.Checkpoint()
	if err != nil || at3.Seq != 3 {
		t.Fatalf("checkpoint at 3: %+v %v", at3, err)
	}
	for range 7 {
		if _, err := f.log.Append(Event{Actor: "alice", Action: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := f.log.VerifySince(3)
	if err != nil {
		t.Fatalf("verify from seq 3 on an unpruned log: %v %+v", err, res)
	}
	if res.FirstSeq != 4 || res.Events != 7 || res.LastSeq != 10 {
		t.Fatalf("walked %+v, want seq 4–10", res)
	}
}

// TestVerifySinceAfterPruneFromALaterCheckpoint: after a prune leaves records 26–72,
// VerifySince(49) must walk from 49, not report a break at 26.
func TestVerifySinceAfterPruneFromALaterCheckpoint(t *testing.T) {
	f := open(t, Options{})
	base := f.now
	var at49 Checkpoint
	for i := range 72 {
		f.now = base.Add(time.Duration(i) * time.Hour)
		if _, err := f.log.Append(Event{Actor: "alice", Action: "a"}); err != nil {
			t.Fatal(err)
		}
		if i+1 == 25 || i+1 == 49 {
			cp, err := f.log.Checkpoint()
			if err != nil {
				t.Fatal(err)
			}
			if i+1 == 49 {
				at49 = cp
			}
		}
	}
	if at49.Seq != 49 {
		t.Fatalf("checkpoint at 49: %+v", at49)
	}
	// now is base+71h; retention 47h cuts at base+24h, so seq 26 (at +25h) stays and 1–25 go.
	n, err := f.log.Prune(47 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 25 {
		t.Fatalf("pruned %d, want 25 (records 1–25)", n)
	}
	if res, err := f.log.Verify(); err != nil || res.FirstSeq != 26 {
		t.Fatalf("verify after prune: %v %+v", err, res)
	}
	res, err := f.log.VerifySince(49)
	if err != nil {
		t.Fatalf("verify from seq 49 after prune: %v %+v", err, res)
	}
	if res.FirstSeq != 50 || res.LastSeq != 72 {
		t.Fatalf("walked %+v, want seq 50–72", res)
	}
}

// TestVerifyStaysWholeAfterRaisingRetention: a prune under a short retention stays a
// valid gap after the operator raises retention. Verify used to compare prune marks
// against the current retention and report a break.
func TestVerifyStaysWholeAfterRaisingRetention(t *testing.T) {
	f := open(t, Options{})
	base := f.now
	for i := range 12 {
		f.now = base.Add(time.Duration(i) * time.Hour)
		if _, err := f.log.Append(Event{Actor: "alice", Action: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := f.log.Prune(8 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("prune deleted nothing")
	}
	if _, err := f.log.Verify(); err != nil {
		t.Fatalf("verify after prune: %v", err)
	}
	f.log.retention = 365 * 24 * time.Hour
	if res, err := f.log.Verify(); err != nil {
		t.Fatalf("verify after raising retention: %v %+v", err, res)
	}
}

func TestReadOnlyLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.db")
	db, err := kv.OpenSQLite(kv.SQLiteOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	wlog, err := Open(db.Handle(), db.WriteHandle(), "audit_events", Options{})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := wlog.Append(Event{Actor: "alice", Action: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil { // cleanly: the WAL is folded into the file
		t.Fatal(err)
	}
	ro, err := kv.OpenSQLite(kv.SQLiteOptions{Path: path, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ro.Close() })
	rlog, err := Open(ro.Handle(), nil, "audit_events", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, total, err := rlog.Query(Filter{}); err != nil || total != 3 {
		t.Fatalf("query read-only: %v %d", err, total)
	}
	if res, err := rlog.Verify(); err != nil || res.Events != 3 {
		t.Fatalf("verify read-only: %v %+v", err, res)
	}
	if _, err := rlog.Append(Event{Actor: "alice", Action: "a"}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("append read-only: %v", err)
	}
	if _, err := rlog.Prune(time.Hour); !errors.Is(err, ErrReadOnly) {
		t.Errorf("prune read-only: %v", err)
	}
	if _, err := rlog.Checkpoint(); !errors.Is(err, ErrReadOnly) {
		t.Errorf("checkpoint read-only: %v", err)
	}
}

func TestAppendTxCommitsWithTheCaller(t *testing.T) {
	f := open(t, Options{})
	err := withTx(f.db.WriteHandle(), func(tx *sql.Tx) error {
		e := Event{Actor: "alice", Action: "a"}
		if err := f.log.AppendTx(tx, &e); err != nil {
			return err
		}
		if e.Seq != 1 {
			t.Fatalf("seq: %d", e.Seq)
		}
		return errors.New("roll it back")
	})
	if err == nil {
		t.Fatal("the caller's error should come back")
	}
	if _, total, _ := f.log.Query(Filter{}); total != 0 {
		t.Fatalf("a rolled-back append left %d records", total)
	}
	// and commits with the caller
	if err := withTx(f.db.WriteHandle(), func(tx *sql.Tx) error {
		e := Event{Actor: "alice", Action: "a"}
		return f.log.AppendTx(tx, &e)
	}); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := f.log.Query(Filter{}); total != 1 {
		t.Fatalf("append in a caller's transaction: %d", total)
	}
}

func TestProvidedSignKey(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef") // 32 bytes, for the tests only
	f := open(t, Options{SignKey: key, SignerID: "root"})
	if _, err := f.log.Append(Event{Actor: "alice", Action: "a"}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(25 * time.Hour)
	if _, err := f.log.Append(Event{Actor: "alice", Action: "a"}); err != nil {
		t.Fatal(err)
	}
	cps, err := f.log.Checkpoints()
	if err != nil || len(cps) != 2 { // one at the first record, one when the 24 hours passed
		t.Fatalf("checkpoints: %v %+v", err, cps)
	}
	if cps[0].Signer != "root" {
		t.Fatalf("signer: %s", cps[0].Signer)
	}
	// the key is not in the database when the caller provides it
	var n int
	_ = f.db.Handle().QueryRow(`SELECT COUNT(*) FROM event_keys`).Scan(&n)
	if n != 0 {
		t.Fatalf("event_keys has %d rows", n)
	}
	if _, err := f.log.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsBadNames(t *testing.T) {
	f := open(t, Options{})
	for _, name := range []string{"Audit", "audit-events", "drop table x", ""} {
		if _, err := Open(f.db.Handle(), f.db.WriteHandle(), name, Options{}); err == nil {
			t.Errorf("name %q should be refused", name)
		}
	}
}

// TestPruneMultipleCheckpoints is the default schedule: hourly records, a checkpoint every
// 24 hours. Prune of 24 hours used to underflow the checkpoint count because the newest
// checkpoint sat after the cut.
func TestPruneMultipleCheckpoints(t *testing.T) {
	f := open(t, Options{CheckpointInterval: 24 * time.Hour})
	base := f.now
	for i := range 72 {
		f.now = base.Add(time.Duration(i) * time.Hour)
		if _, err := f.log.Append(Event{Actor: "alice", Action: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	cps, err := f.log.Checkpoints()
	if err != nil {
		t.Fatal(err)
	}
	if len(cps) < 3 || cps[0].Seq != 1 || cps[1].Seq != 25 || cps[2].Seq != 49 {
		t.Fatalf("checkpoints: %+v", cps)
	}
	n, err := f.log.Prune(24 * time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n == 0 {
		t.Fatal("prune deleted nothing")
	}
	if res, err := f.log.Verify(); err != nil {
		t.Fatalf("verify after prune: %v %+v", err, res)
	}
}

// TestPruneBackdatedTime: a record stamped in the past must not take newer records with it.
func TestPruneBackdatedTime(t *testing.T) {
	f := open(t, Options{})
	base := f.now
	for range 5 {
		if _, err := f.log.Append(Event{Actor: "alice", Action: "a"}); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Hour)
	}
	if _, err := f.log.Append(Event{Actor: "alice", Action: "a", Time: base.Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	n, err := f.log.Prune(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("pruned %d, want 0: a backdated tail must not drop the newer prefix", n)
	}
}

// TestVerifyRejectsGapWithoutPrune: deleting records that a scheduled checkpoint covered, without
// a prune mark, is a break. Verify used to treat the checkpoint as an anchor and say OK.
func TestVerifyRejectsGapWithoutPrune(t *testing.T) {
	f := open(t, Options{})
	for i := range 10 {
		if _, err := f.log.Append(Event{Actor: "alice", Action: "a"}); err != nil {
			t.Fatal(err)
		}
		if (i+1)%2 == 1 {
			if _, err := f.log.Checkpoint(); err != nil {
				t.Fatal(err)
			}
		}
	}
	tamper(t, f.db, `DELETE FROM audit_events WHERE seq <= 9`)
	if _, err := f.log.Verify(); !errors.Is(err, ErrBrokenChain) {
		t.Fatalf("a gap with only scheduled checkpoints: %v", err)
	}
}

// TestUpdateBothRollsBackBothSides: a kv put and an audit append in one UpdateBoth must both
// vanish when the callback fails.
func TestUpdateBothRollsBackBothSides(t *testing.T) {
	f := open(t, Options{})
	err := f.db.UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		if err := tx.Put([]byte("meta"), []byte("token"), []byte("secret")); err != nil {
			return err
		}
		e := Event{Actor: "alice", Action: "secrets_item_use", Object: "item-1"}
		if err := f.log.AppendTx(raw, &e); err != nil {
			return err
		}
		return errors.New("caller failed after both writes")
	})
	if err == nil {
		t.Fatal("UpdateBoth should return the callback's error")
	}
	if err := f.db.View(func(tx kv.Tx) error {
		if v := tx.Get([]byte("meta"), []byte("token")); v != nil {
			t.Errorf("kv write survived the rollback: %q", v)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, total, err := f.log.Query(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(got) != 0 {
		t.Fatalf("event write survived the rollback: %d %+v", total, got)
	}
}

// TestConcurrentAppendFromTwoConnections: two writers on one PostgreSQL log serialise on the
// advisory lock (and retry a unique violation), and the chain still verifies.
func TestConcurrentAppendFromTwoConnections(t *testing.T) {
	dsn := os.Getenv("BEACON_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BEACON_TEST_POSTGRES_DSN names the PostgreSQL server two writers share")
	}
	shared := testDatabase(t, dsn)
	openPG := func() kv.SQL {
		t.Helper()
		pg, err := kv.OpenPostgres(kv.PostgresOptions{DSN: shared})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pg.Close() })
		return pg
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	o := Options{Dialect: PostgreSQL, SignKey: key, SignerID: "test", Now: time.Now, CheckpointInterval: time.Hour}
	oneDB, twoDB := openPG(), openPG()
	one, err := Open(oneDB.Handle(), oneDB.WriteHandle(), "audit_events", o)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Open(twoDB.Handle(), twoDB.WriteHandle(), "audit_events", o)
	if err != nil {
		t.Fatal(err)
	}
	const n = 20
	var wg sync.WaitGroup
	errc := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_, err := one.Append(Event{Actor: "a", Action: fmt.Sprintf("one-%d", i)})
			errc <- err
		}(i)
		go func(i int) {
			defer wg.Done()
			_, err := two.Append(Event{Actor: "b", Action: fmt.Sprintf("two-%d", i)})
			errc <- err
		}(i)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	res, err := one.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != 2*n {
		t.Fatalf("events %d, want %d", res.Events, 2*n)
	}
}

// TestConcurrentAppendVerifyPrune runs append, verify and prune together. Verify must not
// report a break; the chain that remains must still hold.
func TestConcurrentAppendVerifyPrune(t *testing.T) {
	f := open(t, Options{Now: time.Now, CheckpointInterval: 40 * time.Millisecond})
	old := time.Now().Add(-48 * time.Hour)
	for i := range 20 {
		if _, err := f.log.Append(Event{Actor: "alice", Action: "old", Time: old.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	const writers = 3
	const per = 25
	var writersWg, helpersWg sync.WaitGroup
	errCh := make(chan error, 8)
	writersWg.Add(writers)
	for w := range writers {
		go func(id int) {
			defer writersWg.Done()
			for i := range per {
				if _, err := f.log.Append(Event{Actor: "alice", Action: "new", Object: fmt.Sprintf("%d-%d", id, i)}); err != nil {
					errCh <- fmt.Errorf("append: %w", err)
					return
				}
			}
		}(w)
	}
	done := make(chan struct{})
	helpersWg.Add(2)
	go func() {
		defer helpersWg.Done()
		for {
			select {
			case <-done:
				return
			default:
				if _, err := f.log.Verify(); err != nil {
					errCh <- fmt.Errorf("verify: %w", err)
					return
				}
			}
		}
	}()
	go func() {
		defer helpersWg.Done()
		for {
			select {
			case <-done:
				return
			default:
				if _, err := f.log.Prune(24 * time.Hour); err != nil {
					errCh <- fmt.Errorf("prune: %w", err)
					return
				}
			}
		}
	}()
	writersWg.Wait()
	close(done)
	helpersWg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if _, err := f.log.Verify(); err != nil {
		t.Fatalf("final verify: %v", err)
	}
}
