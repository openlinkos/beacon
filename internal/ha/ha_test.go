package ha

// The lease over a live PostgreSQL, when BEACON_TEST_POSTGRES_DSN names one (CI's service
// container): two leases over one lease row, the second waiting; the first stopping and the second
// taking over within the expiry; and Single running what only the leader runs, stopping it on
// leadership loss and starting it again.

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func testPool(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("BEACON_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BEACON_TEST_POSTGRES_DSN names the PostgreSQL server the lease tests run on")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("the test DSN is not valid")
	}
	return stdlib.OpenDB(*cfg)
}

// TestLeaseTakeover: the pair's whole story in the small.
func TestLeaseTakeover(t *testing.T) {
	if os.Getenv("BEACON_TEST_POSTGRES_DSN") == "" {
		t.Skip("BEACON_TEST_POSTGRES_DSN names the PostgreSQL server the lease tests run on")
	}
	admin := testPool(t)
	defer func() { _ = admin.Close() }()
	// a fresh lease row each run, so the test never sees a leftover holder
	if _, err := admin.Exec(`DROP TABLE IF EXISTS leader`); err != nil {
		t.Fatal(err)
	}
	o := Options{Name: "test", Renew: 60 * time.Millisecond, Expire: 300 * time.Millisecond}
	one, err := Open(admin, Options{Name: o.Name, ID: "primary", Renew: o.Renew, Expire: o.Expire})
	if err != nil {
		t.Fatal(err)
	}
	// the pair's two instances: the same lease row, ids of their own (the default id is
	// hostname:pid, which in one process would make them the same holder)
	two, err := Open(admin, Options{Name: o.Name, ID: "standby", Renew: o.Renew, Expire: o.Expire})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// the first takes the lease; the second, a moment later, does not
	oneCtx, stopOne := context.WithCancel(context.Background())
	defer stopOne()
	go one.Run(oneCtx)
	deadline := time.Now().Add(3 * time.Second)
	for !one.Leader() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !one.Leader() {
		t.Fatal("the first instance never took the lease")
	}
	go two.Run(ctx)
	time.Sleep(150 * time.Millisecond) // two renewals of the second, all refused
	if two.Leader() {
		t.Fatal("the second instance took a lease the first holds")
	}

	// the first stops; the second takes over within the expiry, and answers leader ever after
	start := time.Now()
	stopOne() // Run releases the lease on its way out
	took, tookWhen := false, time.Time{}
	for !took && time.Now().Before(start.Add(3*time.Second)) {
		if one.Leader() && two.Leader() {
			t.Fatal("both instances reported leader at once")
		}
		took = two.Leader()
		tookWhen = time.Now()
		time.Sleep(5 * time.Millisecond)
	}
	if !took {
		t.Fatal("the second instance never took over")
	}
	// the clean release makes the takeover quick: far inside the 300ms expiry
	d := tookWhen.Sub(start)
	t.Logf("graceful takeover %s", d)
	if d > o.Expire {
		t.Errorf("the takeover took %s, longer than the %s expiry", d, o.Expire)
	}
	stopOne()
	cancel()
}

// TestSingle: what only the leader may do runs while the lease is held, stops when it is not, and
// runs again when it comes back.
func TestSingle(t *testing.T) {
	// Single's own machinery needs no database: a lease whose leadership it can watch
	o := Options{Name: "test", Renew: 20 * time.Millisecond, Expire: 100 * time.Millisecond}
	l := &Lease{o: o}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	runs, stops := 0, 0
	running := make(chan struct{}, 8)
	stopped := make(chan struct{}, 8)
	Single(ctx, l, func(inner context.Context) {
		mu.Lock()
		runs++
		mu.Unlock()
		running <- struct{}{}
		<-inner.Done() // run until Single cancels: the leader's work
		mu.Lock()
		stops++
		mu.Unlock()
		stopped <- struct{}{}
	})

	l.inner.Store(true) // the lease arrives
	l.heldUntil.Store(time.Now().Add(time.Minute).UnixNano())
	wait(t, running, "fn to start")
	l.inner.Store(false) // and goes
	l.heldUntil.Store(0)
	wait(t, stopped, "fn to stop")
	l.inner.Store(true) // and comes back
	l.heldUntil.Store(time.Now().Add(time.Minute).UnixNano())
	wait(t, running, "fn to start again")
	cancel()
	wait(t, stopped, "fn to stop on the way out")
	mu.Lock()
	defer mu.Unlock()
	if runs != 2 || stops != 2 {
		t.Fatalf("runs %d stops %d; the lease changed twice", runs, stops)
	}
}

