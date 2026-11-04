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
	now         func() time.Time
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
	l := &Log{read: read, write: write, table: name, dialect: o.Dialect, interval: o.CheckpointInterval, now: o.Now}
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
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// storedKey reads the log's generated signer key, making one at random the first time.
func (l *Log) storedKey() ([]byte, error) {
	var key []byte
	err := l.read.QueryRowContext(context.Background(), l.q(`SELECT key FROM event_keys WHERE log = ?`), l.table).Scan(&key)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if l.write == nil {
		return nil, errors.New("no signer key: the log is open read-only and never had one")
	}
	if _, err := l.write.ExecContext(context.Background(), l.q(`INSERT INTO event_keys (log, key) VALUES (?, ?)`), l.table, key); err != nil {
		return nil, err
	}
	return key, nil
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
	err := withTx(l.write, func(tx *sql.Tx) error { return l.AppendTx(tx, &e) })
	if err != nil {
		return Event{}, err
	}
	return e, nil
}

// AppendTx adds e inside a transaction the caller holds (a use and its record commit together);
// the transaction is not committed here, so the caller can add more to it first.
func (l *Log) AppendTx(tx *sql.Tx, e *Event) error {
	ctx := context.Background()
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
	err := withTx(l.write, func(tx *sql.Tx) error {
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
	err := withTx(l.write, func(tx *sql.Tx) error {
		var last int64
		err := tx.QueryRowContext(ctx, l.q(`SELECT seq FROM "`+l.table+`" WHERE time <= ? ORDER BY seq DESC LIMIT 1`), cutoff).Scan(&last)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // nothing that old
		}
		if err != nil {
			return err
		}
		var hash string
		if err := tx.QueryRowContext(ctx, l.q(`SELECT hash FROM "`+l.table+`" WHERE seq = ?`), last).Scan(&hash); err != nil {
			return err
		}
		prev, _, err := l.lastCheckpoint(tx)
		if err != nil {
			return err
		}
		if prev.Seq != uint64(last) { // a checkpoint at the cut is already the anchor
			if _, err := l.checkpointTx(tx, uint64(last), hash, prev); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, l.q(`DELETE FROM "`+l.table+`" WHERE seq <= ?`), last)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		deleted = int(n)
		return nil
	})
	return deleted, err
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

// Verify walks the whole chain of the records still present, from the checkpoint that anchors
// them, checking every hash and every checkpoint's signature. It holds when pruning has taken the
// old records: the first record present must continue from the checkpoint that covered what went,
// and records before the first one present without such a checkpoint mean the chain was tampered
// with — pruning never leaves that state.
func (l *Log) Verify() (Result, error) { return l.verify(0) }

// VerifySince verifies the chain from the checkpoint at seq onwards (the records it covered may
// be gone); there must be a checkpoint at seq.
func (l *Log) VerifySince(seq uint64) (Result, error) { return l.verify(seq) }

func (l *Log) verify(from uint64) (Result, error) {
	checkpoints, err := l.Checkpoints()
	if err != nil {
		return Result{}, err
	}
	// every checkpoint is well-formed, signed by this log's key, and chained to the one before
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
	// where the walk starts: after the checkpoint asked for, or at the first record present
	first := int64(1)
	if from > 0 {
		if _, ok := bySeq[from]; !ok {
			return Result{}, fmt.Errorf("no checkpoint at %d; the checkpoints are at %s", from, seqList(checkpoints))
		}
		first = int64(from) + 1
	} else if err := l.read.QueryRowContext(context.Background(), l.q(`SELECT COALESCE(MIN(seq), 1) FROM "`+l.table+`"`)).Scan(&first); err != nil {
		return Result{}, err
	}
	// the anchor: the newest checkpoint below the start; a chain beginning at 1 anchors itself
	anchor := ""
	found := false
	for _, c := range checkpoints {
		if c.Seq < uint64(first) {
			anchor, found = c.Head, true
		}
	}
	if first > 1 && !found {
		return broken(Result{FirstSeq: uint64(first)}, uint64(first), "record %d follows records that are gone without a checkpoint", first)
	}
	res := Result{FirstSeq: uint64(first), Checkpoints: len(checkpoints)}
	rows, err := l.read.QueryContext(context.Background(), l.q(`SELECT seq, time, actor, on_behalf, via, action, object, detail, reason, context, ip, client, hash, prev_hash
		FROM "`+l.table+`" WHERE seq >= ? ORDER BY seq ASC`), first)
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
	if res.Events == 0 && len(checkpoints) > 0 { // everything pruned: the checkpoints are the chain
		last := checkpoints[len(checkpoints)-1]
		res.Head, res.LastSeq = last.Head, last.Seq
	}
	// a checkpoint above the last record present, with records below it: pruning takes whole
	// beginnings, so those records were removed by hand
	if res.Events > 0 && len(checkpoints) > 0 && checkpoints[len(checkpoints)-1].Seq > res.LastSeq {
		c := checkpoints[len(checkpoints)-1]
		return broken(res, c.Seq, "the records after the checkpoint at %d are gone without a prune")
	}
	return res, nil
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
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
