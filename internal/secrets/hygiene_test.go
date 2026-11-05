package secrets

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// The hygiene's tests: the rotation notices on a settable clock, the server-side rotation that an
// agent never sees, the password health judgements (weak, repeated, old, leaked), and the idle
// grants' three consequences. The fixtures are made up and marked, as the vault's own tests are,
// so the leak check can grep the output for them; the weak-by-the-rules values the pure-function
// table test uses are worn-out words nobody stores.

const (
	fixtureStrong    = "fixture-value-strong-quokka-42"
	fixtureReused    = "fixture-value-duplicate-1a-cougar"
	fixtureHIBPPwned = "fixture-value-hibp-pwned-77"
)

// TestRotationNoticeOnTime is the acceptance test for the rotation notices: an item with a
// rotation period is announced when its period ends inside the notice window (the same window the
// grants' expiry notices take), once per version, to the chat, the audit log and the agents
// holding a live grant — and never for an item without a period.
func TestRotationNoticeOnTime(t *testing.T) {
	g := newGrantsVault(t)
	infra, _, _, token, _, _ := g.standardShape(t)
	worker := g.newAgent(t, "worker", "own")
	g.ent(t, oncallEnt(ActDescribe, ActRead, ActUse))
	// the auto-level item carries a 30-day period; the agent holds a live use grant over it, so its hook hears
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: infra.Name, Level: LevelAuto, RotateDays: 30,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Purpose: PurposePassword, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.RequestSecret(worker, "test", RequestIn{Items: []string{RefOfItem(infra.Name, token.Title)}, Action: ActUse, Reason: "the on-call work needs it"}); err != nil {
		t.Fatal(err)
	}
	g.hooks.waitHooks(t, 1) // the grant's own event went out
	g.hooks.clear()
	g.bot.clear()

	// day 20 of 30: outside the 3-day window, nothing is said
	g.clk.Add(20 * 24 * time.Hour)
	if n := g.SweepHygiene(); n != 0 {
		t.Fatalf("day 20: the sweep changed %d things, want 0", n)
	}
	if rows := g.RotationDue(); len(rows) != 0 {
		t.Fatalf("day 20: %d rotation rows, want 0", len(rows))
	}

	// day 28: inside the window, the notice goes out once
	g.clk.Add(8 * 24 * time.Hour)
	if n := g.SweepHygiene(); n != 1 {
		t.Fatalf("day 28: the sweep changed %d things, want 1", n)
	}
	entries, total, _ := g.st.AuditQuery(store.AuditFilter{Action: "secrets_item_rotate_due", Limit: 10})
	if total != 1 {
		t.Fatalf("%d rotate-due audit entries, want 1", total)
	}
	if !strings.Contains(entries[0].Detail, "ends in 2 day(s)") {
		t.Errorf("the audit detail says %q, want the two days left", entries[0].Detail)
	}
	if got := g.bot.all(); len(got) != 1 || !strings.Contains(got[0], token.Title) {
		t.Fatalf("the chat heard %v, want one notice naming the item", got)
	}
	events := g.hooks.waitHooks(t, 1)
	if len(events) != 1 || !strings.Contains(events[0], "secret.rotate_due") {
		t.Fatalf("the hooks got %v, want one secret.rotate_due", events)
	}
	// the rotation list shows it due soon
	rows := g.RotationDue()
	if len(rows) != 1 || rows[0].State != RotateDueSoon || rows[0].Days != 2 {
		t.Fatalf("rotation rows %+v, want one due_soon two days out", rows)
	}

	// the next sweeps say nothing more for the same version
	g.clk.Add(time.Hour)
	if n := g.SweepHygiene(); n != 0 {
		t.Fatalf("a second sweep changed %d things, want 0", n)
	}
	// past the end it is overdue, and still said only once
	g.clk.Add(3 * 24 * time.Hour)
	if rows := g.RotationDue(); len(rows) != 1 || rows[0].State != RotateOverdue {
		t.Fatalf("rotation rows %+v, want one overdue", rows)
	}
	if n := g.SweepHygiene(); n != 0 {
		t.Fatalf("an overdue item was announced again: %d", n)
	}

	// a new version resets the clock: 28 days later the notice goes out again
	g.clk.Add(25 * 24 * time.Hour)
	if _, err := g.RotateItem(worker, "test", RefOfItem(infra.Name, token.Title), "the period ended; turning it", GenSpec{Length: 24, Charset: "alnum"}); err != nil {
		t.Fatalf("the agent could not turn the item: %v", err)
	}
	g.clk.Add(28 * 24 * time.Hour)
	if n := g.SweepHygiene(); n != 1 {
		t.Fatalf("after a new version, the sweep changed %d things, want 1", n)
	}

	// an item without a period is never announced
	g.clk.Add(30 * 24 * time.Hour)
	if n := g.SweepHygiene(); n != 0 {
		t.Fatalf("an item without a period was announced: %d", n)
	}
}