func wait(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestOptions: the defaults, and the expiry that must outlast the renewal.
func TestOptions(t *testing.T) {
	if _, err := Open(nil, Options{Renew: time.Second, Expire: time.Second}); err == nil {
		t.Error("an expiry equal to the renewal should fail")
	}
	o, err := Options{}.normalized()
	if err != nil {
		t.Fatal(err)
	}
	if o.Name != "beacon" || o.Renew != 5*time.Second || o.Expire != 15*time.Second {
		t.Errorf("defaults: %+v", o)
	}
	if o.ID == "" {
		t.Error("the instance needs an id")
	}
}

// TestDefaultIDsDiffer: two instances in one process (same hostname and pid) must not share an id.
func TestDefaultIDsDiffer(t *testing.T) {
	a, err := Options{}.normalized()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Options{}.normalized()
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatalf("two instances got the same id %q", a.ID)
	}
}

// TestLeaderExpiresLocally: a hung take cannot keep Leader true past start+expiry-margin.
func TestLeaderExpiresLocally(t *testing.T) {
	if os.Getenv("BEACON_TEST_POSTGRES_DSN") == "" {
		t.Skip("BEACON_TEST_POSTGRES_DSN names the PostgreSQL server the lease tests run on")
	}
	admin := testPool(t)
	defer func() { _ = admin.Close() }()
	if _, err := admin.Exec(`DROP TABLE IF EXISTS leader`); err != nil {
		t.Fatal(err)
	}
	o := Options{Name: "expire", ID: "primary", Renew: 80 * time.Millisecond, Expire: 400 * time.Millisecond}
	one, err := Open(admin, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go one.Run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for !one.Leader() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !one.Leader() {
		t.Fatal("the instance never took the lease")
	}
	// hold the row so the next take hangs (a network partition's stand-in)
	tx, err := admin.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`SELECT * FROM leader WHERE name = $1 FOR UPDATE`, o.Name); err != nil {
		t.Fatal(err)
	}
	time.Sleep(350 * time.Millisecond) // past start+expiry-margin, still inside Expire of the last take
	if one.Leader() {
		t.Fatal("Leader stayed true while take was blocked past the local deadline")
	}
	if !one.Healthy() {
		t.Fatal("a waiting instance that last heard from the database should still be healthy")
	}
}

// TestWriteChecksTheLease: a write after another instance takes over is refused.
func TestWriteChecksTheLease(t *testing.T) {
	if os.Getenv("BEACON_TEST_POSTGRES_DSN") == "" {
		t.Skip("BEACON_TEST_POSTGRES_DSN names the PostgreSQL server the lease tests run on")
	}
	admin := testPool(t)
	defer func() { _ = admin.Close() }()
	if _, err := admin.Exec(`DROP TABLE IF EXISTS leader`); err != nil {
		t.Fatal(err)
	}
	o := Options{Name: "guard", Renew: 80 * time.Millisecond, Expire: 400 * time.Millisecond}
	one, err := Open(admin, Options{Name: o.Name, ID: "primary", Renew: o.Renew, Expire: o.Expire})
	if err != nil {
		t.Fatal(err)
	}
	two, err := Open(admin, Options{Name: o.Name, ID: "standby", Renew: o.Renew, Expire: o.Expire})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oneCtx, stopOne := context.WithCancel(context.Background())
	defer stopOne()
	go one.Run(oneCtx)
	deadline := time.Now().Add(3 * time.Second)
	for !one.Leader() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !one.Leader() {
		t.Fatal("the first instance never took the lease")
	}
	tx, err := admin.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := one.CheckWrite(context.Background(), tx); err != nil {
		t.Fatalf("the holder should write: %v", err)
	}
	_ = tx.Rollback()

	stopOne()
	go two.Run(ctx)
	deadline = time.Now().Add(3 * time.Second)
	for !two.Leader() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !two.Leader() {
		t.Fatal("the second instance never took over")
	}
	tx, err = admin.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := one.CheckWrite(context.Background(), tx); !errors.Is(err, ErrNotLeader) {
		t.Fatalf("the old holder still wrote: %v", err)
	}
}

