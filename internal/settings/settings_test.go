package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/store"

	_ "modernc.org/sqlite"
)

type bot struct{ texts []string }

func (b *bot) Send(_ context.Context, t string) error { b.texts = append(b.texts, t); return nil }

type env struct {
	s   *Service
	st  *store.Store
	bot *bot
	db  string // the database file, for the tests that reach past Beacon into it
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := filepath.Join(t.TempDir(), "s.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	b := &bot{}
	s, err := Open(Deps{Store: st, Bot: b, Lang: i18n.EN})
	if err != nil {
		t.Fatal(err)
	}
	return &env{s: s, st: st, bot: b, db: db}
}

var (
	ctx = context.Background()
	api = Actor{Name: "claude-code", Via: "api"}
	why = "because the test needs a reason"
)

func raw(t string) json.RawMessage { return json.RawMessage(t) }

// The end the acceptance asks for: change a setting, read it back in force, revert, and see it
// restored — with the change and the revert both in the history, audited and echoed.
func TestSetReadRevert(t *testing.T) {
	e := newEnv(t)

	// what it starts with: the plan's default
	r, err := e.s.System("request.max_uses")
	if err != nil {
		t.Fatal(err)
	}
	if string(r.Value) != `"unlimited"` || r.From != "default" {
		t.Fatalf("starts at %s (%s), want the default unlimited", r.Value, r.From)
	}

	// change it
	ch, err := e.s.Set(ctx, api, ScopeSystem, "", "request.max_uses", raw("50"), "fewer uses while we test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Version == 0 || ch.Action != "set" || string(ch.After) != "50" {
		t.Fatalf("change recorded as %+v", ch)
	}
	r, _ = e.s.System("request.max_uses")
	if string(r.Value) != "50" || r.From != ScopeSystem || r.Row == nil || r.Row.Version != ch.Version ||
		r.Row.Actor != "claude-code" || r.Row.Reason != "fewer uses while we test" {
		t.Fatalf("reads as %+v", r)
	}

	// the history holds it, and the audit log and the chat were told
	h := e.s.History("request.max_uses", "", "", 0, 10)
	if len(h) != 1 || h[0].Version != ch.Version || h[0].Actor != "claude-code" {
		t.Fatalf("history: %+v", h)
	}
	if len(e.bot.texts) != 1 || !strings.Contains(e.bot.texts[0], "request.max_uses") {
		t.Fatalf("echo: %q", e.bot.texts)
	}
	entries, _, err := e.st.AuditQuery(store.AuditFilter{Action: "settings_set"})
	if err != nil || len(entries) != 1 || entries[0].Actor != "claude-code" || !strings.Contains(entries[0].Detail, "request.max_uses") {
		t.Fatalf("audit: %+v %v", entries, err)
	}

	// setting the same value again changes nothing
	same, err := e.s.Set(ctx, api, ScopeSystem, "", "request.max_uses", raw("50"), why, 0)
	if err != nil || !same.Unchanged {
		t.Fatalf("same value: %+v %v", same, err)
	}

	// revert it, and the default is in force again
	back, err := e.s.Revert(ctx, api, ch.Version, "the test moves on")
	if err != nil {
		t.Fatal(err)
	}
	// the set made the row, so undoing it takes the row away: the default answers again
	if back.Action != "revert" || back.Reverts != ch.Version || !absent(back.After) {
		t.Fatalf("revert: %+v", back)
	}
	r, _ = e.s.System("request.max_uses")
	if string(r.Value) != `"unlimited"` || r.From != "default" || r.Row != nil {
		t.Fatalf("after revert reads as %+v", r)
	}
	if got := e.s.History("", "", "", 0, 10); len(got) != 2 {
		t.Fatalf("history after revert: %+v", got)
	}
}

// A value that does not fit is refused and nothing changes; a key that is not one is refused with
// the words that say so; the scopes are checked, and the system scope takes no target.
func TestSetChecks(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		scope, target, key, value string
		want                      error
	}{
		{ScopeSystem, "", "request.max_uses", `"many"`, ErrInvalid},
		{ScopeSystem, "", "no.such.key", `1`, ErrUnknown},
		{ScopeSystem, "Infra", "request.max_uses", `5`, ErrBadInput},
		{ScopeCollection, "", "request.max_uses", `5`, ErrBadInput},
		{ScopeCollection, "Infra", "request.max_uses", `5`, nil},
		{ScopeCollection, "infra", "request.max_uses", `5`, nil},
		{ScopeTag, "env/prod", "request.max_uses", `5`, nil},
		{ScopeEntitlement, "oncall-dns", "request.max_uses", `5`, nil},
		{ScopeRequest, "r-1234", "request.max_uses", `5`, nil},
	} {
		_, err := e.s.Set(ctx, api, tc.scope, tc.target, tc.key, raw(tc.value), why, 0)
		if tc.want == nil {
			if err != nil {
				t.Errorf("%s %s %s: %v", tc.scope, tc.target, tc.key, err)
			}
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s %s %s: got %v, want %v", tc.scope, tc.target, tc.key, err, tc.want)
		}
	}
	if r, _ := e.s.System("request.max_uses"); r.From != "default" {
		t.Fatalf("a refused change changed something: %+v", r)
	}
}