// TestRotateItemServerSide is the acceptance test for the server-side rotation: the new value is
// made on the server and stored as the next version; an agent holding only a use grant never sees
// it (read is refused), while the use grant stays live over the item — the path the use actions
// take. An agent with a read grant does see the new value, which proves it was stored.
func TestRotateItemServerSide(t *testing.T) {
	g := newGrantsVault(t)
	infra, _, _, token, _, _ := g.standardShape(t)
	worker := g.newAgent(t, "worker", "own")
	reader := g.newAgent(t, "reader", "own")
	// worker may only use; reader may read. token is auto-level so the grants land at once.
	g.ent(t, Entitlement{Name: "use-only", Principals: Principals{Agents: []string{"worker"}},
		Resources: Resources{Collections: []string{infra.Name}}, Actions: []string{ActUse}})
	g.ent(t, Entitlement{Name: "read-only", Principals: Principals{Agents: []string{"reader"}},
		Resources: Resources{Collections: []string{infra.Name}}, Actions: []string{ActRead}})
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: infra.Name, Level: LevelAuto}); err != nil {
		t.Fatal(err)
	}
	token, _, _ = g.Item(token.ID)
	ref := RefOfItem(infra.Name, token.Title)
	for _, a := range []string{worker, reader} {
		if _, err := g.RequestSecret(a, "test", RequestIn{Items: []string{ref},
			Action: map[string]string{worker: ActUse, reader: ActRead}[a], Reason: "the on-call work needs it"}); err != nil {
			t.Fatal(err)
		}
	}

	// an agent without any grant is refused
	if _, err := g.RotateItem("nobody", "test", ref, "turning it", GenSpec{}); err == nil {
		t.Fatal("an agent with no grant turned the item")
	}
	// a read grant is not enough: the point is that the value never reaches the agent
	if _, err := g.RotateItem(reader, "test", ref, "turning it", GenSpec{}); err == nil || !strings.Contains(err.Error(), "use grant") {
		t.Fatalf("a read-only agent turned the item: %v", err)
	}

	// the use agent turns it: the answer is the version number, never the value
	out, err := g.RotateItem(worker, "test", ref, "the period ended; turning it", GenSpec{Length: 32, Charset: "alnum"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Version != token.Version+1 {
		t.Fatalf("the answer's version is %d, want %d", out.Version, token.Version+1)
	}
	if _, total, err := g.st.AuditQuery(store.AuditFilter{Action: "secrets_item_rotate", Limit: 10}); err != nil || total != 1 {
		t.Fatalf("%d rotate audit entries, want 1 (%v)", total, err)
	}

	// the use agent cannot read the value it just turned
	if _, err := g.GetSecret(worker, "test", ref, "", "checking", "", "", "", "test"); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("the use agent read the value: %v", err)
	}
	// its use grant is still live over the item: the use path stands ready
	if _, ok := g.activeGrant(worker, out.ID, "", ActUse); !ok {
		t.Fatal("the use grant is not live after the rotation")
	}
	// the reader sees the new value: 32 characters, not the old one
	got, err := g.GetSecret(reader, "test", ref, "", "checking", "", "", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Fields) != 1 || len(got.Fields[0].Value) != 32 || got.Fields[0].Value == fixtureToken {
		t.Fatalf("the reader did not see the turned value (fields %+v)", got.Fields)
	}
}