// TestIdleWriteDoesNotBlockTakeover: a write transaction that holds FOR SHARE on the lease
// row (a paused or partitioned primary) is killed at the lease's expiry, so a standby can take
// over instead of waiting on the lock until TCP keepalive gives up.
func TestIdleWriteDoesNotBlockTakeover(t *testing.T) {
	if os.Getenv("BEACON_TEST_POSTGRES_DSN") == "" {
		t.Skip("BEACON_TEST_POSTGRES_DSN names the PostgreSQL server the lease tests run on")
	}
	admin := testPool(t)
	defer func() { _ = admin.Close() }()
	if _, err := admin.Exec(`DROP TABLE IF EXISTS leader`); err != nil {
		t.Fatal(err)
	}
	expire := time.Second
	one, err := Open(admin, Options{Name: "idle", ID: "primary", Renew: 200 * time.Millisecond, Expire: expire})
	if err != nil {
		t.Fatal(err)
	}
	two, err := Open(admin, Options{Name: "idle", ID: "standby", Renew: 200 * time.Millisecond, Expire: expire})
	if err != nil {
		t.Fatal(err)
	}
	oneCtx, stopOne := context.WithCancel(context.Background())
	defer stopOne()
	go one.Run(oneCtx)
	deadline := time.Now().Add(3 * time.Second)
	for !one.Leader() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !one.Leader() {
		t.Fatal("the first instance never took the lease")
	}
	tx, err := admin.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := one.CheckWrite(context.Background(), tx); err != nil {
		t.Fatalf("the holder should write: %v", err)
	}
	stopOne()
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	took := false
	for !took && ctx.Err() == nil {
		held, _, err := two.take(ctx)
		if err != nil && ctx.Err() == nil {
			t.Logf("take: %v", err)
		}
		if held {
			took = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	elapsed := time.Since(started)
	if !took {
		t.Fatalf("the standby never took the lease while a write held FOR SHARE; waited %s", elapsed)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("takeover took %s, want around the lease expiry (%s)", elapsed, expire)
	}
	t.Logf("takeover with an idle write holding FOR SHARE: %s", elapsed)
}

// TestRunReleasesAfterLeaderWork: the lease stays until Single's fn returns.
func TestRunReleasesAfterLeaderWork(t *testing.T) {
	if os.Getenv("BEACON_TEST_POSTGRES_DSN") == "" {
		t.Skip("BEACON_TEST_POSTGRES_DSN names the PostgreSQL server the lease tests run on")
	}
	admin := testPool(t)
	defer func() { _ = admin.Close() }()
	if _, err := admin.Exec(`DROP TABLE IF EXISTS leader`); err != nil {
		t.Fatal(err)
	}
	one, err := Open(admin, Options{Name: "wait", ID: "primary", Renew: 80 * time.Millisecond, Expire: 400 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go one.Run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for !one.Leader() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !one.Leader() {
		t.Fatal("the instance never took the lease")
	}
	started := make(chan struct{})
	left := make(chan time.Time, 1)
	Single(ctx, one, func(inner context.Context) {
		close(started)
		<-inner.Done()
		time.Sleep(200 * time.Millisecond)
		left <- time.Now()
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("Single never started")
	}
	cancel()
	var released time.Time
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := admin.QueryRow(`SELECT COUNT(*) FROM leader WHERE name = $1 AND holder = $2`, "wait", "primary").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			released = time.Now()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if released.IsZero() {
		t.Fatal("the lease was never released")
	}
	doneAt := <-left
	if released.Before(doneAt.Add(-30 * time.Millisecond)) {
		t.Errorf("released the lease at %s before the leader's work returned at %s", released.Format(time.RFC3339Nano), doneAt.Format(time.RFC3339Nano))
	}
}

// TestRunReleasesWhenStopHitsATake: cancelling during a renewal still deletes the row.
// A take that sees ctx.Done() returns leader=false; that must not skip release, or the
// standby waits the full expiry for a holder that has already stopped.
func TestRunReleasesWhenStopHitsATake(t *testing.T) {
	if os.Getenv("BEACON_TEST_POSTGRES_DSN") == "" {
		t.Skip("BEACON_TEST_POSTGRES_DSN names the PostgreSQL server the lease tests run on")
	}
	admin := testPool(t)
	defer func() { _ = admin.Close() }()
	if _, err := admin.Exec(`DROP TABLE IF EXISTS leader`); err != nil {
		t.Fatal(err)
	}
	one, err := Open(admin, Options{Name: "stop-take", ID: "primary", Renew: 80 * time.Millisecond, Expire: 400 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go one.Run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for !one.Leader() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !one.Leader() {
		t.Fatal("the instance never took the lease")
	}
	time.Sleep(40 * time.Millisecond) // land inside a take or the wait for the next one
	cancel()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := admin.QueryRow(`SELECT COUNT(*) FROM leader WHERE name = $1 AND holder = $2`, "stop-take", "primary").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the lease was never released")
}

// TestStandbyOpensWhilePrimaryHoldsAWrite: a standby starting while the primary holds FOR SHARE
// on the leader row must not wait on ACCESS EXCLUSIVE (CREATE TABLE IF NOT EXISTS / ALTER ADD
// COLUMN). OpenPool's statement_timeout is 2s; Open must return well under that.
func TestStandbyOpensWhilePrimaryHoldsAWrite(t *testing.T) {
	dsn := os.Getenv("BEACON_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BEACON_TEST_POSTGRES_DSN names the PostgreSQL server the lease tests run on")
	}
	expire := time.Second
	primaryPool, err := OpenPool(dsn, expire)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = primaryPool.Close() }()
	if _, err := primaryPool.Exec(`DROP TABLE IF EXISTS leader`); err != nil {
		t.Fatal(err)
	}
	one, err := Open(primaryPool, Options{Name: "open-while-write", ID: "primary", Renew: 200 * time.Millisecond, Expire: expire})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go one.Run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for !one.Leader() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !one.Leader() {
		t.Fatal("the first instance never took the lease")
	}
	admin := testPool(t)
	defer func() { _ = admin.Close() }()
	tx, err := admin.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := one.CheckWrite(context.Background(), tx); err != nil {
		t.Fatalf("the holder should write: %v", err)
	}
	standbyPool, err := OpenPool(dsn, expire)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = standbyPool.Close() }()
	started := time.Now()
	if _, err := Open(standbyPool, Options{Name: "open-while-write", ID: "standby", Renew: 200 * time.Millisecond, Expire: expire}); err != nil {
		t.Fatalf("standby Open while the primary held a write: %v (elapsed %s)", err, time.Since(started))
	}
	elapsed := time.Since(started)
	if elapsed > time.Second {
		t.Fatalf("standby Open took %s; want well under the lease pool's 2s statement_timeout", elapsed)
	}
}

// TestOpenAddsEpochToAnOldTable: a leader table created before epoch existed still gets the column.
func TestOpenAddsEpochToAnOldTable(t *testing.T) {
	admin := testPool(t)
	defer func() { _ = admin.Close() }()
	if _, err := admin.Exec(`DROP TABLE IF EXISTS leader`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE TABLE leader (
		name TEXT PRIMARY KEY,
		holder TEXT NOT NULL,
		expires TIMESTAMPTZ NOT NULL,
		renewed TIMESTAMPTZ NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(admin, Options{Name: "old-table", ID: "primary", Renew: 200 * time.Millisecond, Expire: time.Second}); err != nil {
		t.Fatal(err)
	}
	var has bool
	if err := admin.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'leader' AND column_name = 'epoch'
	)`).Scan(&has); err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("Open did not add the epoch column to an old leader table")
	}
}
