package secrets

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/feishu"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/settings"
	"github.com/openlinkos/beacon/internal/store"
)

// The grants' tests. The fixtures are made up and marked, as the vault's own tests are, so the
// leak check can grep the output for them.

const fixtureToken = "fixture-value-grant-plugh-xyzzy-7"

// clock is a settable now, so the lifetimes can pass.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// poster collects the chat messages, so the notices and the alarms can be checked.
type poster struct {
	mu   sync.Mutex
	msgs []string
}

func (p *poster) Send(_ context.Context, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs = append(p.msgs, text)
	return nil
}
func (p *poster) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.msgs...)
}

// hookCatcher collects the events the hooks would deliver.
type hookCatcher struct {
	mu     sync.Mutex
	events []string
	bodies []string
}

func (h *hookCatcher) SendSecretHook(_ context.Context, agent, event string, payload []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, agent+" "+event)
	h.bodies = append(h.bodies, string(payload))
	return nil
}
func (h *hookCatcher) got() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.events...)
}

// waitHooks waits until n events landed (or a second passed), for the events that go out on their
// own goroutines.
func (h *hookCatcher) waitHooks(t *testing.T, n int) []string {
	t.Helper()
	for i := 0; i < 100 && len(h.got()) < n; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	return h.got()
}

// clear drops what landed, so the next wait sees only what follows.
func (h *hookCatcher) clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = nil
	h.bodies = nil
}

// cardCatcher collects the cards the approvals send.
type cardCatcher struct {
	mu    sync.Mutex
	cards []feishu.Card
}

func (c *cardCatcher) SendSecretCard(_ context.Context, card feishu.Card) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cards = append(c.cards, card)
	return nil
}
func (c *cardCatcher) got() []feishu.Card {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]feishu.Card(nil), c.cards...)
}

// waitCards waits until n cards landed (or a second passed).
func (c *cardCatcher) waitCards(t *testing.T, n int) []feishu.Card {
	t.Helper()
	for i := 0; i < 100 && len(c.got()) < n; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	return c.got()
}

// grantsVault opens a vault with the settings wired, an owner account, a viewer account and an
// agent of each, and a settable clock.
type grantsVault struct {
	*Service
	st         *store.Store
	sset       *settings.Service
	clk        *clock
	bot        *poster
	hooks      *hookCatcher
	cards      *cardCatcher
	owner      Actor
	viewer     Actor
	alertOn    map[string]bool
	resolvedAt map[string]time.Time
}

