// Package events is the append-only half of the store: the audit log and the record of secret
// uses, in tables with indexes on time, actor and object, chained by hash so that a change or a
// deletion among them can be seen (the plan's §14): each record's hash is SHA-256 over the hash
// of the record before it and a canonical encoding of itself. Checkpoints — signed summaries of
// the chain head, by default every 24 hours — anchor the chain so a rewritten tail cannot pass;
// pruning old events keeps the checkpoints, and the chain that remains still verifies from them.
//
// Every write is committed before anything is handed to a caller (the plan's first invariant:
// record first, then give), which is what the store's audit always did and the secret uses of the
// later packages need too: AppendTx adds the record inside a caller's transaction, so a use and
// its record commit together or not at all.
//
// The tables live in the same database as the key-value space, over database/sql handles (the
// pools of internal/kv: SQLite by default, PostgreSQL under storage.driver postgres). The dialect
// says which: only the placeholder spelling and the table options differ.
package events

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/openlinkos/beacon/internal/kv"
)

// Dialect is the SQL the log's statements are written for. The queries are kept in one spelling
// (SQLite's, with ? placeholders); PostgreSQL's are rewritten from it, so the two backends run the
// same statements and cannot drift apart.
type Dialect int

const (
	// SQLite is the default dialect: ? placeholders and WITHOUT ROWID tables.
	SQLite Dialect = iota
	// PostgreSQL writes $N placeholders and plain heap tables.
	PostgreSQL
)

// placeholder rewrites a statement from ? to $1, $2, … for PostgreSQL.
func (d Dialect) placeholder(sql string) string {
	if d == SQLite || !strings.Contains(sql, "?") {
		return sql
	}
	var b strings.Builder
	n := 0
	for {
		i := strings.IndexByte(sql, '?')
		if i < 0 {
			b.WriteString(sql)
			return b.String()
		}
		n++
		b.WriteString(sql[:i])
		b.WriteString("$")
		b.WriteString(strconv.Itoa(n))
		sql = sql[i+1:]
	}
}