// IfVersion refuses a change to a row that moved since the caller read it.
func TestSetConflict(t *testing.T) {
	e := newEnv(t)
	ch, err := e.s.Set(ctx, api, ScopeSystem, "", "retention.trash", raw(`"60d"`), why, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "retention.trash", raw(`"90d"`), why, ch.Version); err != nil {
		t.Fatalf("with the version last seen: %v", err)
	}
	_, err = e.s.Set(ctx, api, ScopeSystem, "", "retention.trash", raw(`"120d"`), why, ch.Version)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("with a stale version: %v", err)
	}
	// and the row did not move to the stale value
	if r, _ := e.s.System("retention.trash"); string(r.Value) != `"90d"` {
		t.Fatalf("row holds %s", r.Value)
	}
}

// Reset takes a row away, so the level above (or the default) gives the value again; reverting a
// reset puts the value back.
func TestResetAndRevertOfReset(t *testing.T) {
	e := newEnv(t)
	ch, err := e.s.Set(ctx, api, ScopeTag, "env/prod", "grant.alert_grace", raw(`"30m"`), why, 0)
	if err != nil {
		t.Fatal(err)
	}
	rst, err := e.s.Reset(ctx, api, ScopeTag, "env/prod", "grant.alert_grace", why, 0)
	if err != nil || rst.Action != "reset" || string(rst.Before) != `"30m"` {
		t.Fatalf("reset: %+v %v", rst, err)
	}
	if _, _, err := e.s.Value("grant.alert_grace", Selector{Tags: []string{"env/prod"}}); err != nil {
		t.Fatal(err)
	}
	v, levels, _ := e.s.Value("grant.alert_grace", Selector{Tags: []string{"env/prod"}})
	if v.From != "default" || len(levels) != 1 {
		t.Fatalf("after reset: %+v", levels)
	}
	// reverting the reset restores what it replaced
	back, err := e.s.Revert(ctx, api, rst.Version, why)
	if err != nil || string(back.After) != `"30m"` {
		t.Fatalf("revert of reset: %+v %v", back, err)
	}
	v, _, _ = e.s.Value("grant.alert_grace", Selector{Tags: []string{"env/prod"}})
	if string(v.Value) != `"30m"` || v.From != ScopeTag {
		t.Fatalf("after revert of reset: %+v", v)
	}
	_ = ch
}

