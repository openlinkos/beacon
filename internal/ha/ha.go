// Package ha is the primary/standby pair over the PostgreSQL database (the plan's §10.3): one
// lease row in a leader table decides which instance is active. The primary renews the lease every
// Renew (5s by default); the lease lasts Expire (15s by default) without a renewal; both values are
// the ha: section of the configuration. A standby that cannot take the lease answers /healthz with
// 503, so the load balancer or the VIP in front sends traffic to the primary alone; /livez stays
// 200 while the instance can still reach the database, so Docker's health check does not restart a
// healthy standby. The switch takes about the lease's expiry. Everything the pair shares is already
// in the database — the routing and alert state — or in S3-compatible storage — the session
// recordings; the Feishu long connection, the alert routing and the SSH channel are held by the
// same lease, one active instance at a time.
//
// Both clocks are the database's: the lease expires by the server's now(), not by what either
// instance's clock says, so a skewed standby cannot take a lease the primary still holds. A write
// transaction checks the lease row (FOR SHARE) against this instance's id and epoch, so a
// partitioned primary cannot keep writing after a standby has taken over. Leader() itself becomes
// false once the last successful take is older than Expire minus a margin, even if the next take
// is still hung on the network.
package ha

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/openlinkos/beacon/internal/kv"
)

// ErrNotLeader means this instance does not hold the lease: a write must not go through.
var ErrNotLeader = errors.New("this instance does not hold the leader lease")

// Options is how a lease is held. Renew and Expire default to 5s and 15s.
type Options struct {
	// Name is the lease's name in the leader table; every instance of one deployment names the
	// same. Default "beacon".
	Name string
	// ID names this instance in the table (a log reader tells the pair apart). Default
	// hostname:pid plus a random suffix, so two containers that share a hostname and run as
	// PID 1 do not keep renewing each other's lease. ha.id in the configuration sets it.
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
	inner atomic.Bool  // whether the server last said this instance holds the lease
	epoch atomic.Int64 // the epoch of the last successful take; writes check it
	// heldUntil is when Leader must become false even if take has not returned: the start of
	// the last successful take plus Expire minus a margin.
	heldUntil atomic.Int64
	// healthyUntil is when Healthy becomes false: the last take that heard from the database
	// (held or not), plus Expire. A partitioned instance fails /livez; a waiting standby does not.
	healthyUntil atomic.Int64
	tasksMu      sync.Mutex
	tasks        sync.WaitGroup // leader-only work Single is running; Run waits before release
	stopping     atomic.Bool    // set before Wait, so Single does not Add after shutdown
}

// Open takes the pool the lease lives over (PostgreSQL, a pool of its own so a renewal never waits
// behind a write transaction) and prepares the lease's table. CREATE and ALTER run only when
// information_schema says the table or the epoch column is missing, under the schema advisory lock
// with a short lock_timeout, so a standby starting while the primary holds FOR SHARE is not blocked.
func Open(db *sql.DB, o Options) (*Lease, error) {
	o, err := o.normalized()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := ensureLeaderTable(ctx, db); err != nil {
		return nil, err
	}
	return &Lease{db: db, o: o}, nil
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func leaderTableState(ctx context.Context, q queryRower) (exists, hasEpoch bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = 'leader'
	)`).Scan(&exists)
	if err != nil || !exists {
		return exists, false, err
	}
	err = q.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'leader' AND column_name = 'epoch'
	)`).Scan(&hasEpoch)
	return exists, hasEpoch, err
}

func leaderTableReady(db *sql.DB) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exists, hasEpoch, err := leaderTableState(ctx, db)
	return err == nil && exists && hasEpoch
}