// Event is one record in a log. Actor is who did it (a person, an agent, a rule or the system),
// OnBehalf the person an agent acted for; Via is how (api, mcp, card, console, cli, hook);
// Object what it touched; Context carries an alert group, a session or the tool called. IP and
// Client name where the call came from. Seq, PrevHash and Hash are set by Append; the hash chain
// runs over the rest.
type Event struct {
	Seq      uint64    `json:"seq"`
	Time     time.Time `json:"time"`
	Actor    string    `json:"actor"`
	OnBehalf string    `json:"on_behalf,omitempty"`
	Via      string    `json:"via"`
	Action   string    `json:"action"`
	Object   string    `json:"object,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Context  string    `json:"context,omitempty"`
	IP       string    `json:"ip,omitempty"`
	Client   string    `json:"client,omitempty"`
	PrevHash string    `json:"prev_hash"`
	Hash     string    `json:"hash"`
}

// Checkpoint is a signed summary of the chain at a moment: the head hash at Seq, the first record
// it covers, how many, and when. Checkpoints chain among themselves (Prev is the hash of the
// checkpoint before); Signature is an HMAC of the record with the signer's key, so a changed
// checkpoint is as visible as a changed event.
type Checkpoint struct {
	Log       string    `json:"log"`
	Seq       uint64    `json:"seq"` // the last record this checkpoint covers
	FirstSeq  uint64    `json:"first_seq"`
	Count     uint64    `json:"count"`
	Head      string    `json:"head"` // the chain head at Seq
	Time      time.Time `json:"time"`
	Prev      string    `json:"prev"` // the hash of the checkpoint before this one
	Hash      string    `json:"hash"` // over the record itself
	Signer    string    `json:"signer"`
	Signature string    `json:"signature"`
}

// PruneMark is the signed record that a prefix of the log was dropped on purpose. Verify treats a
// gap as a break unless a prune mark sits at the last dropped sequence: a scheduled checkpoint
// alone is not permission to have deleted the records it covered.
type PruneMark struct {
	Log            string    `json:"log"`
	Seq            uint64    `json:"seq"`
	CutTime        time.Time `json:"cut_time"`
	RemainingFirst time.Time `json:"remaining_first,omitzero"`
	Head           string    `json:"head"`
	Hash           string    `json:"hash"`
	Signature      string    `json:"signature"`
}

// Filter narrows a Query. Empty strings and zero times leave that part open; Limit is capped at
// 1000 and defaults to 100; Offset pages through. Records come back newest first.
type Filter struct {
	Actor, OnBehalf, Action, Object string
	From, To                        time.Time
	Limit, Offset                   int
}

// Options is how a Log is opened.
type Options struct {
	// Dialect is the database under the handles: SQLite (the default) or PostgreSQL.
	Dialect Dialect
	// SignKey signs checkpoints (HMAC-SHA256). When nil, one is made at random and kept in the
	// database, which holds a tamperer who cannot write the tables — but whoever can write them
	// all could re-sign; the key hierarchy that comes with the vault replaces this with a key
	// derived from the root key, and SignerID names whichever is in use.
	SignKey []byte
	// SignerID names the key in the checkpoints it signs ("generated" for the one made here).
	SignerID string
	// CheckpointInterval is how often a checkpoint is written as records arrive; 24h by default.
	// A checkpoint is written no sooner than this since the last one.
	CheckpointInterval time.Duration
	// Retention is how long records are kept. When set, Verify refuses a prune whose cut still
	// falls inside it. Zero leaves that check off.
	Retention time.Duration
	// Now is the clock; tests move it.
	Now func() time.Time
}

// ErrReadOnly means the log was open for reading and something tried to write.
var ErrReadOnly = errors.New("the event log is open read-only")

// ErrBrokenChain is Verify's answer when the chain does not hold.
var ErrBrokenChain = errors.New("the event chain is broken")

// Result is what Verify found: how much it walked, where it got to, and with which checkpoint it
// anchored. When Verify returns an error, BrokenAt names the record that failed.
type Result struct {
	Events      uint64
	FirstSeq    uint64
	LastSeq     uint64
	Head        string
	Checkpoints int
	BrokenAt    uint64
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)

// Log is one event table with its chain, checkpoints and signer key. It is safe for concurrent
// use; writes serialise on the write pool, as every write in the store does.
type Log struct {
	read, write *sql.DB
	table       string
	dialect     Dialect
	key         []byte
	signer      string
	interval    time.Duration
	retention   time.Duration
	now         func() time.Time
	guard       func(context.Context, *sql.Tx) error
}

// appendLockNamespace is the first key of the per-log advisory lock AppendTx takes, so two
// writers cannot both read the same head. SchemaLock is a single-argument lock and lives in
// another space.
const appendLockNamespace = 784222

// SetGuard runs fn at the start of every write transaction this log begins (the HA lease check).
// AppendTx, which a caller already holds a transaction for, still takes the per-log advisory lock.
func (l *Log) SetGuard(fn func(context.Context, *sql.Tx) error) {
	l.guard = fn
}

// Open prepares the log called name (audit_events, secret_uses) over the two pools (write may be
// nil for a read-only log). The name becomes the table's; lower-case letters, digits and
// underscores.
func Open(read, write *sql.DB, name string, o Options) (*Log, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("event log %q: lower-case letters, digits and underscores", name)
	}
	if o.CheckpointInterval <= 0 {
		o.CheckpointInterval = 24 * time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.SignerID == "" {
		o.SignerID = "generated"
	}
	l := &Log{read: read, write: write, table: name, dialect: o.Dialect, interval: o.CheckpointInterval, retention: o.Retention, now: o.Now}
	if err := l.create(); err != nil {
		return nil, err
	}
	key := o.SignKey
	if len(key) == 0 {
		var err error
		if key, err = l.storedKey(); err != nil {
			return nil, err
		}
	}
	l.key, l.signer = key, o.SignerID
	return l, nil
}

// q writes the statement for the log's dialect: ? placeholders become $1, $2, … for PostgreSQL.
func (l *Log) q(sql string) string { return l.dialect.placeholder(sql) }

func (l *Log) create() error {
	if l.write == nil {
		return nil // a read-only log reads tables the service made
	}
	ctx := context.Background()
	tx, err := l.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if l.dialect == PostgreSQL { // the pair's two instances, starting together, serialise here
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, kv.SchemaLock); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	// SQLite keeps whole-number primary keys and blobs; PostgreSQL takes BIGINT and BYTEA, and has
	// no WITHOUT ROWID (its tables are heaps with primary-key indexes anyway).
	integer, blob, rowid := "INTEGER", "BLOB", ""
	if l.dialect == PostgreSQL {
		integer, blob = "BIGINT", "BYTEA"
	} else {
		rowid = " WITHOUT ROWID"
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS "` + l.table + `" (
			seq ` + integer + ` PRIMARY KEY,
			time ` + integer + ` NOT NULL,
			actor TEXT NOT NULL DEFAULT '',
			on_behalf TEXT NOT NULL DEFAULT '',
			via TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL DEFAULT '',
			object TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL DEFAULT '',
			context TEXT NOT NULL DEFAULT '',
			ip TEXT NOT NULL DEFAULT '',
			client TEXT NOT NULL DEFAULT '',
			hash TEXT NOT NULL,
			prev_hash TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS "` + l.table + `_time" ON "` + l.table + `" (time)`,
		`CREATE INDEX IF NOT EXISTS "` + l.table + `_actor" ON "` + l.table + `" (actor)`,
		`CREATE INDEX IF NOT EXISTS "` + l.table + `_object" ON "` + l.table + `" (object)`,
		`CREATE TABLE IF NOT EXISTS event_checkpoints (
			log TEXT NOT NULL,
			seq ` + integer + ` NOT NULL,
			first_seq ` + integer + ` NOT NULL,
			count ` + integer + ` NOT NULL,
			head TEXT NOT NULL,
			time ` + integer + ` NOT NULL,
			prev TEXT NOT NULL DEFAULT '',
			hash TEXT NOT NULL,
			signer TEXT NOT NULL DEFAULT '',
			signature TEXT NOT NULL,
			PRIMARY KEY (log, seq)
		)` + rowid,
		`CREATE TABLE IF NOT EXISTS event_keys (
			log TEXT NOT NULL PRIMARY KEY,
			key ` + blob + ` NOT NULL
		)` + rowid,
		`CREATE TABLE IF NOT EXISTS event_prunes (
			log TEXT NOT NULL,
			seq ` + integer + ` NOT NULL,
			cut_time ` + integer + ` NOT NULL,
			remaining_first ` + integer + ` NOT NULL DEFAULT 0,
			head TEXT NOT NULL,
			hash TEXT NOT NULL,
			signature TEXT NOT NULL,
			PRIMARY KEY (log, seq)
		)` + rowid,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// storedKey reads the log's generated signer key, making one at random the first time. The insert
// keeps what a concurrent opener stored already (both instances of a pair, starting together): the
// row is read back either way, so every process signs with the one key the table holds.
func (l *Log) storedKey() ([]byte, error) {
	read := func() ([]byte, error) {
		var key []byte
		err := l.read.QueryRowContext(context.Background(), l.q(`SELECT key FROM event_keys WHERE log = ?`), l.table).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return key, err
	}
	if key, err := read(); err != nil || key != nil {
		return key, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if l.write == nil {
		return nil, errors.New("no signer key: the log is open read-only and never had one")
	}
	if _, err := l.write.ExecContext(context.Background(), l.q(`INSERT INTO event_keys (log, key) VALUES (?, ?)
		ON CONFLICT (log) DO NOTHING`), l.table, key); err != nil {
		return nil, err
	}
	return read() // what the table holds now, ours or the concurrent opener's
}

// SetClock sets the log's clock, which stamps what it writes; a nil clock is the real one. The
// store hands its own clock over, so what a log stamps matches the rest of what it does.
func (l *Log) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	l.now = now
}

// ---- the chain

// canonical is what a record's hash is taken over: a version mark, the sequence, the time, every
// field length-prefixed, and the hash of the record before.
func canonical(e *Event) []byte {
	out := make([]byte, 0, 256)
	out = append(out, "beacon/event/v1\x00"...)
	var b8 [8]byte
	binary.BigEndian.PutUint64(b8[:], e.Seq)
	out = append(out, b8[:]...)
	binary.BigEndian.PutUint64(b8[:], uint64(e.Time.UnixNano()))
	out = append(out, b8[:]...)
	for _, s := range []string{e.Actor, e.OnBehalf, e.Via, e.Action, e.Object, e.Detail, e.Reason, e.Context, e.IP, e.Client, e.PrevHash} {
		var b4 [4]byte
		binary.BigEndian.PutUint32(b4[:], uint32(len(s)))
		out = append(out, b4[:]...)
		out = append(out, s...)
	}
	return out
}

func sum(prev string, e *Event) string {
	e.PrevHash = prev
	h := sha256.Sum256(canonical(e))
	return hex.EncodeToString(h[:])
}

func canonicalCheckpoint(c *Checkpoint) []byte {
	out := make([]byte, 0, 160)
	out = append(out, "beacon/checkpoint/v1\x00"...)
	out = append(out, c.Log...)
	out = append(out, 0)
	var b8 [8]byte
	for _, v := range []uint64{c.Seq, c.FirstSeq, c.Count} {
		binary.BigEndian.PutUint64(b8[:], v)
		out = append(out, b8[:]...)
	}
	binary.BigEndian.PutUint64(b8[:], uint64(c.Time.UnixNano()))
	out = append(out, b8[:]...)
	out = append(out, c.Head...)
	out = append(out, 0)
	out = append(out, c.Prev...)
	return out
}

func sign(key []byte, c *Checkpoint) string {
	m := hmac.New(sha256.New, key)
	m.Write(canonicalCheckpoint(c))
	return hex.EncodeToString(m.Sum(nil))
}

func canonicalPrune(p *PruneMark) []byte {
	out := make([]byte, 0, 160)
	out = append(out, "beacon/prune/v1\x00"...)
	out = append(out, p.Log...)
	out = append(out, 0)
	var b8 [8]byte
	binary.BigEndian.PutUint64(b8[:], p.Seq)
	out = append(out, b8[:]...)
	binary.BigEndian.PutUint64(b8[:], uint64(p.CutTime.UnixNano()))
	out = append(out, b8[:]...)
	var remaining uint64
	if !p.RemainingFirst.IsZero() {
		remaining = uint64(p.RemainingFirst.UnixNano())
	}
	binary.BigEndian.PutUint64(b8[:], remaining)
	out = append(out, b8[:]...)
	out = append(out, p.Head...)
	return out
}

func signPrune(key []byte, p *PruneMark) string {
	m := hmac.New(sha256.New, key)
	m.Write(canonicalPrune(p))
	return hex.EncodeToString(m.Sum(nil))
}

// head is where the chain stands: the newest record, or the newest checkpoint when pruning has
// taken the records it covered. Anything appended continues from there.
func (l *Log) head(q interface {
	QueryRow(string, ...any) *sql.Row
}) (seq uint64, hash string, err error) {
	err = q.QueryRow(l.q(`SELECT seq, hash FROM "`+l.table+`" ORDER BY seq DESC LIMIT 1`)).Scan(&seq, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, "", err
	}
	if errors.Is(err, sql.ErrNoRows) {
		seq, hash = 0, ""
	}
	var cseq uint64
	var chead string
	err = q.QueryRow(l.q(`SELECT seq, head FROM event_checkpoints WHERE log = ? ORDER BY seq DESC LIMIT 1`), l.table).Scan(&cseq, &chead)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return 0, "", err
	case cseq > seq: // the checkpoint stands past the last record: pruning took them
		seq, hash = cseq, chead
	}
	return seq, hash, nil
}

