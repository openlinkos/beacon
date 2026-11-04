// Package ha is the primary/standby pair over the PostgreSQL database (the plan's §10.3): one
// lease row in a leader table decides which instance is active. The primary renews the lease every
// Renew (5s by default); the lease lasts Expire (15s by default) without a renewal; both values are
// the ha: section of the configuration. A standby that cannot take the lease answers /healthz with
// 503, so the load balancer or the VIP in front sends traffic to the primary alone; the switch
// takes about the lease's expiry. Everything the pair shares is already in the database — the
// routing and alert state — or in S3-compatible storage — the session recordings; the Feishu long
// connection, the alert routing and the SSH channel are held by the same lease, one active
// instance at a time.
//
// Both clocks are the database's: the lease expires by the server's now(), not by what either
// instance's clock says, so a skewed standby cannot take a lease the primary still holds.
package ha

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Options is how a lease is held. Renew and Expire default to 5s and 15s.
type Options struct {
	// Name is the lease's name in the leader table; every instance of one deployment names the
	// same. Default "beacon".
	Name string
	// ID names this instance in the table (a log reader tells the pair apart). Default
	// hostname:pid.
	ID string
	// Renew is how often the primary renews. Default 5s.
	Renew time.Duration
	// Expire is how long the lease lasts without a renewal. Default 15s; the switch to the
	// standby takes about this long.
	Expire time.Duration
	// Log is where the transitions are said; nil keeps quiet.
	Log *slog.Logger
}

// Lease is one instance's hold on the leader lease: Run renews it (or waits for it) until the
// context ends, and Leader reports whether this instance holds it now.
type Lease struct {
	db    *sql.DB
	o     Options
	inner atomic.Bool // whether the server last said this instance holds the lease
}

// Open takes the pool the lease lives over (PostgreSQL, a pool of its own so a renewal never waits
// behind a write transaction) and prepares the lease's table.
func Open(db *sql.DB, o Options) (*Lease, error) {
	o, err := o.normalized()
	if err != nil {
		return nil, err
	}
	l := &Lease{db: db, o: o}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS leader (
		name TEXT PRIMARY KEY,
		holder TEXT NOT NULL,
		expires TIMESTAMPTZ NOT NULL,
		renewed TIMESTAMPTZ NOT NULL
	)`); err != nil {
		return nil, err
	}
	return l, nil
}

// normalized fills the defaults and checks the one rule: the expiry must outlast the renewal, or
// the lease would expire between renewals.
func (o Options) normalized() (Options, error) {
	if o.Name == "" {
		o.Name = "beacon"
	}
	if o.ID == "" {
		host, _ := os.Hostname()
		o.ID = fmt.Sprintf("%s:%d", host, os.Getpid())
	}
	if o.Renew <= 0 {
		o.Renew = 5 * time.Second
	}
	if o.Expire <= 0 {
		o.Expire = 15 * time.Second
	}
	if o.Expire <= o.Renew {
		return o, fmt.Errorf("the lease's expiry %s must outlast its renewal %s", o.Expire, o.Renew)
	}
	return o, nil
}

// take is the one statement the lease is: this instance becomes the holder when the lease is free
// or already its own, and the server says which by answering a row. now() is the database's clock,
// the only one the pair shares.
func (l *Lease) take(ctx context.Context) (bool, error) {
	var expires time.Time
	err := l.db.QueryRowContext(ctx, `INSERT INTO leader (name, holder, expires, renewed)
		VALUES ($1, $2, now() + make_interval(secs => $3), now())
		ON CONFLICT (name) DO UPDATE SET holder = $2, expires = now() + make_interval(secs => $3), renewed = now()
		WHERE leader.expires <= now() OR leader.holder = $2
		RETURNING expires`, l.o.Name, l.o.ID, l.o.Expire.Seconds()).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // another instance holds the lease, unexpired
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// release hands the lease over, if this instance still holds it, so a clean stop switches at once
// instead of after the expiry.
func (l *Lease) release(ctx context.Context) error {
	_, err := l.db.ExecContext(ctx, `DELETE FROM leader WHERE name = $1 AND holder = $2`, l.o.Name, l.o.ID)
	return err
}

// Run holds the lease until ctx ends: it takes it when it can, renews it while it holds it, and
// says every transition. It returns when ctx is done, releasing the lease on the way out when it
// held one.
func (l *Lease) Run(ctx context.Context) {
	wasLeader := false
	defer func() {
		if wasLeader {
			rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = l.release(rctx)
			cancel()
		}
	}()
	for {
		leader, err := l.take(ctx)
		if err != nil && ctx.Err() == nil && l.o.Log != nil {
			l.o.Log.Warn("the leader lease could not be renewed", "err", err)
		}
		if leader != wasLeader {
			if l.o.Log != nil {
				if leader {
					l.o.Log.Info("this instance became the leader", "lease", l.o.Name, "id", l.o.ID)
				} else {
					l.o.Log.Warn("this instance gave up the leader lease; a standby answers /healthz with 503", "lease", l.o.Name, "id", l.o.ID)
				}
			}
			wasLeader = leader
		}
		l.inner.Store(leader)
		select {
		case <-ctx.Done():
			l.inner.Store(false)
			return
		case <-time.After(l.o.Renew):
		}
	}
}

// Leader reports whether this instance held the lease at the last attempt: /healthz answers 503
// when it did not, and the front sends its traffic elsewhere.
func (l *Lease) Leader() bool { return l.inner.Load() }

// ID names this instance, as Run logs it.
func (l *Lease) ID() string { return l.o.ID }

// Single runs what only the leader may do: fn runs in its own context while this instance holds
// the lease, is cancelled the moment it does not, and starts again — fresh, as after a restart —
// when the lease comes back. What survives a switch is what the database holds; fn should read its
// state from there on every start, as the service does after a restart. The lease's Run must be
// running for the changes to arrive.
func Single(ctx context.Context, l *Lease, fn func(context.Context)) {
	go func() {
		for {
			for !l.Leader() { // wait for the lease
				select {
				case <-ctx.Done():
					return
				case <-time.After(l.o.Renew):
				}
			}
			// the lease is held: run fn until it is not, or ctx ends
			ictx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { fn(ictx); close(done) }()
			for l.Leader() {
				select {
				case <-ctx.Done():
					cancel()
					<-done
					return
				case <-time.After(l.o.Renew):
				}
			}
			cancel() // leadership lost: stop what only the leader does
			<-done
			// and wait for the lease again
		}
	}()
}

// OpenPool opens the lease's own small pool over the DSN: one connection, for a renewal is one
// statement that must never wait behind the store's writes.
func OpenPool(dsn string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("postgres: the DSN is not valid")
	}
	cfg.RuntimeParams["application_name"] = "beacon-lease"
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Database, err)
	}
	return db, nil
}