func pgCode(err error) string {
	var e *pgconn.PgError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func lockUnavailable(err error) bool {
	switch pgCode(err) {
	case "55P03", "57014": // lock_not_available, query_canceled
		return true
	}
	return false
}

func duplicateColumn(err error) bool { return pgCode(err) == "42701" }

func sleepRetry(ctx context.Context) bool {
	t := time.NewTimer(200 * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func ensureLeaderTable(ctx context.Context, db *sql.DB) error {
	for {
		if err := ctx.Err(); err != nil {
			if leaderTableReady(db) {
				return nil
			}
			return err
		}
		exists, hasEpoch, err := leaderTableState(ctx, db)
		if err != nil {
			if lockUnavailable(err) && sleepRetry(ctx) {
				continue
			}
			if leaderTableReady(db) {
				return nil
			}
			return err
		}
		if exists && hasEpoch {
			return nil
		}
		err = migrateLeaderTable(ctx, db)
		if err == nil || duplicateColumn(err) {
			return nil
		}
		if lockUnavailable(err) && sleepRetry(ctx) {
			continue
		}
		if leaderTableReady(db) {
			return nil
		}
		return err
	}
}

func migrateLeaderTable(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// lock_timeout so a FOR SHARE on the row does not sit until the pool's 2s statement_timeout;
	// statement_timeout 15s overrides that 2s for the ALTER itself.
	if _, err := tx.ExecContext(ctx, `SELECT set_config('lock_timeout', '500ms', true), set_config('statement_timeout', '15s', true)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, kv.SchemaLock); err != nil {
		return err
	}
	exists, hasEpoch, err := leaderTableState(ctx, tx)
	if err != nil {
		return err
	}
	switch {
	case !exists:
		if _, err := tx.ExecContext(ctx, `CREATE TABLE leader (
			name TEXT PRIMARY KEY,
			holder TEXT NOT NULL,
			expires TIMESTAMPTZ NOT NULL,
			renewed TIMESTAMPTZ NOT NULL,
			epoch BIGINT NOT NULL DEFAULT 1
		)`); err != nil {
			return err
		}
	case !hasEpoch:
		if _, err := tx.ExecContext(ctx, `ALTER TABLE leader ADD COLUMN epoch BIGINT NOT NULL DEFAULT 1`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// normalized fills the defaults and checks the one rule: the expiry must outlast the renewal, or
// the lease would expire between renewals.
func (o Options) normalized() (Options, error) {
	if o.Name == "" {
		o.Name = "beacon"
	}
	if o.ID == "" {
		host, _ := os.Hostname()
		raw := make([]byte, 4)
		if _, err := rand.Read(raw); err != nil {
			return o, fmt.Errorf("a random instance id: %w", err)
		}
		o.ID = fmt.Sprintf("%s:%d:%s", host, os.Getpid(), hex.EncodeToString(raw))
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

// margin is how long before the database expiry Leader() becomes false locally, so a hung take or
// a pause cannot overlap a standby that has already taken the row.
func (o Options) margin() time.Duration {
	m := o.Expire / 5
	if m > o.Renew {
		m = o.Renew
	}
	if m < 10*time.Millisecond {
		m = 10 * time.Millisecond
	}
	return m
}

// takeBound is how long one take may wait for the database: short enough that a blackholed
// connection cannot sit until TCP gives up, long enough for a local round-trip.
func (o Options) takeBound() time.Duration {
	d := o.Renew
	if d > 2*time.Second {
		d = 2 * time.Second
	}
	if d < 50*time.Millisecond {
		d = 50 * time.Millisecond
	}
	return d
}

// take is the one statement the lease is: this instance becomes the holder when the lease is free
// or already its own, and the server says which by answering a row. now() is the database's clock,
// the only one the pair shares. Epoch advances only when the holder changes, so a renewal does not
// invalidate the holder's in-flight writes.
func (l *Lease) take(ctx context.Context) (bool, int64, error) {
	tctx, cancel := context.WithTimeout(ctx, l.o.takeBound())
	defer cancel()
	var expires time.Time
	var epoch int64
	err := l.db.QueryRowContext(tctx, `INSERT INTO leader (name, holder, expires, renewed, epoch)
		VALUES ($1, $2, now() + make_interval(secs => $3), now(), 1)
		ON CONFLICT (name) DO UPDATE SET
			holder = $2,
			expires = now() + make_interval(secs => $3),
			renewed = now(),
			epoch = CASE WHEN leader.holder IS NOT DISTINCT FROM $2 THEN leader.epoch ELSE leader.epoch + 1 END
		WHERE leader.expires <= now() OR leader.holder = $2
		RETURNING expires, epoch`, l.o.Name, l.o.ID, l.o.Expire.Seconds()).Scan(&expires, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil // another instance holds the lease, unexpired
	}
	if err != nil {
		return false, 0, err
	}
	return true, epoch, nil
}

// release hands the lease over, if this instance still holds it, so a clean stop switches at once
// instead of after the expiry.
func (l *Lease) release(ctx context.Context) error {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := l.db.ExecContext(rctx, `DELETE FROM leader WHERE name = $1 AND holder = $2`, l.o.Name, l.o.ID)
	return err
}

// Run holds the lease until ctx ends: it takes it when it can, renews it while it holds it, and
// says every transition. It returns when ctx is done, waiting for what only the leader runs to
// return, then releasing the lease so a standby does not overlap that work.
func (l *Lease) Run(ctx context.Context) {
	defer func() {
		l.inner.Store(false)
		l.heldUntil.Store(0)
		l.waitTasks(10 * time.Second)
		// Always try to delete our row. A take interrupted by ctx.Done() returns
		// leader=false, which must not skip the release: the row still names us
		// until expiry, and a standby would wait the full Expire.
		_ = l.release(context.Background())
	}()
	wasLeader := false
	for {
		started := time.Now()
		leader, epoch, err := l.take(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if l.o.Log != nil {
				l.o.Log.Warn("the leader lease could not be renewed", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(l.o.Renew):
			}
			continue
		}
		l.healthyUntil.Store(started.Add(l.o.Expire).UnixNano())
		if leader != wasLeader {
			if l.o.Log != nil {
				if leader {
					l.o.Log.Info("this instance became the leader", "lease", l.o.Name, "id", l.o.ID, "epoch", epoch)
				} else {
					l.o.Log.Warn("this instance gave up the leader lease; a standby answers /healthz with 503", "lease", l.o.Name, "id", l.o.ID)
				}
			}
			wasLeader = leader
		}
		if leader {
			l.epoch.Store(epoch)
			l.heldUntil.Store(started.Add(l.o.Expire - l.o.margin()).UnixNano())
			l.inner.Store(true)
		} else {
			l.inner.Store(false)
			l.heldUntil.Store(0)
		}
		select {
		case <-ctx.Done():
			l.inner.Store(false)
			l.heldUntil.Store(0)
			return
		case <-time.After(l.o.Renew):
		}
	}
}

// Leader reports whether this instance holds the lease and the local deadline has not passed.
// /healthz answers 503 when it does not, and the front sends its traffic elsewhere.
func (l *Lease) Leader() bool {
	if !l.inner.Load() {
		return false
	}
	until := l.heldUntil.Load()
	if until == 0 {
		return false
	}
	return time.Now().Before(time.Unix(0, until))
}

// Healthy reports whether the last take heard from the database (held or not) within the expiry.
// /livez answers 200 for a waiting standby, and 503 when this instance cannot reach PostgreSQL.
func (l *Lease) Healthy() bool {
	until := l.healthyUntil.Load()
	if until == 0 {
		return false
	}
	return time.Now().Before(time.Unix(0, until))
}

// startTask accounts for one Single fn. False means Run is already waiting to release.
func (l *Lease) startTask() bool {
	l.tasksMu.Lock()
	defer l.tasksMu.Unlock()
	if l.stopping.Load() {
		return false
	}
	l.tasks.Add(1)
	return true
}

func (l *Lease) waitTasks(d time.Duration) {
	l.tasksMu.Lock()
	l.stopping.Store(true)
	l.tasksMu.Unlock()
	wait := make(chan struct{})
	go func() {
		l.tasks.Wait()
		close(wait)
	}()
	select {
	case <-wait:
	case <-time.After(d):
		if l.o.Log != nil {
			l.o.Log.Warn("leader work did not return before the lease was released", "lease", l.o.Name, "id", l.o.ID)
		}
	}
}

// ID names this instance, as Run logs it.
func (l *Lease) ID() string { return l.o.ID }

// Epoch is the epoch of the last successful take; zero before the first one.
func (l *Lease) Epoch() int64 { return l.epoch.Load() }

// CheckWrite looks at the leader row inside a write transaction (FOR SHARE) and refuses the write
// when this instance is not the holder at the epoch it last took. Call it at the start of every
// write transaction when high availability is on. The transaction is given the lease's expiry as
// idle_in_transaction_session_timeout and lock_timeout, so a paused or partitioned primary cannot
// hold the row lock past the expiry and block a standby's take.
func (l *Lease) CheckWrite(ctx context.Context, tx *sql.Tx) error {
	if l == nil {
		return nil
	}
	ms := l.o.Expire.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	timeout := strconv.FormatInt(ms, 10)
	if _, err := tx.ExecContext(ctx, `SELECT set_config('idle_in_transaction_session_timeout', $1, true), set_config('lock_timeout', $1, true)`, timeout); err != nil {
		return err
	}
	var holder string
	var epoch int64
	var held bool
	err := tx.QueryRowContext(ctx, `SELECT holder, epoch, expires > now() FROM leader WHERE name = $1 FOR SHARE`, l.o.Name).
		Scan(&holder, &epoch, &held)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotLeader
	}
	if err != nil {
		return err
	}
	if !held || holder != l.o.ID || epoch != l.epoch.Load() {
		return ErrNotLeader
	}
	return nil
}

// Single runs what only the leader may do: fn runs in its own context while this instance holds
// the lease, is cancelled the moment it does not, and starts again — fresh, as after a restart —
// when the lease comes back. What survives a switch is what the database holds; fn should read its
// state from there on every start, as the service does after a restart. The lease's Run must be
// running for the changes to arrive. Run waits for fn to return before it releases the lease.
func Single(ctx context.Context, l *Lease, fn func(context.Context)) {
	go func() {
		watch := time.NewTicker(100 * time.Millisecond) // an atomic read: the leader's work follows the flip closely
		defer watch.Stop()
		for {
			for !l.Leader() { // wait for the lease
				select {
				case <-ctx.Done():
					return
				case <-watch.C:
				}
			}
			// the lease is held: run fn until it is not, or ctx ends
			if ctx.Err() != nil {
				return
			}
			ictx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			if !l.startTask() {
				cancel()
				return
			}
			go func() {
				defer close(done)
				defer l.tasks.Done()
				fn(ictx)
			}()
			hold := true
			for hold {
				select {
				case <-ctx.Done():
					cancel()
					<-done
					return
				case <-watch.C:
					hold = l.Leader()
				}
			}
			cancel() // leadership lost: stop what only the leader does
			<-done
			// and wait for the lease again
		}
	}()
}

// OpenPool opens the lease's own small pool over the DSN: one connection, for a renewal is one
// statement that must never wait behind the store's writes. A statement and lock timeout of 2s
// bounds a hung take so it cannot sit until TCP keepalive gives up. expire is the lease's
// expiry: idle-in-transaction and TCP keepalives are set from it so a write that holds FOR SHARE
// cannot block a take past the switch.
func OpenPool(dsn string, expire time.Duration) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("postgres: the DSN is not valid")
	}
	applyLeaseTimeouts(cfg, expire)
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Database, err)
	}
	return db, nil
}

// applyLeaseTimeouts sets the lease pool's statement and lock timeouts short (a hung take
// must fail in 2s), and idle-in-transaction plus TCP keepalives from the lease's expiry so a
// write holding FOR SHARE cannot outlast the switch.
func applyLeaseTimeouts(cfg *pgx.ConnConfig, expire time.Duration) {
	if expire <= 0 {
		expire = 15 * time.Second
	}
	cfg.RuntimeParams["application_name"] = "beacon-lease"
	cfg.RuntimeParams["statement_timeout"] = "2000"
	cfg.RuntimeParams["lock_timeout"] = "2000"
	ms := expire.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	cfg.RuntimeParams["idle_in_transaction_session_timeout"] = strconv.FormatInt(ms, 10)
	sec := int((expire + time.Second - 1) / time.Second)
	if sec < 1 {
		sec = 1
	}
	cfg.RuntimeParams["tcp_keepalives_idle"] = strconv.Itoa(sec)
	cfg.RuntimeParams["tcp_keepalives_interval"] = "1"
	cfg.RuntimeParams["tcp_keepalives_count"] = "3"
}