// TestWeakPassword is the local weak judgement as a table: too short, in the common list, wearing
// the item's title or user name — and none of these for a real one.
func TestWeakPassword(t *testing.T) {
	for _, tc := range []struct {
		value, username, title string
		min                    int64
		want                   bool
	}{
		{"qwerty", "", "", 8, true},                             // in the common list
		{"password1", "", "", 8, true},                          // in the common list
		{"short", "", "", 8, true},                              // too short
		{"a-long-one-indeed", "", "", 8, false},                 // none of the rules
		{"Tumbler Quokka", "", "tumbler quokka", 8, true},       // wears the title
		{"a-long-one-indeed", "a-long-one-indeed", "", 8, true}, // wears the user name
		{"a-long-one-indeed", "someone-else", "", 8, false},
	} {
		if got := WeakPassword(tc.value, tc.username, tc.title, tc.min); got != tc.want {
			t.Errorf("WeakPassword(%q, %q, %q, %d) = %v, want %v", tc.value, tc.username, tc.title, tc.min, got, tc.want)
		}
	}
}

// TestHealthReport judges weak, repeated and old passwords on one shape, with the leak corpus off:
// nothing leaves the server, nothing leaks into the findings but references and judgements.
func TestHealthReport(t *testing.T) {
	g := newGrantsVault(t)
	infra := newGrantsVaultShape(t, g)

	// three items: one strong and fresh, one wearing its own title (weak), and two sharing a value
	// (repeated); the clock then passes the age the settings allow
	fresh := g.makeItemWithValue(t, infra, "Fresh service", fixtureStrong)
	titled := g.makeItemWithValue(t, infra, "Worn service", "worn service")
	dupA := g.makeItemWithValue(t, infra, "Duplicate one", fixtureReused)
	dupB := g.makeItemWithValue(t, infra, "Duplicate two", fixtureReused)
	_ = fresh
	_ = titled

	// before the age passes: weak and repeated, no old
	report, err := g.HealthReport(g.owner)
	if err != nil {
		t.Fatal(err)
	}
	if report.HIBP {
		t.Error("the report says the leak corpus was asked, but the setting is off")
	}
	got := map[string]HealthFinding{}
	for _, f := range report.Findings {
		got[f.Title] = f
	}
	if f, ok := got["Worn service"]; !ok || !containsString(f.Kinds, KindWeak) {
		t.Errorf("the title-wearing password was not judged weak: %+v", f)
	}
	if f, ok := got["Duplicate one"]; !ok || !containsString(f.Kinds, KindRepeated) || f.Shares != 2 {
		t.Errorf("the shared password was not judged repeated by two: %+v", f)
	}
	if f, ok := got["Duplicate two"]; !ok || !containsString(f.Kinds, KindRepeated) {
		t.Errorf("the second sharer was not judged repeated: %+v", f)
	}
	if _, ok := got["Fresh service"]; ok {
		t.Error("the strong, fresh password has findings")
	}
	if report.Passwords != 4 {
		t.Errorf("%d passwords judged, want 4", report.Passwords)
	}
	// no finding carries a value
	for _, f := range report.Findings {
		if strings.Contains(f.Ref, fixtureStrong) || strings.Contains(f.Ref, fixtureReused) {
			t.Errorf("a finding carries a value: %+v", f)
		}
	}

	// past the age: everything is old as well, and the age shows
	g.clk.Add(91 * 24 * time.Hour)
	report, err = g.HealthReport(g.owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 4 {
		t.Fatalf("%d findings after the age passed, want every password", len(report.Findings))
	}
	for _, f := range report.Findings {
		if !containsString(f.Kinds, KindOld) {
			t.Errorf("%s is not judged old: %+v", f.Title, f)
		}
		if f.AgeDays < 91 {
			t.Errorf("%s shows %d days of age, want at least 91", f.Title, f.AgeDays)
		}
	}
	_ = dupA
	_ = dupB

	// a viewer without read over the collection sees nothing of its values
	report, err = g.HealthReport(g.viewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 0 || report.Passwords != 0 {
		t.Errorf("the viewer saw the health of values they may not read: %+v", report)
	}
}

// newGrantsVaultShape makes one collection the owner may read and a viewer may not.
func newGrantsVaultShape(t *testing.T, g *grantsVault) Collection {
	t.Helper()
	infra, err := g.CreateCollection(g.owner, "Infra", "", LevelReview, nil)
	if err != nil {
		t.Fatal(err)
	}
	return infra
}

// makeItemWithValue makes an item with one password field of the value given.
func (g *grantsVault) makeItemWithValue(t *testing.T, col Collection, title, value string) Item {
	t.Helper()
	item, err := g.SetItem(g.owner, ItemIn{Collection: col.Name, Title: title,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Purpose: PurposePassword, Value: value}}})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// TestHIBPRangeCount is the k-anonymity query itself: the range asked for is the first five
// characters of the hash and nothing more, and the count comes back for the suffix that stayed
// home.
func TestHIBPRangeCount(t *testing.T) {
	value := fixtureHIBPPwned
	sum := sha1.Sum([]byte(value))
	hexed := strings.ToUpper(hex.EncodeToString(sum[:]))
	prefix, suffix := hexed[:5], hexed[5:]
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = true
		if strings.HasSuffix(r.URL.Path, "/"+prefix) {
			fmt.Fprintf(w, "%s:%d\n%s:%d\n", "00000000000000000000000000000000000", 3, suffix, 217)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n, err := HIBPRangeCount(context.Background(), srv.Client(), srv.URL, value)
	if err != nil {
		t.Fatal(err)
	}
	if n != 217 {
		t.Errorf("the corpus answered %d, want 217", n)
	}
	if !seen["/range/"+prefix] {
		t.Errorf("the ranges asked for were %v, want /range/%s", seen, prefix)
	}
	for path := range seen {
		if strings.Contains(path, suffix) || strings.Contains(path, value) {
			t.Errorf("a request carried more than the five-character prefix: %q", path)
		}
	}
	// a value whose suffix is not in the range answers zero
	n, err = HIBPRangeCount(context.Background(), srv.Client(), srv.URL, fixtureStrong)
	if err != nil || n != 0 {
		t.Errorf("an absent value answered %d (%v), want 0", n, err)
	}
	// a corpus that does not answer is an error, never a finding
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	if _, err := HIBPRangeCount(context.Background(), dead.Client(), dead.URL, value); err == nil {
		t.Error("a failing corpus answered without an error")
	}
}

// TestHealthReportHIBP is the leak corpus in the report: off by default (nothing asked), on when
// the setting says so, and then only the value in the corpus is judged leaked. The corpus is a
// local server standing in for the public interface.
func TestHealthReportHIBP(t *testing.T) {
	g := newGrantsVault(t)
	infra := newGrantsVaultShape(t, g)
	g.makeItemWithValue(t, infra, "Pwned service", fixtureHIBPPwned)
	g.makeItemWithValue(t, infra, "Clean service", fixtureStrong)

	sum := sha1.Sum([]byte(fixtureHIBPPwned))
	suffix := strings.ToUpper(hex.EncodeToString(sum[:]))[5:]
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		fmt.Fprintf(w, "%s:%d\n", suffix, 9)
	}))
	defer srv.Close()
	g.d.HTTP = srv.Client()
	hibpBaseURL = srv.URL
	defer func() { hibpBaseURL = "https://api.pwnedpasswords.com" }()

	// off (the default): the report stands entirely local
	report, err := g.HealthReport(g.owner)
	if err != nil {
		t.Fatal(err)
	}
	if report.HIBP || asked != 0 {
		t.Fatalf("the corpus was asked with the setting off (hibp=%v, asked=%d)", report.HIBP, asked)
	}
	for _, f := range report.Findings {
		if containsString(f.Kinds, KindLeaked) {
			t.Errorf("a leaked finding with the corpus off: %+v", f)
		}
	}

	// on: the pwned value is judged leaked, the clean one is not
	g.setSetting(t, "hygiene.hibp_check", true)
	report, err = g.HealthReport(g.owner)
	if err != nil {
		t.Fatal(err)
	}
	if !report.HIBP || asked == 0 {
		t.Fatalf("the corpus was not asked with the setting on (asked=%d)", asked)
	}
	leaked := 0
	for _, f := range report.Findings {
		if containsString(f.Kinds, KindLeaked) {
			leaked++
			if f.Title != "Pwned service" {
				t.Errorf("%s was judged leaked, want only the pwned service", f.Title)
			}
		}
	}
	if leaked != 1 {
		t.Fatalf("%d leaked findings, want 1", leaked)
	}
}