// lastCheckpoint is the newest checkpoint, with its hash to chain the next one to.
func (l *Log) lastCheckpoint(q interface {
	QueryRow(string, ...any) *sql.Row
}) (Checkpoint, bool, error) {
	var c Checkpoint
	var when int64
	err := q.QueryRow(l.q(`SELECT seq, first_seq, count, head, time, prev, hash, signer, signature
		FROM event_checkpoints WHERE log = ? ORDER BY seq DESC LIMIT 1`), l.table).
		Scan(&c.Seq, &c.FirstSeq, &c.Count, &c.Head, &when, &c.Prev, &c.Hash, &c.Signer, &c.Signature)
	if errors.Is(err, sql.ErrNoRows) {
		return Checkpoint{}, false, nil
	}
	if err != nil {
		return Checkpoint{}, false, err
	}
	c.Log, c.Time = l.table, time.Unix(0, when).UTC()
	return c, true, nil
}

// Append adds e to the log, filling in its sequence and hashes, and writes a checkpoint when the
// interval has passed. Time is stamped when zero.
func (l *Log) Append(e Event) (Event, error) {
	if l.write == nil {
		return Event{}, ErrReadOnly
	}
	err := l.withTx(func(tx *sql.Tx) error { return l.AppendTx(tx, &e) })
	if err != nil {
		return Event{}, err
	}
	return e, nil
}