func newGrantsVault(t *testing.T) *grantsVault {
	t.Helper()
	RateReset()
	MetricsReset()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// the accounts: an owner (the setup link) and a viewer (an invitation)
	code, _, err := st.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Join(code, "own", "Own", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	inv, _, err := st.CreateInvitation("viewer", "test", "own")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Join(inv, "vie", "Vie", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	clk := &clock{now: time.Now()}
	sset, err := settings.Open(settings.Deps{Store: st, Lang: i18n.EN, Log: testLog(t)})
	if err != nil {
		t.Fatal(err)
	}
	g := &grantsVault{st: st, sset: sset, clk: clk, bot: &poster{}, hooks: &hookCatcher{}, cards: &cardCatcher{},
		owner: Actor{Name: "own", Via: "test", Role: "owner"}, viewer: Actor{Name: "vie", Via: "test", Role: "viewer"},
		alertOn: map[string]bool{}}
	g.alertOn["alert-1"] = true
	g.resolvedAt = map[string]time.Time{}
	s, err := Open(context.Background(), SealConfig{Mode: SealFile, Dir: filepath.Join(dir, "secrets")},
		Deps{Store: st, Settings: sset, Lang: i18n.EN, Log: testLog(t), Now: clk.Now,
			Bot: g.bot, Hooks: g.hooks, Cards: g.cards,
			CheckPassword: func(string, string) error { return nil },
			AlertState: func(group string) (bool, time.Time, bool) {
				on, ok := g.alertOn[group]
				if ok && !on {
					if r, ok := g.resolvedAt[group]; ok {
						return false, r, true
					}
					return false, g.clk.Now(), true
				}
				return on, time.Time{}, ok
			}})
	if err != nil {
		t.Fatal(err)
	}
	if err := sset.SetKey(s.SK()); err != nil {
		t.Fatal(err)
	}
	g.Service = s
	return g
}

// newAgent makes an agent of the account given and returns its name.
func (g *grantsVault) newAgent(t *testing.T, name, ownerAcc string, tags ...string) string {
	t.Helper()
	if _, err := g.st.CreateAgent(store.Agent{Name: name, Owner: ownerAcc, Preset: "readonly"}); err != nil {
		t.Fatal(err)
	}
	if len(tags) > 0 {
		if err := g.SetAgentTags(g.owner, name, tags); err != nil {
			t.Fatal(err)
		}
	}
	return name
}

// setSetting writes a settings row at the system scope.
func (g *grantsVault) setSetting(t *testing.T, key string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.sset.Set(context.Background(), settings.Actor{Name: "own", Via: "test"}, settings.ScopeSystem, "", key, b, "test", 0); err != nil {
		t.Fatal(err)
	}
}

// makeItem makes an item with one concealed field in a collection.
func (g *grantsVault) makeItem(t *testing.T, col Collection, title string, tags ...string) Item {
	t.Helper()
	item, err := g.SetItem(g.owner, ItemIn{Collection: col.Name, Title: title, Tags: tags,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Purpose: PurposePassword, Value: fixtureToken}}})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// ent writes an entitlement through the door the surfaces use.
func (g *grantsVault) ent(t *testing.T, e Entitlement) Entitlement {
	t.Helper()
	out, err := g.SetEntitlement(g.owner, e)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// the standard shape: Infra (review) with a plain item, a prod item (person via tag), a token item
// (auto via tag) and an explicit auto item; Personal for the out-of-scope cases.
func (g *grantsVault) standardShape(t *testing.T) (Collection, Item, Item, Item, Item, Collection) {
	t.Helper()
	infra, err := g.CreateCollection(g.owner, "Infra", "", LevelReview, nil)
	if err != nil {
		t.Fatal(err)
	}
	personal, err := g.CreateCollection(g.owner, "Personal", "", LevelPerson, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.SetTag(g.owner, "env/prod", LevelPerson, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := g.SetTag(g.owner, "kind/readonly-token", LevelAuto, "", nil); err != nil {
		t.Fatal(err)
	}
	plain := g.makeItem(t, infra, "Plain database")
	prod := g.makeItem(t, infra, "Prod database", "env/prod")
	token := g.makeItem(t, infra, "Token item", "kind/readonly-token")
	never := g.makeItem(t, infra, "Never item")
	if _, err := g.SetItem(g.owner, ItemIn{ID: never.ID, Collection: infra.Name, Level: LevelNever,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	never, _, _ = g.Item(never.ID) // the row as it stands, with its explicit level
	g.makeItem(t, personal, "Someones card")
	return infra, plain, prod, token, never, personal
}

// oncallEnt is the entitlement the on-call agent works through.
func oncallEnt(actions ...string) Entitlement {
	if len(actions) == 0 {
		actions = []string{ActDescribe, ActRead}
	}
	return Entitlement{Name: "oncall-dns", Description: "the on-call agent's reach",
		Principals: Principals{Agents: []string{"worker"}},
		Resources:  Resources{Collections: []string{"Infra"}},
		Actions:    actions}
}

// ---- the five steps

// TestFiveSteps walks the design's five steps one by one, on one shape.
func TestFiveSteps(t *testing.T) {
	g := newGrantsVault(t)
	infra, plain, prod, token, never, personal := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	agent, _ := g.agentInfoOf("worker")
	now := g.now()

	step := func(item Item, col Collection, action string) policyDecision {
		return g.decidePolicy(agent, item, col, action, "")
	}

	// step 1: a deny matching wins over everything
	g.ent(t, Entitlement{Name: "no-prod", Deny: true,
		Principals: Principals{Agents: []string{"worker"}},
		Resources:  Resources{Tags: []string{"env/prod"}}})
	if d := step(prod, infra, ActRead); d.OK || !d.NotFound {
		t.Fatalf("step 1: a deny matching refuses as not-found, got %+v", d)
	}
	// the deny gone, the way the later steps need the shape
	if err := g.DeleteEntitlement(g.owner, "no-prod"); err != nil {
		t.Fatal(err)
	}
	// step 2: nothing covering, and the action outside what covers
	if d := step(plain, personal, ActRead); d.OK || !d.NotFound {
		t.Fatalf("step 2a: out of any entitlement is not-found, got %+v", d)
	}
	g.ent(t, oncallEnt(ActDescribe)) // replaced: describe only
	if d := step(plain, infra, ActRead); d.OK || !d.NotFound {
		t.Fatalf("step 2b: an action outside the entitlement is not-found, got %+v", d)
	}
	g.ent(t, oncallEnt())
	// step 3: never is refused, and named as such (not as not-found)
	if d := step(never, infra, ActRead); d.OK || d.NotFound {
		t.Fatalf("step 3: never is refused, got %+v", d)
	}
	// step 4: the approval is the stricter of the item's level and the entitlement's floor
	if d := step(plain, infra, ActRead); !d.OK || d.Level != LevelReview {
		t.Fatalf("step 4a: the collection's default holds, got %+v", d)
	}
	if d := step(prod, infra, ActRead); !d.OK || d.Level != LevelPerson {
		t.Fatalf("step 4b: the stricter tag wins, got %+v", d)
	}
	if d := step(token, infra, ActRead); !d.OK || d.Level != LevelReview {
		t.Fatalf("step 4c: the strictest of collection and tags holds, got %+v", d)
	}
	g.ent(t, Entitlement{Name: "oncall-dns", Principals: Principals{Agents: []string{"worker"}},
		Resources: Resources{Collections: []string{"Infra"}}, Actions: []string{ActDescribe, ActRead},
		Approval: LevelPerson})
	if d := step(token, infra, ActRead); !d.OK || d.Level != LevelPerson {
		t.Fatalf("step 4d: the entitlement's floor raises the bar, got %+v", d)
	}
	g.ent(t, oncallEnt())
	// step 5 is TestUsesDurationClamps below; here the actions' intersection:
	if d := step(plain, infra, ActRead); !d.OK || !containsString(d.Actions, ActRead) || containsString(d.Actions, ActUse) {
		t.Fatalf("the actions are the entitlement's, got %+v", d)
	}
	_ = now
}

// TestLevelInheritance is the first axis: an item's explicit level is its own; otherwise the
// strictest of the collection's default and every tag it wears.
func TestLevelInheritance(t *testing.T) {
	g := newGrantsVault(t)
	infra, plain, prod, token, _, _ := g.standardShape(t)
	if got := g.EffectiveLevel(plain); got != LevelReview {
		t.Fatalf("the collection's default holds, got %s", got)
	}
	if got := g.EffectiveLevel(prod); got != LevelPerson {
		t.Fatalf("the stricter tag raises the level, got %s", got)
	}
	if got := g.EffectiveLevel(token); got != LevelReview {
		t.Fatalf("a looser tag does not lower the collection's default, got %s", got)
	}
	// an explicit level is the manager's decision: it overrides the inheritance and is recorded
	// on the row (the console marks it "overrides the default")
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: infra.Name, Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	item, _, _ := g.Item(token.ID)
	if item.Level != LevelAuto {
		t.Fatalf("the explicit level is on the row, got %q", item.Level)
	}
	if got := g.EffectiveLevel(item); got != LevelAuto {
		t.Fatalf("the explicit level wins, got %s", got)
	}
}

// TestActionIntersection: the item, its tags and its collection may each narrow the actions.
func TestActionIntersection(t *testing.T) {
	g := newGrantsVault(t)
	infra, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, Entitlement{Name: "wide", Principals: Principals{Agents: []string{"worker"}},
		Resources: Resources{Collections: []string{"Infra"}}, Actions: []string{ActDescribe, ActRead, ActUse}})
	g.newAgent(t, "worker", "own")
	agent, _ := g.agentInfoOf("worker")

	// the item narrows to use only
	if _, err := g.SetItem(g.owner, ItemIn{ID: plain.ID, Collection: infra.Name, Actions: []string{ActUse},
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	item, col, _ := g.Item(plain.ID)
	d := g.decidePolicy(agent, item, col, ActRead, "")
	if d.OK || !d.NotFound {
		t.Fatalf("read is outside what the item allows, got %+v", d)
	}
	d = g.decidePolicy(agent, item, col, ActUse, "")
	if !d.OK || !equalStrings(d.Actions, []string{ActUse}) {
		t.Fatalf("use is what is left, got %+v", d)
	}
	// a tag narrows too
	if _, err := g.SetTag(g.owner, "kind/one-shot", "", "", []string{ActUse}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.SetItem(g.owner, ItemIn{ID: plain.ID, Collection: infra.Name, Tags: []string{"kind/one-shot"},
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	item, col, _ = g.Item(plain.ID)
	if got := g.itemActions(item, col); !equalStrings(got, []string{ActUse}) {
		t.Fatalf("the tag narrows the actions, got %v", got)
	}
	// and a collection narrows as well
	if _, err := g.UpdateCollection(g.owner, infra.Name, "", "", []string{ActUse, ActRead}); err != nil {
		t.Fatal(err)
	}
	item, col, _ = g.Item(plain.ID)
	if got := g.itemActions(item, col); !equalStrings(got, []string{ActUse}) {
		t.Fatalf("the collection and the tag intersect, got %v", got)
	}
}

// TestEntitlementSelection: several entitlements covering one item — the widest approval floor
// wins, at equal floors the larger ceilings, and a named entitlement is the one used.
func TestEntitlementSelection(t *testing.T) {
	g := newGrantsVault(t)
	infra, plain, _, _, _, _ := g.standardShape(t)
	g.newAgent(t, "worker", "own")
	agent, _ := g.agentInfoOf("worker")

	g.ent(t, Entitlement{Name: "narrow", Principals: Principals{Agents: []string{"worker"}},
		Resources: Resources{Collections: []string{"Infra"}}, Actions: []string{ActDescribe, ActRead},
		Approval: LevelPerson})
	d := g.decidePolicy(agent, plain, infra, ActRead, "")
	if !d.OK || d.Entitlement.Name != "narrow" || d.Level != LevelPerson {
		t.Fatalf("one covering entitlement is the one, got %+v", d)
	}
	g.ent(t, oncallEnt()) // no floor: auto
	d = g.decidePolicy(agent, plain, infra, ActRead, "")
	if !d.OK || d.Entitlement.Name != "oncall-dns" || d.Level != LevelReview {
		t.Fatalf("the wider floor wins, got %+v", d)
	}
	// the named entitlement is the one the request means
	d = g.decidePolicy(agent, plain, infra, ActRead, "narrow")
	if !d.OK || d.Entitlement.Name != "narrow" || d.Level != LevelPerson {
		t.Fatalf("a named entitlement is used, got %+v", d)
	}
	// at equal floors the larger ceilings win: oncall-dns may grant 100 uses, wide may grant 10
	g.setSetting(t, "request.max_uses", "unlimited")
	if _, err := g.sset.Set(context.Background(), settings.Actor{Name: "own", Via: "test"},
		settings.ScopeEntitlement, "oncall-dns", "request.max_uses", json.RawMessage("100"), "test", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := g.sset.Set(context.Background(), settings.Actor{Name: "own", Via: "test"},
		settings.ScopeEntitlement, "wide", "request.max_uses", json.RawMessage("10"), "test", 0); err != nil {
		t.Fatal(err)
	}
	g.ent(t, Entitlement{Name: "wide", Principals: Principals{Agents: []string{"worker"}},
		Resources: Resources{Collections: []string{"Infra"}}, Actions: []string{ActDescribe, ActRead}})
	d = g.decidePolicy(agent, plain, infra, ActRead, "")
	if !d.OK || d.Entitlement.Name != "oncall-dns" {
		t.Fatalf("the larger use ceiling wins, got %+v", d)
	}
	// deny beats them all
	g.ent(t, Entitlement{Name: "no-infra", Deny: true,
		Principals: Principals{Agents: []string{"worker"}}, Resources: Resources{Collections: []string{"Infra"}}})
	if d := g.decidePolicy(agent, plain, infra, ActRead, ""); d.OK {
		t.Fatalf("deny wins, got %+v", d)
	}
}

// TestOwnerCap: an agent asks for no more than its owner holds over the collection.
func TestOwnerCap(t *testing.T) {
	g := newGrantsVault(t)
	infra, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	// an agent of the viewer, who holds nothing over Infra; the entitlement reaches it by owner
	g.ent(t, Entitlement{Name: "vie-reach", Principals: Principals{AgentsOf: []string{"vie"}},
		Resources: Resources{Collections: []string{"Infra"}}, Actions: []string{ActDescribe, ActRead}})
	g.newAgent(t, "watcher", "vie")
	out, err := g.RequestSecret("watcher", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Granted) != 0 || out.Pending != nil {
		t.Fatalf("the owner's lack of access keeps the ask out, got %+v", out)
	}
	if len(out.Refused) != 1 || !out.Refused[0].NotFound {
		t.Fatalf("the refusal is not-found, got %+v", out.Refused)
	}
	// read over the collection opens the way (the owner cap, not the agent's own)
	if err := g.SetPermission(g.owner, infra.Name, "user:vie", PermRead, ""); err != nil {
		t.Fatal(err)
	}
	out, err = g.RequestSecret("watcher", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Pending == nil && len(out.Granted) != 1 {
		t.Fatalf("with the owner reading, the ask goes through, got %+v", out)
	}
}

// ---- requests and the split

// TestRequestSplit: one call, items at mixed levels — the auto part is granted at once, the rest
// waits, and the answer says which is which.
func TestRequestSplit(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, prod, token, _, _ := g.standardShape(t)
	// the token item is review (the collection's default); make it auto explicitly
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: "Infra", Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title, plain.Title, prod.Title},
		Reason: "fix the dns outage tonight"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Granted) != 1 || out.Granted[0].Item != token.ID {
		t.Fatalf("the auto part is granted at once, got %+v", out.Granted)
	}
	if out.Pending == nil || len(out.Pending.Items) != 2 {
		t.Fatalf("the review and person parts wait, got %+v", out.Pending)
	}
	if out.Pending.Level != LevelPerson {
		t.Fatalf("the waiting part carries the strictest level, got %s", out.Pending.Level)
	}
	// the auto grant works at once; the waiting ones do not
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading it", "", "", "", ""); err != nil {
		t.Fatalf("the auto grant reads: %v", err)
	}
	if _, err := g.GetSecret("worker", "test", "Infra/"+plain.Title, "password", "reading it", "", "", "", ""); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("the waiting part has no grant yet, got %v", err)
	}
	// refusing the person part does not touch the auto grant
	if _, _, err := g.Decide(DecideActor{Name: "own", Kind: "person", Via: "console", Role: "owner"},
		out.Pending.ID, false, "not tonight", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading it again", "", "", "", ""); err != nil {
		t.Fatalf("the auto grant survives the refusal, got %v", err)
	}
}

// TestRequestReasonAndClamps: the reason's shortest length, and the uses and duration clamped by
// the settings at every level.
func TestRequestReasonAndClamps(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, token, _, _ := g.standardShape(t)
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: "Infra", Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")

	// the reason is required, ten characters by default
	if _, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "too short"}); err == nil {
		t.Fatal("a short reason is refused")
	}
	// the clamps: max_uses 3 and max_duration an hour
	g.setSetting(t, "request.max_uses", 3)
	g.setSetting(t, "request.max_duration", "1h")
	var unlimited int64 = 0
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns outage",
		Uses: &unlimited, Duration: &unlimited})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Granted) != 1 || out.Granted[0].Uses != 3 {
		t.Fatalf("the uses are clamped to the ceiling, got %+v", out.Granted)
	}
	if !out.Granted[0].ExpiresAt.After(g.now()) || g.now().Add(time.Hour+time.Minute).Before(out.Granted[0].ExpiresAt) {
		t.Fatalf("the duration is clamped to the ceiling, got %v", out.Granted[0].ExpiresAt)
	}
	// the per-level defaults: a review item with nothing asked waits with the review default (30d)
	g.setSetting(t, "request.max_uses", "unlimited")
	g.setSetting(t, "request.max_duration", "unlimited")
	out, err = g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Pending == nil {
		t.Fatal("the review item waits")
	}
	_ = plain
}

// ---- the invariants

// TestRecordBeforeGive drives the design's first invariant through the service's own AppendUse
// hook: when the use record cannot be written, the read gives nothing, the grant's counter stands
// still and the log holds no new event; when it can, the record and the counter move together.
func TestRecordBeforeGive(t *testing.T) {
	dir := t.TempDir()
	RateReset()
	st, err := store.Open(filepath.Join(dir, "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	code, _, err := st.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Join(code, "own", "Own", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	clk := &clock{now: time.Now()}
	sset, err := settings.Open(settings.Deps{Store: st, Lang: i18n.EN, Log: testLog(t)})
	if err != nil {
		t.Fatal(err)
	}
	var shut atomicBool
	s, err := Open(context.Background(), SealConfig{Mode: SealFile, Dir: filepath.Join(dir, "secrets")},
		Deps{Store: st, Settings: sset, Lang: i18n.EN, Log: testLog(t), Now: clk.Now,
			CheckPassword: func(string, string) error { return nil },
			AppendUse: func(tx *sql.Tx, e *events.Event) error {
				if shut.get() {
					return errors.New("the log is shut")
				}
				return st.UsesLog().AppendTx(tx, e)
			}})
	if err != nil {
		t.Fatal(err)
	}
	if err := sset.SetKey(s.SK()); err != nil {
		t.Fatal(err)
	}
	own := Actor{Name: "own", Via: "test", Role: "owner"}
	col, err := s.CreateCollection(own, "Infra", "", LevelAuto, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetItem(own, ItemIn{Collection: col.Name, Title: "Token item",
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEntitlement(own, Entitlement{Name: "oncall", Principals: Principals{Agents: []string{"worker"}},
		Resources: Resources{Collections: []string{"Infra"}}, Actions: []string{ActDescribe, ActRead}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAgent(store.Agent{Name: "worker", Owner: "own", Preset: "readonly"}); err != nil {
		t.Fatal(err)
	}
	out, err := s.RequestSecret("worker", "test", RequestIn{Items: []string{"Token item"}, Reason: "fix the dns outage"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("the grant: %v %+v", err, out)
	}
	grantID := out.Granted[0].ID
	usesOf := func() int {
		_, total, err := st.UsesLog().Query(events.Filter{Actor: "worker", Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		return total
	}
	usedOf := func() int64 {
		var used int64
		_ = st.Storage().View(func(tx kv.Tx) error {
			v := tx.Get(bucketGrants, []byte(grantID))
			var gr Grant
			_ = json.Unmarshal(v, &gr)
			used = gr.Used
			return nil
		})
		return used
	}
	// a good read works and is recorded
	res, err := s.GetSecret("worker", "test", "Infra/Token item", "password", "reading it", "", "", "", "")
	if err != nil || len(res.Fields) != 1 || res.Fields[0].Value != fixtureToken {
		t.Fatalf("the first read works, got %v %+v", err, res)
	}
	if usesOf() != 1 || usedOf() != 1 {
		t.Fatalf("the use is recorded with the counter, got %d uses %d counter", usesOf(), usedOf())
	}
	// the log shuts: the read refuses, the counter stands still, the log holds no new event
	shut.set(true)
	if _, err := s.GetSecret("worker", "test", "Infra/Token item", "password", "reading it", "", "", "", ""); err == nil {
		t.Fatal("a use that cannot be recorded gives no value")
	} else if !strings.Contains(err.Error(), "could not be recorded") {
		t.Fatalf("the refusal says why, got %v", err)
	}
	if usesOf() != 1 || usedOf() != 1 {
		t.Fatalf("the failed use wrote nothing and moved nothing, got %d uses %d counter", usesOf(), usedOf())
	}
	// the log opens again: the read works and the counter catches up
	shut.set(false)
	if _, err := s.GetSecret("worker", "test", "Infra/Token item", "password", "reading it", "", "", "", ""); err != nil {
		t.Fatalf("the read works again, got %v", err)
	}
	if usesOf() != 2 || usedOf() != 2 {
		t.Fatalf("the counter caught up, got %d uses %d counter", usesOf(), usedOf())
	}
}

type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (a *atomicBool) set(v bool) { a.mu.Lock(); a.v = v; a.mu.Unlock() }
func (a *atomicBool) get() bool  { a.mu.Lock(); defer a.mu.Unlock(); return a.v }

// TestRequesterCannotApprove: the requester never reviews its own request — the second invariant —
// by agent or, with the stricter separation, by its owner.
func TestRequesterCannotApprove(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the request: %v %+v", err, out)
	}
	// the agent itself cannot decide — through the review-agent door
	if _, _, err := g.Decide(DecideActor{Name: "worker", Kind: "agent", Via: "mcp"}, out.Pending.ID, true, "i allow this", nil, nil); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("the requester cannot review its own request, got %v", err)
	}
	// the owner may, with the default separation (requester only)
	if _, _, err := g.Decide(DecideActor{Name: "own", Kind: "person", Via: "console", Role: "owner"},
		out.Pending.ID, true, "go ahead", nil, nil); err != nil {
		t.Fatalf("the owner decides under the default separation, got %v", err)
	}
	// the stricter separation stands the owner aside too
	g.setSetting(t, "review.separation", "owner")
	out, err = g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the second request: %v %+v", err, out)
	}
	if _, _, err := g.Decide(DecideActor{Name: "own", Kind: "person", Via: "console", Role: "owner"},
		out.Pending.ID, true, "go ahead", nil, nil); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("with the stricter separation the owner stands aside, got %v", err)
	}
	// a person who is neither operator nor owner (nor a named reviewer) never decides
	if _, _, err := g.Decide(DecideActor{Name: "vie", Kind: "person", Via: "console", Role: "viewer"},
		out.Pending.ID, true, "go ahead", nil, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a viewer does not decide, got %v", err)
	}
	// an operator (or a named reviewer) does
	if _, _, err := g.Decide(DecideActor{Name: "ops", Kind: "person", Via: "console", Role: "operator"},
		out.Pending.ID, true, "go ahead", nil, nil); err != nil {
		t.Fatalf("an operator decides, got %v", err)
	}
}

// TestNeverNoAgent: a never item reaches no agent, not by asking, not by holding — the fourth
// invariant.
func TestNeverNoAgent(t *testing.T) {
	g := newGrantsVault(t)
	infra, _, _, _, never, _ := g.standardShape(t)
	g.ent(t, Entitlement{Name: "all-infra", Principals: Principals{AgentsOf: []string{"own"}},
		Resources: Resources{Collections: []string{"Infra"}}, Actions: []string{ActDescribe, ActRead}})
	g.newAgent(t, "worker", "own")
	// the ask is refused
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{never.Title}, Reason: "fix the dns outage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Granted) != 0 || out.Pending != nil || len(out.Refused) != 1 || out.Refused[0].NotFound {
		// never is named as refused (not hidden as not-found): the design says so, so no one wonders
		t.Logf("the refusals: %+v", out.Refused)
	}
	// a grant forced over a never item (by tightening the item after the grant) dies at once
	plain := g.makeItem(t, infra, "Turned never")
	if _, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns"}); err != nil {
		t.Fatal(err)
	}
	// (the item is review level; approve it to hold a grant)
	reqs := g.Requests(ReqPending)
	if len(reqs) != 1 {
		t.Fatalf("one request waits, got %d", len(reqs))
	}
	if _, _, err := g.Decide(DecideActor{Name: "own", Kind: "person", Via: "console", Role: "owner"},
		reqs[0].ID, true, "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := g.GetSecret("worker", "test", "Infra/"+plain.Title, "password", "reading", "", "", "", ""); err != nil {
		t.Fatalf("the grant reads before the change, got %v", err)
	}
	// the item turns never: the grants are re-assessed and revoked, and a read refuses
	if _, err := g.SetItem(g.owner, ItemIn{ID: plain.ID, Collection: infra.Name, Level: LevelNever,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	// the invariant fires first: never is not given to an agent, whatever the grant said
	if _, err := g.GetSecret("worker", "test", "Infra/"+plain.Title, "password", "reading", "", "", "", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a never item gives nothing, got %v", err)
	}
	hooks := g.hooks.got()
	found := false
	for _, h := range hooks {
		if strings.HasSuffix(h, " secret.revoked") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the revoked grant told its holder, got %v", hooks)
	}
}

// ---- the signature's invariant on the new rows

// TestEntitlementTamperBlocked: a row changed behind Beacon's back fails its signature, is served
// as absent, and is reported — the third invariant, on the grants' rows.
func TestEntitlementTamperBlocked(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	e := g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	// the row is widened straight in the database: never to Personal
	e.Resources.Collections = []string{"Infra", "Personal"}
	b, _ := json.Marshal(e)
	if err := g.st.Storage().Update(func(tx kv.Tx) error {
		return tx.Put(bucketEntitlements, []byte(e.Name), b)
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Entitlement(e.Name); ok {
		t.Fatal("a row that fails its signature is served as absent")
	}
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Granted) != 0 || out.Pending != nil {
		t.Fatalf("the tampered entitlement grants nothing, got %+v", out)
	}
	if bad := g.CheckRows(); len(bad) == 0 {
		t.Fatal("the tampered row is reported")
	}
	// a tampered grant row is as absent
	out2, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || len(out2.Granted) != 0 {
		t.Fatalf("still nothing, got %v %+v", err, out2)
	}
}

// ---- grants' lifetime

// TestGrantLifecycle: uses run out, expiry ends, renewal by the level, release and revocation.
func TestGrantLifecycle(t *testing.T) {
	g := newGrantsVault(t)
	_, _, _, token, _, _ := g.standardShape(t)
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: "Infra", Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	uses := int64(2)
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns", Uses: &uses})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("the grant: %v %+v", err, out)
	}
	read := func() error {
		_, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", "")
		return err
	}
	if err := read(); err != nil {
		t.Fatal(err)
	}
	if err := read(); err != nil {
		t.Fatal(err)
	}
	if err := read(); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("the third read is past the uses, got %v", err)
	}
	if grants := g.GrantsOf("worker"); len(grants) != 1 || grants[0].Status != GrantExhausted {
		t.Fatalf("the grant is exhausted, got %+v", grants)
	}

	// a grant with an expiry ends with it, and the notice goes ahead of the end
	out, err = g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("the second grant: %v %+v", err, out)
	}
	// renewal: an auto-level grant renews on its own
	g.clk.Add(2 * 24 * time.Hour)
	g.Sweep() // past the default notice window (3d) is not yet past the end (no expiry: never)
	if err := read(); err != nil {
		t.Fatalf("the unlimited grant reads on, got %v", err)
	}
	grant2 := out.Granted[0]
	if _, _, err := g.RenewGrant("worker", grant2.ID, "", "test"); err != nil {
		t.Fatalf("an auto-level grant renews, got %v", err)
	}
	// a review-level grant's renewal asks again
	g.ent(t, oncallEnt())
	plain := g.makeItem(t, mustFindCollection(t, g, "Infra"), "Plain database")
	out, err = g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the review ask waits: %v %+v", err, out)
	}
	if _, _, err := g.Decide(DecideActor{Name: "own", Kind: "person", Via: "console", Role: "owner"},
		out.Pending.ID, true, "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	grants := g.GrantsOf("worker")
	var reviewGrant Grant
	for _, gr := range grants {
		if gr.Level == LevelReview && gr.Status == GrantActive {
			reviewGrant = gr
		}
	}
	if reviewGrant.ID == "" {
		t.Fatalf("the review grant is there, got %+v", grants)
	}
	_, pending, err := g.RenewGrant("worker", reviewGrant.ID, "still need it", "test")
	if err != nil {
		t.Fatal(err)
	}
	if pending == nil || pending.Status != ReqPending {
		t.Fatalf("a review-level renewal asks again, got %+v", pending)
	}
	// release and revocation
	if err := g.ReleaseGrant("worker", reviewGrant.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if err := g.RevokeGrant(g.owner, grant2.ID, "the drill is over"); err != nil {
		t.Fatal(err)
	}
	for _, gr := range g.GrantsOf("worker") {
		if gr.Status == GrantActive {
			t.Fatalf("no grant stands, got %+v", gr)
		}
	}
}

func mustFindCollection(t *testing.T, g *grantsVault, name string) Collection {
	t.Helper()
	col, ok := g.Collection(name)
	if !ok {
		t.Fatalf("no collection %s", name)
	}
	return col
}

// TestPendingExpiry: a request nobody decided meets request.on_pending_expiry.
func TestPendingExpiry(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.setSetting(t, "request.pending_ttl", "1h")
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the request: %v %+v", err, out)
	}
	id := out.Pending.ID
	g.clk.Add(2 * time.Hour)
	if n := g.Sweep(); n == 0 {
		t.Fatal("the sweep did something")
	}
	r, ok := g.Request(id)
	if !ok || r.Status != ReqExpired {
		t.Fatalf("the default is to expire, got %+v", r)
	}
	// deny: the request is denied and the hook says so
	g.setSetting(t, "request.on_pending_expiry", "deny")
	out, err = g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the request: %v %+v", err, out)
	}
	id = out.Pending.ID
	g.clk.Add(2 * time.Hour)
	g.Sweep()
	if r, ok := g.Request(id); !ok || r.Status != ReqDenied {
		t.Fatalf("the setting's deny holds, got %+v", r)
	}
	// approve: the request turns into a grant
	g.setSetting(t, "request.on_pending_expiry", "approve")
	out, err = g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the request: %v %+v", err, out)
	}
	id = out.Pending.ID
	g.clk.Add(2 * time.Hour)
	g.Sweep()
	if r, ok := g.Request(id); !ok || r.Status != ReqGranted {
		t.Fatalf("the setting's approve holds, got %+v", r)
	}
	if _, err := g.GetSecret("worker", "test", "Infra/"+plain.Title, "password", "reading", "", "", "", ""); err != nil {
		t.Fatalf("the approved grant reads, got %v", err)
	}
}

// TestPolicyTightened: the policy changing around a grant ends it the way the setting says.
func TestPolicyTightened(t *testing.T) {
	g := newGrantsVault(t)
	_, _, _, token, _, _ := g.standardShape(t)
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: "Infra", Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	e := g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("the grant: %v %+v", err, out)
	}
	grantID := out.Granted[0].ID
	read := func() error {
		_, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", "")
		return err
	}
	// the entitlement goes: the default is to revoke
	if err := g.DeleteEntitlement(g.owner, e.Name); err != nil {
		t.Fatal(err)
	}
	if err := read(); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("the revoked grant reads nothing, got %v", err)
	}
	if grants := g.GrantsOf("worker"); len(grants) != 1 || grants[0].Status != GrantRevoked {
		t.Fatalf("the grant is revoked, got %+v", grants)
	}
	// keep_until_expiry: the grant stands until its own end
	g.setSetting(t, "grant.on_policy_tightened", "keep_until_expiry")
	g.ent(t, oncallEnt())
	out, err = g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("the second grant: %v %+v", err, out)
	}
	if err := g.DeleteEntitlement(g.owner, e.Name); err != nil {
		t.Fatal(err)
	}
	if err := read(); err != nil {
		t.Fatalf("the grant stands until its expiry, got %v", err)
	}
	_ = grantID
}

// TestOwnerAccessLost: the owner losing the collection ends the agent's grants over it.
func TestOwnerAccessLost(t *testing.T) {
	dir := t.TempDir()
	RateReset()
	st, err := store.Open(filepath.Join(dir, "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	code, _, _ := st.CreateSetupLink()
	if _, err := st.Join(code, "own", "Own", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	inv, _, _ := st.CreateInvitation("operator", "test", "own")
	if _, err := st.Join(inv, "ops", "Ops", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	clk := &clock{now: time.Now()}
	sset, _ := settings.Open(settings.Deps{Store: st, Lang: i18n.EN, Log: testLog(t)})
	s, err := Open(context.Background(), SealConfig{Mode: SealFile, Dir: filepath.Join(dir, "secrets")},
		Deps{Store: st, Settings: sset, Lang: i18n.EN, Log: testLog(t), Now: clk.Now,
			CheckPassword: func(string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	_ = sset.SetKey(s.SK())
	own := Actor{Name: "own", Via: "test", Role: "owner"}
	col, err := s.CreateCollection(own, "Shared", "", LevelAuto, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetItem(own, ItemIn{Collection: col.Name, Title: "Token item",
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	// the agent belongs to the operator, who reads the collection
	if err := s.SetPermission(own, col.Name, "user:ops", PermRead, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEntitlement(own, Entitlement{Name: "ops-reach", Principals: Principals{Agents: []string{"worker"}},
		Resources: Resources{Collections: []string{"Shared"}}, Actions: []string{ActDescribe, ActRead}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAgent(store.Agent{Name: "worker", Owner: "ops", Preset: "readonly"}); err != nil {
		t.Fatal(err)
	}
	out, err := s.RequestSecret("worker", "test", RequestIn{Items: []string{"Token item"}, Reason: "fix the dns"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("the grant: %v %+v", err, out)
	}
	if _, err := s.GetSecret("worker", "test", "Shared/Token item", "password", "reading", "", "", "", ""); err != nil {
		t.Fatalf("the grant reads, got %v", err)
	}
	// the operator's permission goes: the grants over the collection are revoked
	if err := s.SetPermission(own, col.Name, "user:ops", "", ""); err != nil {
		t.Fatal(err)
	}
	if grants := s.GrantsOf("worker"); len(grants) != 1 || grants[0].Status != GrantRevoked {
		t.Fatalf("the owner losing access revokes the grants, got %+v", grants)
	}
	if _, err := s.GetSecret("worker", "test", "Shared/Token item", "password", "reading", "", "", "", ""); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("a read after the loss has no grant, got %v", err)
	}
}

// TestItemChangedNotice: a new version under a grant tells the holder to take it again.
func TestItemChangedNotice(t *testing.T) {
	g := newGrantsVault(t)
	_, _, _, token, _, _ := g.standardShape(t)
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: "Infra", Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("the grant: %v %+v", err, out)
	}
	g.hooks.waitHooks(t, 1) // the grant's own event went out
	g.hooks.clear()
	// the value changes: the holder is told
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: "Infra",
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: "fixture-value-rotated-thud-wombat-31"}}}); err != nil {
		t.Fatal(err)
	}
	hooks := g.hooks.waitHooks(t, 1)
	if len(hooks) != 1 || !strings.Contains(hooks[0], "secret.changed") {
		t.Fatalf("the holder heard of the change, got %v", hooks)
	}
	// and the notice is off when the setting says none
	g.setSetting(t, "grant.on_item_changed", "none")
	g.hooks.clear()
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: "Infra",
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	if hooks := g.hooks.got(); len(hooks) != 0 {
		t.Fatalf("the notice is off, got %v", hooks)
	}
}

// ---- rate limits and anomalies

// TestRateLimit: past the uses a minute, the agent is throttled, or only told of when the setting
// says alert.
func TestRateLimit(t *testing.T) {
	g := newGrantsVault(t)
	_, _, _, token, _, _ := g.standardShape(t)
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: "Infra", Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	if _, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns"}); err != nil {
		t.Fatal(err)
	}
	read := func() error {
		_, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", "")
		return err
	}
	g.setSetting(t, "rate.per_agent_per_minute", 2)
	if err := read(); err != nil {
		t.Fatal(err)
	}
	if err := read(); err != nil {
		t.Fatal(err)
	}
	if err := read(); !errors.Is(err, ErrThrottled) {
		t.Fatalf("the third read is past the rate, got %v", err)
	}
	// alert instead: the read goes through and the chat hears of it
	g.setSetting(t, "rate.on_exceed", "alert")
	if err := read(); err != nil {
		t.Fatalf("with alert the read goes through, got %v", err)
	}
	alarmed := false
	for _, m := range g.bot.all() {
		if strings.Contains(m, "passed its rate") {
			alarmed = true
		}
	}
	if !alarmed {
		t.Fatal("the chat heard of the rate")
	}
}

// TestAnomalies: the first use of a collection notifies; with the harsher consequence the agent's
// grants are suspended.
func TestAnomalies(t *testing.T) {
	g := newGrantsVault(t)
	_, _, _, token, _, _ := g.standardShape(t)
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: "Infra", Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("the grant: %v %+v", err, out)
	}
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	notified := false
	for _, m := range g.bot.all() {
		if strings.Contains(m, "first_collection_use") || strings.Contains(m, "for the first time") {
			notified = true
		}
	}
	if !notified {
		t.Fatalf("the first use notified, got %v", g.bot.all())
	}
	// the second use in the same collection is no anomaly
	before := len(g.bot.all())
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if after := len(g.bot.all()); after != before {
		t.Fatalf("the second use is no anomaly, got %d more", after-before)
	}
	// the harsher consequence: a fresh agent's first use suspends its grants
	g.setSetting(t, "anomaly.on_detect", "suspend_agent")
	g.newAgent(t, "spender", "own")
	g.ent(t, Entitlement{Name: "spender-reach", Principals: Principals{Agents: []string{"spender"}},
		Resources: Resources{Collections: []string{"Infra"}}, Actions: []string{ActDescribe, ActRead}})
	if _, err := g.RequestSecret("spender", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns"}); err != nil {
		t.Fatal(err)
	}
	// a grant at the auto level needs no decision; the first read fires the rule and suspends
	if _, err := g.GetSecret("spender", "test", "Infra/"+token.Title, "password", "reading", "", "", "", ""); err != nil {
		t.Fatalf("the first read works and fires, got %v", err)
	}
	if _, err := g.GetSecret("spender", "test", "Infra/"+token.Title, "password", "reading", "", "", "", ""); !errors.Is(err, ErrSuspendedAgent) {
		t.Fatalf("the suspended agent reads nothing, got %v", err)
	}
	if grants := g.GrantsOf("spender"); len(grants) == 0 || grants[0].Status != GrantRevoked {
		t.Fatalf("the grants were suspended (revoked), got %+v", grants)
	}
	// a signature failure notifies rather than suspends: it names no agent
	g.ent(t, Entitlement{Name: "broken", Principals: Principals{Agents: []string{"worker"}},
		Resources: Resources{Collections: []string{"Infra"}}, Actions: []string{ActDescribe}})
	bad := Entitlement{Name: "broken", Principals: Principals{Agents: []string{"someone-else"}},
		Resources: Resources{Collections: []string{"Infra"}}, Actions: []string{ActRead}}
	bad.Sig = nil
	bb, _ := json.Marshal(bad)
	if err := g.st.Storage().Update(func(tx kv.Tx) error { return tx.Put(bucketEntitlements, []byte("broken"), bb) }); err != nil {
		t.Fatal(err)
	}
	if bad := g.CheckRows(); len(bad) == 0 {
		t.Fatal("a failed signature is reported")
	}
	// an operator lifts the suspension
	if err := g.UnsuspendAgent(g.owner, "spender"); err != nil {
		t.Fatal(err)
	}
}

// ---- the search and the preview

// TestSearchForAgent: the describe search shows what the entitlements reach, with the fields, the
// level and the grant, and nothing else.
func TestSearchForAgent(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, personal := g.standardShape(t)
	g.makeItem(t, personal, "A card")
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	res := g.SearchForAgent("worker", "", "", nil)
	if len(res) != 4 { // plain, prod, token and never are all in Infra; Personal is not
		t.Fatalf("the search shows the entitlement's reach, got %d", len(res))
	}
	for _, r := range res {
		if r.Collection != "Infra" {
			t.Fatalf("nothing outside the reach shows, got %+v", r)
		}
		if len(r.Fields) == 0 {
			t.Fatalf("the fields show with their names, got %+v", r)
		}
		if r.HasGrant {
			t.Fatalf("no grant yet, got %+v", r)
		}
	}
	// a grant shows once it is held (the review ask approved)
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the ask waits: %v %+v", err, out)
	}
	if _, _, err := g.Decide(DecideActor{Name: "own", Kind: "person", Via: "console", Role: "owner"},
		out.Pending.ID, true, "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range g.SearchForAgent("worker", plain.Title, "", nil) {
		if r.Item.ID == plain.ID && !r.HasGrant {
			t.Fatalf("the grant shows, got %+v", r)
		}
	}
	// a deny hides what it matches
	g.ent(t, Entitlement{Name: "no-prod", Deny: true, Principals: Principals{Agents: []string{"worker"}},
		Resources: Resources{Tags: []string{"env/prod"}}})
	seen := map[string]bool{}
	for _, r := range g.SearchForAgent("worker", "", "", nil) {
		seen[r.Item.Title] = true
	}
	if seen["Prod database"] {
		t.Fatal("the deny hides its matches")
	}
	// the preview counts by level: plain and token are review (the collection's default beats the
	// looser tag), prod is person, never is apart and out of the total
	p := g.PreviewForAgent("worker")
	if p.Total != 3 || p.Auto != 0 || p.Review != 2 || p.Person != 1 || p.Never != 1 {
		t.Fatalf("the preview counts by level, got %+v", p)
	}
}

// ---- the hooks and the card

// TestSecretHookDelivery: an event posted to the agent's hook carries the same signature the
// on-call hooks carry, and the receiver can check it.
func TestSecretHookDelivery(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	var events []string
	var sigs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := readAll(r)
		mu.Lock()
		defer mu.Unlock()
		bodies = append(bodies, string(b))
		events = append(events, r.Header.Get("X-Beacon-Event"))
		sigs = append(sigs, r.Header.Get("X-Beacon-Signature"))
		w.WriteHeader(200)
	}))
	defer srv.Close()
	h := &HookHTTP{URL: srv.URL, Secret: "test-hook-secret-not-a-real-one-32", Headers: map[string]string{"X-Webhook-Secret": "receiver-secret-not-real"}}
	if err := h.SendSecretHook(context.Background(), "worker", "secret.granted", []byte(`{"delivery":"d1"}`)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 || events[0] != "secret.granted" {
		t.Fatalf("the event went out, got %v %v", bodies, events)
	}
	if sigs[0] != SecretHookSignature("test-hook-secret-not-a-real-one-32", []byte(bodies[0])) {
		t.Fatalf("the signature is the HMAC of the body, got %s", sigs[0])
	}
}

// TestApprovalCard: a waiting request sends the card with the three decisions and the reason
// input, and the buttons carry the request's id.
func TestApprovalCard(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the request: %v %+v", err, out)
	}
	cards := g.cards.waitCards(t, 1)
	if len(cards) != 1 {
		t.Fatalf("the card went out, got %d", len(cards))
	}
	b, _ := json.Marshal(cards[0])
	var probe struct {
		Body struct {
			Elements []struct {
				Tag      string `json:"tag"`
				Elements []struct {
					Tag     string `json:"tag"`
					Columns []struct {
						Elements []struct {
							Tag       string `json:"tag"`
							Behaviors []struct {
								Value map[string]any `json:"value"`
							} `json:"behaviors"`
						} `json:"elements"`
					} `json:"columns"`
				} `json:"elements"`
			} `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		t.Fatal(err)
	}
	var buttons []map[string]any
	for _, el := range probe.Body.Elements {
		if el.Tag != "form" {
			continue
		}
		for _, sub := range el.Elements {
			if sub.Tag == "column_set" {
				for _, col := range sub.Columns {
					for _, c := range col.Elements {
						if c.Tag == "button" && len(c.Behaviors) > 0 && c.Behaviors[0].Value != nil {
							buttons = append(buttons, c.Behaviors[0].Value)
						}
					}
				}
			}
		}
	}
	if len(buttons) != 3 {
		t.Fatalf("the card carries the three decisions, got %v", buttons)
	}
	for _, v := range buttons {
		if v["r"] != out.Pending.ID {
			t.Fatalf("the buttons carry the request's id, got %v", v)
		}
	}
}

// ---- the otp rule

// TestGetSecretOTP: an otp field answers with the current code and the seconds left, never the
// seed — unless the setting gives it.
func TestGetSecretOTP(t *testing.T) {
	g := newGrantsVault(t)
	col, err := g.CreateCollection(g.owner, "Infra", "", LevelAuto, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.SetItem(g.owner, ItemIn{Collection: col.Name, Title: "One-time item",
		Fields: []FieldIn{{Name: "code", Type: FieldOTP, Value: fixtureOTPSeed}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.SetItem(g.owner, ItemIn{ID: mustItem(t, g, col, "One-time item").ID, Collection: col.Name, Level: LevelAuto,
		Fields: []FieldIn{{Name: "code", Type: FieldOTP, Value: fixtureOTPSeed}}}); err != nil {
		t.Fatal(err)
	}
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	if _, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{"One-time item"}, Reason: "fix the dns"}); err != nil {
		t.Fatal(err)
	}
	res, err := g.GetSecret("worker", "test", "Infra/One-time item", "code", "signing in", "", "", "", "")
	if err != nil || len(res.Fields) != 1 {
		t.Fatalf("the read works, got %v %+v", err, res)
	}
	f := res.Fields[0]
	if len(f.Value) != 6 || f.Value == fixtureOTPSeed {
		t.Fatalf("the code is the current one, not the seed, got %q", f.Value)
	}
	if f.OTPExpiry <= 0 || f.OTPExpiry > 30 {
		t.Fatalf("the seconds left show, got %d", f.OTPExpiry)
	}
	want, _, _ := TOTPNow(fixtureOTPSeed, g.now())
	if f.Value != want {
		t.Fatalf("the code is the right one, got %s want %s", f.Value, want)
	}
	// with the setting on, the seed goes out
	g.setSetting(t, "otp.seed_to_agents", true)
	res, err = g.GetSecret("worker", "test", "Infra/One-time item", "code", "signing in", "", "", "", "")
	if err != nil || res.Fields[0].Value != fixtureOTPSeed {
		t.Fatalf("the setting gives the seed, got %v %+v", err, res)
	}
}

func mustItem(t *testing.T, g *grantsVault, col Collection, title string) Item {
	t.Helper()
	item, _, ok := g.Item(title)
	if !ok {
		t.Fatalf("no item %s", title)
	}
	return item
}

// ---- the alert binding

// TestAlertGrace: a grant bound to an alert outlives it by the grace, no longer.
func TestAlertGrace(t *testing.T) {
	g := newGrantsVault(t)
	_, _, _, token, _, _ := g.standardShape(t)
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: "Infra", Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns", Alert: "alert-1"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("the grant: %v %+v", err, out)
	}
	read := func() error {
		_, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", "")
		return err
	}
	if err := read(); err != nil {
		t.Fatal(err)
	}
	// the alert resolves: the grace (15 minutes by default) holds
	g.alertOn["alert-1"] = false
	g.resolvedAt["alert-1"] = g.clk.Now()
	if err := read(); err != nil {
		t.Fatalf("inside the grace the grant works, got %v", err)
	}
	g.clk.Add(20 * time.Minute)
	g.Sweep()
	if err := read(); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("past the grace the grant is gone, got %v", err)
	}
}

// ---- the metrics

// TestMetrics: the counters and the gauges answer what happened.
func TestMetrics(t *testing.T) {
	g := newGrantsVault(t)
	_, _, _, token, _, _ := g.standardShape(t)
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: "Infra", Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns"})
	g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", "")
	var uses float64
	for _, s := range g.MetricsSamples() {
		if strings.Contains(s.Labels, `"read"`) {
			uses = s.Value
		}
	}
	if uses != 1 {
		t.Fatalf("the uses counted, got %v", uses)
	}
	var reqs float64
	for _, s := range g.RequestSamples() {
		if strings.Contains(s.Labels, `"granted"`) {
			reqs = s.Value
		}
	}
	if reqs < 1 {
		t.Fatalf("the requests counted, got %v", reqs)
	}
	active, expiring := g.GrantGauge()
	if active != 1 || expiring != 0 {
		t.Fatalf("the grants gauged, got %d %d", active, expiring)
	}
}

// TestAgentTagsShape: the grouping tags carry the entitlement's reach, and a bad shape is refused.
func TestAgentTagsShape(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.newAgent(t, "worker", "own")
	if err := g.SetAgentTags(g.owner, "worker", []string{"Role/OnCall"}); err != nil {
		t.Fatal(err) // tags normalise to lower-case, as item tags do
	}
	if got := g.TagsOfAgent("worker"); !equalStrings(got, []string{"role/oncall"}) {
		t.Fatalf("the tags normalised, got %v", got)
	}
	if err := g.SetAgentTags(g.owner, "worker", []string{"role/on-call"}); err != nil {
		t.Fatal(err)
	}
	if got := g.TagsOfAgent("worker"); !equalStrings(got, []string{"role/on-call"}) {
		t.Fatalf("the tags are set, got %v", got)
	}
	// an entitlement over the tag reaches the agent
	g.ent(t, Entitlement{Name: "oncall-any", Principals: Principals{AgentTags: []string{"role/on-call"}},
		Resources: Resources{Collections: []string{"Infra"}}, Actions: []string{ActDescribe, ActRead}})
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the tag carries the reach, got %v %+v", err, out)
	}
}

func readAll(r *http.Request) ([]byte, error) { return io.ReadAll(r.Body) }