// A change a later change touched cannot be reverted; the later one can.
func TestRevertConflict(t *testing.T) {
	e := newEnv(t)
	first, err := e.s.Set(ctx, api, ScopeSystem, "", "rate.per_agent_per_minute", raw("100"), why, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "rate.per_agent_per_minute", raw("200"), why, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Revert(ctx, api, first.Version, why); !errors.Is(err, ErrConflict) {
		t.Fatalf("reverting a changed-again row: %v", err)
	}
	h := e.s.History("", "", "", 0, 1)
	if _, err := e.s.Revert(ctx, api, h[0].Version, why); err != nil {
		t.Fatalf("reverting the latest: %v", err)
	}
	if r, _ := e.s.System("rate.per_agent_per_minute"); string(r.Value) != "100" {
		t.Fatalf("holds %s, want 100", r.Value)
	}
}

// The third invariant, on the settings' own rows: a row changed in the database itself — not
// through Beacon — fails its signature, is served as the last signed value (never a looser
// default) and is reported: logged, audited, echoed to the alert chat, and listed for the console.
func TestTamperedRowIsNothingAndIsHeard(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "request.max_uses", raw("20"), why, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Set(ctx, api, ScopeTag, "env/prod", "grant.on_policy_tightened", raw(`"notify"`), why, 0); err != nil {
		t.Fatal(err)
	}

	// someone with the database file widens the limit and softens the consequence, by hand
	db, err := sql.Open("sqlite", "file:"+e.db+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	widen := func(bucket, key string, change func(map[string]any)) {
		rows, err := db.Query(`SELECT k, v FROM kv WHERE bucket = ? AND k = ?`, bucket, []byte(key))
		if err != nil {
			t.Fatal(err)
		}
		var k, v []byte
		for rows.Next() {
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
		}
		rows.Close()
		if k == nil {
			t.Fatalf("no row for %s %q", bucket, key)
		}
		var m map[string]any
		if err := json.Unmarshal(v, &m); err != nil {
			t.Fatal(err)
		}
		change(m)
		b, _ := json.Marshal(m)
		if _, err := db.Exec(`UPDATE kv SET v = ? WHERE bucket = ? AND k = ?`, b, bucket, k); err != nil {
			t.Fatal(err)
		}
	}
	widen("settings", "system\x00\x00request.max_uses", func(m map[string]any) { m["value"] = "unlimited" })
	widen("settings", "tag\x00env/prod\x00grant.on_policy_tightened", func(m map[string]any) { m["value"] = "approve-not-a-word" })

	// the service reads again, as it does at start: both rows stay at the signed value
	heard := &bot{}
	s2, err := Open(Deps{Store: e.st, Bot: heard, Lang: i18n.EN})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := s2.System("request.max_uses"); string(r.Value) != "20" {
		t.Fatalf("tampered row fell open: %+v", r)
	}
	if v, _, _ := s2.Value("grant.on_policy_tightened", Selector{Tags: []string{"env/prod"}}); string(v.Value) != `"notify"` {
		t.Fatalf("tampered tag row fell open: %+v", v)
	}
	if bad := s2.Bad(); len(bad) != 2 {
		t.Fatalf("tampered rows not all reported: %v", bad)
	}
	if len(heard.texts) == 0 || !strings.Contains(heard.texts[0], "request.max_uses") {
		t.Fatalf("tampering not sent to the alert chat: %q", heard.texts)
	}
	entries, _, err := e.st.AuditQuery(store.AuditFilter{Action: "settings_signature_invalid"})
	if err != nil || len(entries) != 1 || !strings.Contains(entries[0].Detail, "2 row(s)") {
		t.Fatalf("tampering not audited: %+v %v", entries, err)
	}

	// the rows stay gone after another change elsewhere
	if _, err := s2.Set(ctx, api, ScopeSystem, "", "retention.trash", raw(`"45d"`), why, 0); err != nil {
		t.Fatal(err)
	}
	if r, _ := s2.System("request.max_uses"); string(r.Value) != "20" {
		t.Fatalf("tampered row came back loose: %+v", r)
	}
}

// A row whose signature was made with another key is nothing as well — the key is per database, so
// this is a row carried in from elsewhere.
func TestRowFromAnotherDatabaseIsNothing(t *testing.T) {
	e := newEnv(t)
	e2 := newEnv(t)
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "rate.per_agent_per_minute", raw("120"), why, 0); err != nil {
		t.Fatal(err)
	}
	// copy e's row into e2's database, bytes and all, straight into the kv table
	var row []byte
	_ = e.st.Storage().View(func(tx kv.Tx) error {
		row = tx.Get(bucketSettings, rowKeyBytes(ScopeSystem, "", "rate.per_agent_per_minute"))
		return nil
	})
	_ = e2.st.Storage().Update(func(tx kv.Tx) error {
		return tx.Put(bucketSettings, rowKeyBytes(ScopeSystem, "", "rate.per_agent_per_minute"), row)
	})
	s3, err := Open(Deps{Store: e2.st, Lang: i18n.EN})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := s3.System("rate.per_agent_per_minute"); r.From != "default" {
		t.Fatalf("a row from another database counts: %+v", r)
	}
	if len(s3.Bad()) != 1 {
		t.Fatalf("not reported: %v", s3.Bad())
	}
}

// The history lists newest first, narrowed by key, and a change can be fetched by its version.
func TestHistory(t *testing.T) {
	e := newEnv(t)
	var versions []int64
	for _, v := range []string{"10", "20", "30"} {
		ch, err := e.s.Set(ctx, api, ScopeSystem, "", "grant.expiry_notice", raw(fmt.Sprintf("%q", v+"m")), why, 0)
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, ch.Version)
	}
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "retention.trash", raw(`"45d"`), why, 0); err != nil {
		t.Fatal(err)
	}
	h := e.s.History("grant.expiry_notice", "", "", 0, 10)
	if len(h) != 3 || string(h[0].After) != `"30m"` || !absent(h[2].Before) {
		t.Fatalf("history: %+v", h)
	}
	h = e.s.History("", "", "", versions[1], 10)
	if len(h) != 1 || h[0].Version != versions[0] {
		t.Fatalf("history before a version: %+v", h)
	}
	if ch, ok := e.s.ChangeAt(versions[2]); !ok || string(ch.After) != `"30m"` {
		t.Fatalf("change at: %+v %v", ch, ok)
	}
}

