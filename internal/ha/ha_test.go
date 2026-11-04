package ha

// The lease over a live PostgreSQL, when BEACON_TEST_POSTGRES_DSN names one (CI's service
// container): two leases over one lease row, the second waiting; the first stopping and the second
// taking over within the expiry; and Single running what only the leader runs, stopping it on
// leadership loss and starting it again.

import (
	"context"
	"database/sql"
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
		took = two.Leader()
		tookWhen = time.Now()
		time.Sleep(20 * time.Millisecond)
	}
	if !took {
		t.Fatal("the second instance never took over")
	}
	// the clean release makes the takeover quick: far inside the 300ms expiry
	if d := tookWhen.Sub(start); d > o.Expire {
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
	wait(t, running, "fn to start")
	l.inner.Store(false) // and goes
	wait(t, stopped, "fn to stop")
	l.inner.Store(true) // and comes back
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