// TestUnusedGrantConsequences walks the three consequences of grant.on_unused on a settable clock:
// marked (the default — nothing happens, the console lists the idle grant), the owner told (once),
// and released (the grant ends, the agent is told, the value is refused afterwards). A grant that
// has been used is not idle; one that goes idle again after a use is acted on again.
func TestUnusedGrantConsequences(t *testing.T) {
	// ---- mark, the default
	g := newGrantsVault(t)
	g.setSetting(t, "grant.unused_after", "2h")
	infra, _, _, token, _, _ := g.standardShape(t)
	worker := g.newAgent(t, "worker", "own")
	g.ent(t, oncallEnt())
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: infra.Name, Level: LevelAuto}); err != nil {
		t.Fatal(err)
	}
	ref := RefOfItem(infra.Name, token.Title)
	out, err := g.RequestSecret(worker, "test", RequestIn{Items: []string{ref}, Action: ActRead, Reason: "the on-call work needs it"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("the grant did not come: %v %+v", err, out)
	}
	g.bot.clear()
	g.clk.Add(3 * time.Hour)
	if rows := g.UnusedGrants(); len(rows) != 1 || rows[0].Agent != worker {
		t.Fatalf("the idle grants are %+v, want worker's", rows)
	}
	if n := g.SweepHygiene(); n != 0 {
		t.Fatalf("mark changed %d things, want nothing", n)
	}
	if grants := g.GrantsOf(worker); len(grants) != 1 || grants[0].Status != GrantActive {
		t.Fatalf("mark must leave the grant standing: %+v", grants)
	}
	if msgs := g.bot.all(); len(msgs) != 0 {
		t.Fatalf("mark said something to the chat: %v", msgs)
	}

	// a use makes the grant busy: it is not idle, and its last use is recorded
	if _, err := g.GetSecret(worker, "test", ref, "", "the on-call work needs it", "", "", "", "test"); err != nil {
		t.Fatal(err)
	}
	grants := g.GrantsOf(worker)
	if grants[0].LastUsed.IsZero() {
		t.Fatal("the use did not record when the grant last moved a value")
	}
	if rows := g.UnusedGrants(); len(rows) != 0 {
		t.Fatalf("a just-used grant is idle: %+v", rows)
	}

	// ---- notify: said once, the grant stands
	g2 := newGrantsVault(t)
	g2.setSetting(t, "grant.unused_after", "2h")
	g2.setSetting(t, "grant.on_unused", "notify")
	infra2, _, _, token2, _, _ := g2.standardShape(t)
	worker2 := g2.newAgent(t, "worker", "own")
	g2.ent(t, oncallEnt())
	if _, err := g2.SetItem(g2.owner, ItemIn{ID: token2.ID, Collection: infra2.Name, Level: LevelAuto}); err != nil {
		t.Fatal(err)
	}
	ref2 := RefOfItem(infra2.Name, token2.Title)
	if _, err := g2.RequestSecret(worker2, "test", RequestIn{Items: []string{ref2}, Action: ActRead, Reason: "the on-call work needs it"}); err != nil {
		t.Fatal(err)
	}
	g2.bot.clear()
	g2.clk.Add(3 * time.Hour)
	if n := g2.SweepHygiene(); n != 1 {
		t.Fatalf("notify changed %d things, want 1", n)
	}
	if msgs := g2.bot.all(); len(msgs) != 1 || !strings.Contains(msgs[0], worker2) {
		t.Fatalf("the chat heard %v, want one notice naming the agent", msgs)
	}
	if _, total, _ := g2.st.AuditQuery(store.AuditFilter{Action: "secrets_grant_unused", Limit: 10}); total != 1 {
		t.Fatalf("%d unused audit entries, want 1", total)
	}
	if grants := g2.GrantsOf(worker2); len(grants) != 1 || grants[0].Status != GrantActive {
		t.Fatalf("notify must leave the grant standing: %+v", grants)
	}
	g2.clk.Add(time.Hour)
	if n := g2.SweepHygiene(); n != 0 {
		t.Fatalf("notify said it again: %d", n)
	}

	// ---- auto_release: the grant ends, the agent hears, the value is refused
	g2.setSetting(t, "grant.on_unused", "auto_release")
	g2.clk.Add(2 * time.Hour) // idle again since the notice (never used)
	g2.hooks.clear()
	if n := g2.SweepHygiene(); n != 1 {
		t.Fatalf("auto_release changed %d things, want 1", n)
	}
	grants = g2.GrantsOf(worker2)
	if len(grants) != 0 {
		t.Fatalf("the idle grant still named after auto_release: %+v", grants)
	}
	events := g2.hooks.waitHooks(t, 1)
	if len(events) != 1 || !strings.Contains(events[0], "secret.revoked") {
		t.Fatalf("the hooks got %v, want one secret.revoked", events)
	}
	if _, err := g2.GetSecret(worker2, "test", ref2, "", "checking", "", "", "", "test"); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("the released grant still took a value: %v", err)
	}
}