// The clock the service runs on is the one it is given, for the rows' times.
func TestClock(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s, err := Open(Deps{Store: st, Lang: i18n.EN, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := s.Set(ctx, api, ScopeSystem, "", "retention.trash", raw(`"45d"`), why, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ch.Time.Equal(now) {
		t.Fatalf("change time %s, want %s", ch.Time, now)
	}
}

func TestDeletedRowStaysStrict(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "request.max_uses", raw("5"), why, 0); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+e.db+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`DELETE FROM kv WHERE bucket = ? AND k = ?`, "settings", []byte("system\x00\x00request.max_uses")); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(Deps{Store: e.st, Lang: i18n.EN})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := s2.System("request.max_uses")
	if string(r.Value) != "5" {
		t.Fatalf("deleted row fell open: %+v", r)
	}
	if len(s2.Bad()) == 0 {
		t.Fatal("the missing row was not reported")
	}
}

func TestHistoryTamperCannotRevert(t *testing.T) {
	e := newEnv(t)
	ch, err := e.s.Set(ctx, api, ScopeSystem, "", "request.max_uses", raw("5"), why, 0)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+e.db+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var k, v []byte
	rows, err := db.Query(`SELECT k, v FROM kv WHERE bucket = ?`, "settings_changes")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
	}
	rows.Close()
	var m map[string]any
	if err := json.Unmarshal(v, &m); err != nil {
		t.Fatal(err)
	}
	m["before"] = json.RawMessage("1000000")
	b, _ := json.Marshal(m)
	if _, err := db.Exec(`UPDATE kv SET v = ? WHERE bucket = ? AND k = ?`, b, "settings_changes", k); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Revert(ctx, api, ch.Version, why); err == nil {
		t.Fatal("a tampered history row should not revert")
	}
}

func TestSetKeyMigratesOnce(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "request.max_uses", raw("5"), why, 0); err != nil {
		t.Fatal(err)
	}
	newKey := make([]byte, 32)
	for i := range newKey {
		newKey[i] = byte(i + 1)
	}
	if err := e.s.SetKey(newKey); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.s.System("request.max_uses"); string(r.Value) != "5" {
		t.Fatalf("after SetKey: %+v", r)
	}
	s2, err := Open(Deps{Store: e.st, Lang: i18n.EN})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Set(ctx, api, ScopeSystem, "", "request.max_uses", raw("9"), why, 0); err == nil {
		t.Fatal("Set before SetKey should fail")
	}
	if err := s2.SetKey(newKey); err != nil {
		t.Fatal(err)
	}
	if r, _ := s2.System("request.max_uses"); string(r.Value) != "5" {
		t.Fatalf("after Open+SetKey: %+v", r)
	}
	forged, err := e.st.Secret("settings")
	if err != nil || len(forged) == 0 {
		t.Fatal(err)
	}
	fake := Row{Key: "request.max_uses", Scope: ScopeSystem, Value: raw(`"unlimited"`), Version: 99, Actor: "attacker", Via: "api", At: time.Now().UTC()}
	fake.Sig = sign(macKey(forged), fake)
	if err := e.st.Storage().Update(func(tx kv.Tx) error { return putRow(tx, fake) }); err != nil {
		t.Fatal(err)
	}
	s3, err := Open(Deps{Store: e.st, Lang: i18n.EN})
	if err != nil {
		t.Fatal(err)
	}
	if err := s3.SetKey(newKey); err != nil {
		t.Fatal(err)
	}
	if r, _ := s3.System("request.max_uses"); string(r.Value) != "5" {
		t.Fatalf("forged with the store secret: %+v", r)
	}
	if err := s3.SetKey(newKey); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalsRequiredExpands(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "review.approvals_required", raw("2"), why, 0); err != nil {
		t.Fatal(err)
	}
	r, _ := e.s.System("review.approvals_required")
	if string(jsonAt(r.Value, "review")) != "2" || string(jsonAt(r.Value, "person")) != "2" {
		t.Fatalf("expanded: %s", r.Value)
	}
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "review.approvals_required", raw(`{"auto":0,"review":0,"person":0}`), why, 0); err == nil {
		t.Fatal("zero approvals should be refused")
	}
	if _, err := e.s.Set(ctx, api, ScopeTag, "env/prod", "review.approvals_required", raw("3"), why, 0); err != nil {
		t.Fatal(err)
	}
	v, _, _ := e.s.Value("review.approvals_required", Selector{Tags: []string{"env/prod"}})
	if string(jsonAt(v.Value, "person")) != "3" {
		t.Fatalf("more approvals is stricter: %s", v.Value)
	}
}