// AppendTx adds e inside a transaction the caller holds (a use and its record commit together);
// the transaction is not committed here, so the caller can add more to it first. On PostgreSQL it
// takes a per-log advisory lock first so two writers cannot both read the same head.
func (l *Log) AppendTx(tx *sql.Tx, e *Event) error {
	ctx := context.Background()
	if err := l.lockAppend(ctx, tx); err != nil {
		return err
	}
	if e.Time.IsZero() {
		e.Time = l.now().UTC()
	} else {
		e.Time = e.Time.UTC()
	}
	hseq, hhash, err := l.head(tx)
	if err != nil {
		return err
	}
	e.Seq = hseq + 1
	e.Hash = sum(hhash, e)
	if _, err := tx.ExecContext(ctx, l.q(`INSERT INTO "`+l.table+`"
		(seq, time, actor, on_behalf, via, action, object, detail, reason, context, ip, client, hash, prev_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		e.Seq, e.Time.UnixNano(), e.Actor, e.OnBehalf, e.Via, e.Action, e.Object, e.Detail, e.Reason, e.Context, e.IP, e.Client, e.Hash, e.PrevHash); err != nil {
		return err
	}
	// a checkpoint when due, covering everything since the last one
	last, ok, err := l.lastCheckpoint(tx)
	if err != nil {
		return err
	}
	if !ok || l.now().Sub(last.Time) >= l.interval {
		if _, err := l.checkpointTx(tx, e.Seq, e.Hash, last); err != nil {
			return err
		}
	}
	return nil
}

// checkpointTx writes a checkpoint covering first..head. last is the checkpoint before, if there
// is one.
func (l *Log) checkpointTx(tx *sql.Tx, seq uint64, hash string, last Checkpoint) (Checkpoint, error) {
	ctx := context.Background()
	if last.Seq > seq {
		return Checkpoint{}, fmt.Errorf("checkpoint at %d cannot follow one at %d", seq, last.Seq)
	}
	first, count := uint64(1), seq
	if last.Seq > 0 {
		first, count = last.Seq+1, seq-last.Seq
	}
	c := Checkpoint{Log: l.table, Seq: seq, FirstSeq: first, Count: count, Head: hash,
		Time: l.now().UTC(), Prev: last.Hash, Signer: l.signer}
	chash := sha256.Sum256(canonicalCheckpoint(&c))
	c.Hash = hex.EncodeToString(chash[:])
	c.Signature = sign(l.key, &c)
	if _, err := tx.ExecContext(ctx, l.q(`INSERT INTO event_checkpoints
		(log, seq, first_seq, count, head, time, prev, hash, signer, signature)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (log, seq) DO NOTHING`),
		c.Log, c.Seq, c.FirstSeq, c.Count, c.Head, c.Time.UnixNano(), c.Prev, c.Hash, c.Signer, c.Signature); err != nil {
		return Checkpoint{}, err
	}
	return c, nil
}

// Checkpoint writes one now, covering everything since the last.
func (l *Log) Checkpoint() (Checkpoint, error) {
	if l.write == nil {
		return Checkpoint{}, ErrReadOnly
	}
	var out Checkpoint
	err := l.withTx(func(tx *sql.Tx) error {
		seq, hash, err := l.head(tx)
		if err != nil {
			return err
		}
		last, _, err := l.lastCheckpoint(tx)
		if err != nil {
			return err
		}
		if seq == 0 || (last.Seq >= seq && last.Hash != "") {
			out = last // nothing new to cover
			return nil
		}
		out, err = l.checkpointTx(tx, seq, hash, last)
		return err
	})
	return out, err
}

// Query returns the records matching f, newest first, and how many match in all.
func (l *Log) Query(f Filter) ([]Event, int, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	f.Limit = min(f.Limit, 1000)
	if f.Offset < 0 {
		f.Offset = 0
	}
	where, args := []string{"1=1"}, []any{}
	if f.Actor != "" {
		where, args = append(where, "actor = ?"), append(args, f.Actor)
	}
	if f.OnBehalf != "" {
		where, args = append(where, "on_behalf = ?"), append(args, f.OnBehalf)
	}
	if f.Action != "" {
		where, args = append(where, "action = ?"), append(args, f.Action)
	}
	if f.Object != "" {
		where, args = append(where, "object = ?"), append(args, f.Object)
	}
	if !f.From.IsZero() {
		where, args = append(where, "time >= ?"), append(args, f.From.UnixNano())
	}
	if !f.To.IsZero() {
		where, args = append(where, "time <= ?"), append(args, f.To.UnixNano())
	}
	cond := ""
	for i, w := range where {
		if i > 0 {
			cond += " AND "
		}
		cond += w
	}
	var total int
	if err := l.read.QueryRowContext(context.Background(), l.q(`SELECT COUNT(*) FROM "`+l.table+`" WHERE `+cond), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := l.read.QueryContext(context.Background(), l.q(`SELECT seq, time, actor, on_behalf, via, action, object, detail, reason, context, ip, client, hash, prev_hash
		FROM "`+l.table+`" WHERE `+cond+` ORDER BY seq DESC LIMIT ? OFFSET ?`),
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		var e Event
		var when int64
		if err := rows.Scan(&e.Seq, &when, &e.Actor, &e.OnBehalf, &e.Via, &e.Action, &e.Object, &e.Detail, &e.Reason, &e.Context, &e.IP, &e.Client, &e.Hash, &e.PrevHash); err != nil {
			return nil, 0, err
		}
		e.Time = time.Unix(0, when).UTC()
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// Prune removes the records older than retention, writing a checkpoint over them first so the
// chain that remains still verifies, and reports how many went. Checkpoints are never removed.
// The cut is a prefix of the sequence: only records before the earliest record still inside
// the window. When a later checkpoint already sits past that prefix, the cut moves back to the
// newest checkpoint at or before it.
func (l *Log) Prune(retention time.Duration) (int, error) {
	ctx := context.Background()
	if l.write == nil {
		return 0, ErrReadOnly
	}
	if retention <= 0 {
		return 0, nil
	}
	cutoff := l.now().Add(-retention).UnixNano()
	deleted := 0
	err := l.withTx(func(tx *sql.Tx) error {
		cutSeq, err := l.pruneCutSeq(tx, cutoff)
		if err != nil || cutSeq == 0 {
			return err
		}
		last, _, err := l.lastCheckpoint(tx)
		if err != nil {
			return err
		}
		prev, err := l.checkpointAtOrBefore(tx, cutSeq)
		if err != nil {
			return err
		}
		if last.Seq > cutSeq {
			if prev.Seq == 0 {
				return nil
			}
			cutSeq = prev.Seq
		} else if prev.Seq != cutSeq {
			var hash string
			if err := tx.QueryRowContext(ctx, l.q(`SELECT hash FROM "`+l.table+`" WHERE seq = ?`), cutSeq).Scan(&hash); err != nil {
				return err
			}
			if _, err := l.checkpointTx(tx, cutSeq, hash, prev); err != nil {
				return err
			}
		}
		if err := l.writePruneMark(tx, cutSeq, cutoff); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, l.q(`DELETE FROM "`+l.table+`" WHERE seq <= ?`), cutSeq)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		deleted = int(n)
		return nil
	})
	return deleted, err
}

func (l *Log) pruneCutSeq(tx *sql.Tx, cutoff int64) (uint64, error) {
	ctx := context.Background()
	var minAfter sql.NullInt64
	if err := tx.QueryRowContext(ctx, l.q(`SELECT MIN(seq) FROM "`+l.table+`" WHERE time > ?`), cutoff).Scan(&minAfter); err != nil {
		return 0, err
	}
	if minAfter.Valid {
		if minAfter.Int64 <= 1 {
			return 0, nil
		}
		return uint64(minAfter.Int64) - 1, nil
	}
	var maxSeq sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM "`+l.table+`"`).Scan(&maxSeq); err != nil {
		return 0, err
	}
	if !maxSeq.Valid {
		return 0, nil
	}
	return uint64(maxSeq.Int64), nil
}

func (l *Log) checkpointAtOrBefore(tx *sql.Tx, seq uint64) (Checkpoint, error) {
	var c Checkpoint
	var when int64
	err := tx.QueryRowContext(context.Background(), l.q(`SELECT seq, first_seq, count, head, time, prev, hash, signer, signature
		FROM event_checkpoints WHERE log = ? AND seq <= ? ORDER BY seq DESC LIMIT 1`), l.table, seq).
		Scan(&c.Seq, &c.FirstSeq, &c.Count, &c.Head, &when, &c.Prev, &c.Hash, &c.Signer, &c.Signature)
	if errors.Is(err, sql.ErrNoRows) {
		return Checkpoint{}, nil
	}
	if err != nil {
		return Checkpoint{}, err
	}
	c.Log, c.Time = l.table, time.Unix(0, when).UTC()
	return c, nil
}

func (l *Log) writePruneMark(tx *sql.Tx, cutSeq uint64, cutoff int64) error {
	ctx := context.Background()
	var head string
	err := tx.QueryRowContext(ctx, l.q(`SELECT hash FROM "`+l.table+`" WHERE seq = ?`), cutSeq).Scan(&head)
	if err != nil {
		if err := tx.QueryRowContext(ctx, l.q(`SELECT head FROM event_checkpoints WHERE log = ? AND seq = ?`), l.table, cutSeq).Scan(&head); err != nil {
			return err
		}
	}
	var remaining int64
	if err := tx.QueryRowContext(ctx, l.q(`SELECT COALESCE(MIN(time), 0) FROM "`+l.table+`" WHERE seq > ?`), cutSeq).Scan(&remaining); err != nil {
		return err
	}
	p := PruneMark{Log: l.table, Seq: cutSeq, CutTime: time.Unix(0, cutoff).UTC(), Head: head}
	if remaining > 0 {
		p.RemainingFirst = time.Unix(0, remaining).UTC()
	}
	sum := sha256.Sum256(canonicalPrune(&p))
	p.Hash = hex.EncodeToString(sum[:])
	p.Signature = signPrune(l.key, &p)
	_, err = tx.ExecContext(ctx, l.q(`INSERT INTO event_prunes
		(log, seq, cut_time, remaining_first, head, hash, signature)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (log, seq) DO UPDATE SET
			cut_time = excluded.cut_time, remaining_first = excluded.remaining_first,
			head = excluded.head, hash = excluded.hash, signature = excluded.signature`),
		p.Log, p.Seq, p.CutTime.UnixNano(), remaining, p.Head, p.Hash, p.Signature)
	return err
}

// Checkpoints lists them, oldest first.
func (l *Log) Checkpoints() ([]Checkpoint, error) {
	rows, err := l.read.QueryContext(context.Background(), l.q(`SELECT seq, first_seq, count, head, time, prev, hash, signer, signature
		FROM event_checkpoints WHERE log = ? ORDER BY seq ASC`), l.table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		var when int64
		if err := rows.Scan(&c.Seq, &c.FirstSeq, &c.Count, &c.Head, &when, &c.Prev, &c.Hash, &c.Signer, &c.Signature); err != nil {
			return nil, err
		}
		c.Log, c.Time = l.table, time.Unix(0, when).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// Verify walks the whole chain of the records still present. A gap is a break unless a signed
// prune mark sits at the last dropped sequence. VerifySince re-anchors at those marks.
func (l *Log) Verify() (Result, error) { return l.verify(0) }

// VerifySince verifies the chain from the checkpoint at seq onwards (the records it covered may
// be gone); there must be a checkpoint at seq. The walk starts after that checkpoint, not at the
// first record still present: an unpruned log must still verify from a mid-chain checkpoint, and
// a prune that left later records must still verify from a checkpoint among them. A prune mark
// in the range re-anchors the walk so a verify from before a prune still holds.
func (l *Log) VerifySince(seq uint64) (Result, error) { return l.verify(seq) }

func (l *Log) verify(from uint64) (Result, error) {
	db := l.read
	if db == nil {
		db = l.write
	}
	// Repeatable Read so MIN(seq), the prune marks and the remaining rows are one snapshot.
	// Under READ COMMITTED a prune can empty the table between those statements and look like a break.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback() }()
	return l.verifyTx(tx, from)
}

func (l *Log) verifyTx(tx *sql.Tx, from uint64) (Result, error) {
	ctx := context.Background()
	checkpoints, err := l.checkpointsTx(tx)
	if err != nil {
		return Result{}, err
	}
	prunes, err := l.prunesTx(tx)
	if err != nil {
		return Result{}, err
	}
	bySeq := map[uint64]Checkpoint{}
	var prevC Checkpoint
	for i, c := range checkpoints {
		bySeq[c.Seq] = c
		rehash := sha256.Sum256(canonicalCheckpoint(&c))
		if hex.EncodeToString(rehash[:]) != c.Hash {
			return broken(Result{}, c.Seq, "checkpoint at %d was changed", c.Seq)
		}
		if !hmac.Equal([]byte(sign(l.key, &c)), []byte(c.Signature)) {
			return broken(Result{}, c.Seq, "checkpoint at %d fails its signature", c.Seq)
		}
		if i > 0 && (c.Prev != prevC.Hash || c.Seq <= prevC.Seq) {
			return broken(Result{}, c.Seq, "checkpoint at %d does not follow the one at %d", prevC.Seq, c.Seq)
		}
		prevC = c
	}
	if from > 0 {
		if _, ok := bySeq[from]; !ok {
			return Result{}, fmt.Errorf("no checkpoint at %d; the checkpoints are at %s", from, seqList(checkpoints))
		}
	}
	var minSeq sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(seq) FROM "`+l.table+`"`).Scan(&minSeq); err != nil {
		return Result{}, err
	}
	if !minSeq.Valid {
		return l.verifyEmpty(from, checkpoints, prunes)
	}
	firstPresent := uint64(minSeq.Int64)
	for seq, p := range prunes {
		if firstPresent <= seq {
			return broken(Result{FirstSeq: firstPresent, Checkpoints: len(checkpoints)}, seq,
				"a prune at %d still has records at or before it", seq)
		}
		if l.retention > 0 && l.now().Sub(p.CutTime) < l.retention {
			return broken(Result{FirstSeq: firstPresent, Checkpoints: len(checkpoints)}, seq,
				"records before %d are still inside the retention", seq+1)
		}
	}
	anchor := ""
	start := firstPresent
	if from > 0 {
		anchor = bySeq[from].Head
		start = from + 1
		for _, c := range checkpoints {
			if p, ok := prunes[c.Seq]; ok && c.Seq >= from && c.Seq < firstPresent {
				anchor = p.Head
				start = c.Seq + 1
			}
		}
		if firstPresent > start {
			return broken(Result{FirstSeq: firstPresent, Checkpoints: len(checkpoints)}, firstPresent,
				"record %d follows records that are gone without a prune checkpoint", firstPresent)
		}
	} else if firstPresent > 1 {
		p, ok := prunes[firstPresent-1]
		if !ok {
			return broken(Result{FirstSeq: firstPresent, Checkpoints: len(checkpoints)}, firstPresent,
				"record %d follows records that are gone without a prune checkpoint", firstPresent)
		}
		anchor = p.Head
	}
	res := Result{FirstSeq: start, Checkpoints: len(checkpoints)}
	rows, err := tx.QueryContext(ctx, l.q(`SELECT seq, time, actor, on_behalf, via, action, object, detail, reason, context, ip, client, hash, prev_hash
		FROM "`+l.table+`" WHERE seq >= ? ORDER BY seq ASC`), start)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = rows.Close() }()
	expectPrev := anchor
	for rows.Next() {
		var e Event
		var when int64
		if err := rows.Scan(&e.Seq, &when, &e.Actor, &e.OnBehalf, &e.Via, &e.Action, &e.Object, &e.Detail, &e.Reason, &e.Context, &e.IP, &e.Client, &e.Hash, &e.PrevHash); err != nil {
			return res, err
		}
		e.Time = time.Unix(0, when).UTC()
		if e.PrevHash != expectPrev {
			return broken(res, e.Seq, "record %d does not continue the chain", e.Seq)
		}
		if e.Hash != sum(e.PrevHash, &e) {
			return broken(res, e.Seq, "record %d was changed", e.Seq)
		}
		if c, ok := bySeq[e.Seq]; ok && c.Head != e.Hash {
			return broken(res, e.Seq, "record %d does not match the checkpoint at it", e.Seq)
		}
		expectPrev, res.LastSeq, res.Head, res.Events = e.Hash, e.Seq, e.Hash, res.Events+1
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	if res.Events == 0 && len(checkpoints) > 0 {
		last := checkpoints[len(checkpoints)-1]
		if _, ok := prunes[last.Seq]; !ok {
			return broken(res, last.Seq, "the records are gone without a prune checkpoint")
		}
		res.Head, res.LastSeq = last.Head, last.Seq
	}
	if res.Events > 0 && len(checkpoints) > 0 && checkpoints[len(checkpoints)-1].Seq > res.LastSeq {
		c := checkpoints[len(checkpoints)-1]
		if _, ok := prunes[c.Seq]; !ok {
			return broken(res, c.Seq, "the records after the checkpoint at %d are gone without a prune", c.Seq)
		}
	}
	return res, nil
}

func (l *Log) verifyEmpty(from uint64, checkpoints []Checkpoint, prunes map[uint64]PruneMark) (Result, error) {
	if len(checkpoints) == 0 {
		return Result{}, nil
	}
	last := checkpoints[len(checkpoints)-1]
	if from > 0 && last.Seq <= from {
		return Result{Checkpoints: len(checkpoints), LastSeq: last.Seq, Head: last.Head}, nil
	}
	need := last.Seq
	if from > 0 {
		need = from
		for _, c := range checkpoints {
			if c.Seq > from {
				need = c.Seq
			}
		}
	}
	if _, ok := prunes[need]; !ok {
		if _, ok := prunes[last.Seq]; !ok {
			return broken(Result{Checkpoints: len(checkpoints)}, last.Seq, "the records are gone without a prune checkpoint")
		}
	}
	return Result{Checkpoints: len(checkpoints), LastSeq: last.Seq, Head: last.Head}, nil
}

func (l *Log) checkpointsTx(tx *sql.Tx) ([]Checkpoint, error) {
	rows, err := tx.QueryContext(context.Background(), l.q(`SELECT seq, first_seq, count, head, time, prev, hash, signer, signature
		FROM event_checkpoints WHERE log = ? ORDER BY seq ASC`), l.table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		var when int64
		if err := rows.Scan(&c.Seq, &c.FirstSeq, &c.Count, &c.Head, &when, &c.Prev, &c.Hash, &c.Signer, &c.Signature); err != nil {
			return nil, err
		}
		c.Log, c.Time = l.table, time.Unix(0, when).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

func (l *Log) prunesTx(tx *sql.Tx) (map[uint64]PruneMark, error) {
	rows, err := tx.QueryContext(context.Background(), l.q(`SELECT seq, cut_time, remaining_first, head, hash, signature
		FROM event_prunes WHERE log = ? ORDER BY seq ASC`), l.table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[uint64]PruneMark{}
	for rows.Next() {
		var p PruneMark
		var cut, remaining int64
		if err := rows.Scan(&p.Seq, &cut, &remaining, &p.Head, &p.Hash, &p.Signature); err != nil {
			return nil, err
		}
		p.Log, p.CutTime = l.table, time.Unix(0, cut).UTC()
		if remaining > 0 {
			p.RemainingFirst = time.Unix(0, remaining).UTC()
		}
		rehash := sha256.Sum256(canonicalPrune(&p))
		if hex.EncodeToString(rehash[:]) != p.Hash {
			return nil, fmt.Errorf("%w: prune at %d was changed", ErrBrokenChain, p.Seq)
		}
		if !hmac.Equal([]byte(signPrune(l.key, &p)), []byte(p.Signature)) {
			return nil, fmt.Errorf("%w: prune at %d fails its signature", ErrBrokenChain, p.Seq)
		}
		out[p.Seq] = p
	}
	return out, rows.Err()
}

func seqList(checkpoints []Checkpoint) string {
	out := ""
	for i, c := range checkpoints {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%d", c.Seq)
	}
	if out == "" {
		return "none"
	}
	return out
}

// broken records where the chain failed and says so; seq is the record or checkpoint in question.
func broken(res Result, seq uint64, format string, args ...any) (Result, error) {
	res.BrokenAt = seq
	return res, fmt.Errorf("%w: "+format, append([]any{ErrBrokenChain}, args...)...)
}

// withTx runs fn in one transaction on the write pool.
func withTx(db *sql.DB, fn func(tx *sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func retryable(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "23505" || pg.Code == "40P01"
	}
	return false
}

func (l *Log) appendLockKey() int32 {
	h := sha256.Sum256([]byte(l.table))
	return int32(binary.BigEndian.Uint32(h[:4]))
}

func (l *Log) lockAppend(ctx context.Context, tx *sql.Tx) error {
	if l.dialect != PostgreSQL {
		return nil
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, appendLockNamespace, l.appendLockKey())
	return err
}

// withTx runs fn in one write transaction, with the HA guard if set, and retries a unique
// violation or a deadlock (two writers racing the chain head).
func (l *Log) withTx(fn func(tx *sql.Tx) error) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		err = l.oneTx(fn)
		if err == nil || !retryable(err) {
			return err
		}
		time.Sleep(time.Duration(1<<attempt) * 5 * time.Millisecond)
	}
	return err
}

func (l *Log) oneTx(fn func(tx *sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := l.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if l.guard != nil {
		if err := l.guard(ctx, tx); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