func TestReasonIsRequired(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "request.max_uses", raw("5"), "short", 0); err == nil {
		t.Fatal("a short reason should be refused")
	}
}

func TestRequestCannotSetTheChain(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.Set(ctx, api, ScopeRequest, "r-1", "review.chain", raw(`["rules"]`), why, 0); err == nil {
		t.Fatal("a request must not drop people from the chain")
	}
}

func TestReplayOldRowStaysAtHead(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "request.max_uses", raw("5"), why, 0); err != nil {
		t.Fatal(err)
	}
	var first []byte
	_ = e.st.Storage().View(func(tx kv.Tx) error {
		first = append([]byte(nil), tx.Get(bucketSettings, rowKeyBytes(ScopeSystem, "", "request.max_uses"))...)
		return nil
	})
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "request.max_uses", raw("20"), why, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Storage().Update(func(tx kv.Tx) error {
		return tx.Put(bucketSettings, rowKeyBytes(ScopeSystem, "", "request.max_uses"), first)
	}); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(Deps{Store: e.st, Lang: i18n.EN})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := s2.System("request.max_uses"); string(r.Value) != "20" {
		t.Fatalf("replayed old row fell open: %+v", r)
	}
}

func TestMissingHeadFailsClosed(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "request.max_uses", raw("5"), why, 0); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+e.db+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`DELETE FROM kv WHERE bucket = ? AND k = ?`, "settings", []byte("\x00head")); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(Deps{Store: e.st, Lang: i18n.EN})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := s2.System("request.max_uses"); string(r.Value) != "1" {
		t.Fatalf("missing head fell open: %+v", r)
	}
}

func TestLeaseExpiresLongerThanRenew(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "storage.lease.renew", raw(`"20s"`), why, 0); err == nil {
		t.Fatal("renew must stay shorter than expires")
	}
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "storage.lease.expires", raw(`"3s"`), why, 0); err == nil {
		t.Fatal("expires must stay longer than renew")
	}
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "storage.lease.expires", raw(`"30s"`), why, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "storage.lease.renew", raw(`"10s"`), why, 0); err != nil {
		t.Fatal(err)
	}
}

func TestDurationUnlimitedIsNotZero(t *testing.T) {
	e := newEnv(t)
	if d, ok := e.s.Duration("request.max_duration", Selector{}); ok || d != 0 {
		t.Fatalf("unlimited must not be a zero duration: %d %v", d, ok)
	}
	if _, err := e.s.Set(ctx, api, ScopeSystem, "", "request.pending_ttl", raw(`"48h"`), why, 0); err != nil {
		t.Fatal(err)
	}
	d, ok := e.s.Duration("request.pending_ttl", Selector{})
	if !ok || d != int64(48*time.Hour) {
		t.Fatalf("finite duration: %d %v", d, ok)
	}
}

func TestRetentionAndImportStayAtSystem(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.Set(ctx, api, ScopeCollection, "infra", "retention.trash", raw(`"7d"`), why, 0); err == nil {
		t.Fatal("retention is system-only")
	}
	if _, err := e.s.Set(ctx, api, ScopeTag, "env/prod", "backup.retries", raw("1"), why, 0); err == nil {
		t.Fatal("backup is system-only")
	}
}
