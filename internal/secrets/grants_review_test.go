package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/settings"
)

func (g *grantsVault) setSettingAt(t *testing.T, scope, target, key string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.sset.Set(context.Background(), settings.Actor{Name: "own", Via: "test"}, scope, target, key, b, "because the test needs a reason", 0); err != nil {
		t.Fatal(err)
	}
}

// TestFieldRestrictionsBind: the entitlement's fields are the grant's fields; an ask outside
// them is refused; GetSecret filters to those fields and keeps notes back unless they are allowed.
func TestFieldRestrictionsBind(t *testing.T) {
	g := newGrantsVault(t)
	infra, _, _, _, _, _ := g.standardShape(t)
	login, err := g.SetItem(g.owner, ItemIn{Collection: infra.Name, Title: "Login card", Level: LevelAuto,
		Fields: []FieldIn{
			{Name: "username", Type: FieldString, Value: "alice"},
			{Name: "password", Type: FieldConcealed, Value: fixtureToken},
		},
		Notes: "the card pin is 1234", HasNotes: true})
	if err != nil {
		t.Fatal(err)
	}
	g.ent(t, Entitlement{Name: "oncall-dns", Principals: Principals{Agents: []string{"worker"}},
		Resources: Resources{Collections: []string{"Infra"}, Fields: []string{"username"}},
		Actions:   []string{ActDescribe, ActRead}})
	g.newAgent(t, "worker", "own")

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{login.Title}, Reason: "fix the dns outage"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("an ask with no fields takes the entitlement's: %v %+v", err, out)
	}
	if got := out.Granted[0].Fields; len(got) != 1 || got[0] != "username" {
		t.Fatalf("the grant is narrowed to username, got %v", got)
	}
	got, err := g.GetSecret("worker", "test", "Infra/"+login.Title, "", "reading the login", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Fields) != 1 || got.Fields[0].Name != "username" || got.Fields[0].Value != "alice" {
		t.Fatalf("get_secret without a field returns only username, got %+v", got.Fields)
	}
	if got.Notes != "" {
		t.Fatalf("notes are not among the allowed fields, got %q", got.Notes)
	}
	if _, err := g.GetSecret("worker", "test", "Infra/"+login.Title, "password", "reading the password", "", "", "", ""); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("password is outside the grant, got %v", err)
	}

	pw := []string{"password"}
	refused, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{login.Title}, Fields: pw, Reason: "fix the dns outage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(refused.Granted) != 0 || refused.Pending != nil || len(refused.Refused) != 1 {
		t.Fatalf("an ask for password is refused, got %+v", refused)
	}
	if refused.Refused[0].Reason != "a field is out of the entitlement" {
		t.Fatalf("the refusal names the field, got %q", refused.Refused[0].Reason)
	}
}

// TestDenyWithoutPrincipalAppliesToAll: a deny that names no agent still matches every agent.
func TestDenyWithoutPrincipalAppliesToAll(t *testing.T) {
	g := newGrantsVault(t)
	_, _, _, _, _, personal := g.standardShape(t)
	g.ent(t, Entitlement{Name: "no-personal", Deny: true, Resources: Resources{Collections: []string{"Personal"}}})
	g.ent(t, Entitlement{Name: "personal-read", Principals: Principals{Agents: []string{"worker"}},
		Resources: Resources{Collections: []string{"Personal"}}, Actions: []string{ActDescribe, ActRead}})
	g.newAgent(t, "worker", "own")

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{"Someones card"}, Reason: "fix the dns outage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Granted) != 0 || out.Pending != nil || len(out.Refused) != 1 || !out.Refused[0].NotFound {
		t.Fatalf("a deny with no principal hides the item, got %+v", out)
	}
	if out.Refused[0].Reason != "there is no such item" {
		t.Fatalf("the refusal does not name the item, got %q", out.Refused[0].Reason)
	}
	for _, r := range g.SearchForAgent("worker", "", personal.Name, nil) {
		t.Fatalf("search does not show a denied collection, got %+v", r)
	}
}

// TestUsesClampedPerItem: each item takes its own ceilings, even when the ask does not name the
// entitlement, and two items in one ask do not share the first item's selector.
func TestUsesClampedPerItem(t *testing.T) {
	g := newGrantsVault(t)
	infra, _, _, token, _, _ := g.standardShape(t)
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: infra.Name, Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	tools, err := g.CreateCollection(g.owner, "Tools", "", LevelAuto, nil)
	if err != nil {
		t.Fatal(err)
	}
	tool := g.makeItem(t, tools, "Tool token")
	g.ent(t, Entitlement{Name: "oncall-dns", Principals: Principals{Agents: []string{"worker"}},
		Resources: Resources{Collections: []string{"Infra", "Tools"}}, Actions: []string{ActDescribe, ActRead}})
	g.newAgent(t, "worker", "own")
	g.setSettingAt(t, settings.ScopeEntitlement, "oncall-dns", "request.max_uses", 2)
	g.setSettingAt(t, settings.ScopeCollection, "Tools", "request.max_uses", 1)

	named, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns outage"})
	if err != nil || len(named.Granted) != 1 {
		t.Fatalf("the entitlement's ceiling applies without being named: %v %+v", err, named)
	}
	if named.Granted[0].Uses != 2 || named.Granted[0].Entitlement != "oncall-dns" {
		t.Fatalf("uses 2 through oncall-dns, got %+v", named.Granted[0])
	}

	// collection rows bind only when no more-specific entitlement row is set (settings.Value).
	if _, err := g.sset.Reset(context.Background(), settings.Actor{Name: "own", Via: "test"},
		settings.ScopeEntitlement, "oncall-dns", "request.max_uses", "because the test needs a reason", 0); err != nil {
		t.Fatal(err)
	}
	g.setSettingAt(t, settings.ScopeCollection, infra.Name, "request.max_uses", 2)

	both, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title, tool.Title}, Reason: "fix the dns outage"})
	if err != nil || len(both.Granted) != 2 {
		t.Fatalf("two auto items grant at once: %v %+v", err, both)
	}
	byItem := map[string]Grant{}
	for _, grant := range both.Granted {
		byItem[grant.Item] = grant
	}
	if byItem[token.ID].Uses != 2 {
		t.Fatalf("Infra is clamped by its collection, got %+v", byItem[token.ID])
	}
	if byItem[tool.ID].Uses != 1 {
		t.Fatalf("Tools is clamped by its collection, got %+v", byItem[tool.ID])
	}
}

// TestReassessOnTightenAndTrash: raising the level, trashing the item, and Sweep all stop a use;
// auto renewal re-runs the policy rather than extending a grant the policy no longer allows.
func TestReassessOnTightenAndTrash(t *testing.T) {
	g := newGrantsVault(t)
	infra, _, _, token, _, _ := g.standardShape(t)
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: infra.Name, Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns outage"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("the grant: %v %+v", err, out)
	}
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading it", "", "", "", ""); err != nil {
		t.Fatal(err)
	}

	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: infra.Name, Level: LevelPerson,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading it", "", "", "", ""); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("a stricter level ends the grant, got %v", err)
	}

	plain := g.makeItem(t, infra, "Soon trashed")
	if _, err := g.SetItem(g.owner, ItemIn{ID: plain.ID, Collection: infra.Name, Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	out, err = g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("the trash grant: %v %+v", err, out)
	}
	if _, err := g.TrashItem(g.owner, plain.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := g.GetSecret("worker", "test", "Infra/"+plain.Title, "password", "reading it", "", "", "", ""); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("a trashed item is not readable, got %v", err)
	}

	kept, err := g.SetItem(g.owner, ItemIn{Collection: infra.Name, Title: "Kept until expiry", Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}})
	if err != nil {
		t.Fatal(err)
	}
	g.setSetting(t, "grant.on_policy_tightened", "keep_until_expiry")
	out, err = g.RequestSecret("worker", "test", RequestIn{Items: []string{kept.Title}, Reason: "fix the dns outage"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("the kept grant: %v %+v", err, out)
	}
	if _, err := g.SetItem(g.owner, ItemIn{ID: kept.ID, Collection: infra.Name, Level: LevelPerson,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	if grants := g.GrantsOf("worker"); func() bool {
		for _, grant := range grants {
			if grant.Item == kept.ID && grant.Status == GrantActive {
				return true
			}
		}
		return false
	}() == false {
		t.Fatal("keep_until_expiry leaves the grant active")
	}
	if _, err := g.GetSecret("worker", "test", "Infra/"+kept.Title, "password", "reading it", "", "", "", ""); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("use still re-runs the policy, got %v", err)
	}
	if _, _, err := g.RenewGrant("worker", out.Granted[0].ID, "", "test"); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("auto renewal re-runs the policy, got %v", err)
	}
	g.setSetting(t, "grant.on_policy_tightened", "revoke")
	g.Sweep()
	for _, grant := range g.GrantsOf("worker") {
		if grant.Item == kept.ID && grant.Status == GrantActive {
			t.Fatalf("Sweep revokes what the policy tightened, got %+v", grant)
		}
	}
}

// TestHiddenNotFound: an item outside the agent's reach looks the same as one that is not there,
// in the ask and in the search, including never items and the owner's missing collection cap.
func TestHiddenNotFound(t *testing.T) {
	g := newGrantsVault(t)
	infra, _, _, _, never, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.newAgent(t, "vie-bot", "vie")
	g.ent(t, Entitlement{Name: "viewer-infra", Principals: Principals{Agents: []string{"vie-bot"}},
		Resources: Resources{Collections: []string{"Infra"}}, Actions: []string{ActDescribe, ActRead}})

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{never.Title, "Someones card"}, Reason: "fix the dns outage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Granted) != 0 || out.Pending != nil || len(out.Refused) != 2 {
		t.Fatalf("both named items are hidden, got %+v", out)
	}
	for _, r := range out.Refused {
		if !r.NotFound || r.Reason != "there is no such item" {
			t.Fatalf("hidden items share one reason, got %+v", r)
		}
		if strings.Contains(r.Reason, never.Title) || strings.Contains(r.Reason, "Someones") {
			t.Fatalf("the reason does not name the item, got %+v", r)
		}
	}
	for _, r := range g.SearchForAgent("worker", "", "", nil) {
		if r.Item.ID == never.ID || r.Collection == "Personal" {
			t.Fatalf("search hides never and out-of-reach items, got %+v", r)
		}
	}
	if res := g.SearchForAgent("vie-bot", "", "", nil); len(res) != 0 {
		t.Fatalf("a viewer's agent does not see Infra the owner cannot read, got %d", len(res))
	}
	_ = infra
}

// TestGrantHeadReplayBlocked: putting an older grant row back, or a leftover the head does not
// name, does not restore a use.
func TestGrantHeadReplayBlocked(t *testing.T) {
	g := newGrantsVault(t)
	infra, _, _, token, _, _ := g.standardShape(t)
	if _, err := g.SetItem(g.owner, ItemIn{ID: token.ID, Collection: infra.Name, Level: LevelAuto,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}}); err != nil {
		t.Fatal(err)
	}
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns outage"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("the grant: %v %+v", err, out)
	}
	id := out.Granted[0].ID
	var snap []byte
	if err := g.st.Storage().View(func(tx kv.Tx) error {
		snap = append([]byte(nil), tx.Get(bucketGrants, []byte(id))...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := g.RevokeGrant(g.owner, id, "tighten"); err != nil {
		t.Fatal(err)
	}
	if err := g.st.Storage().Update(func(tx kv.Tx) error {
		return tx.Put(bucketGrants, []byte(id), snap)
	}); err != nil {
		t.Fatal(err)
	}
	if got := g.GrantsOf("worker"); len(got) != 0 && got[0].Status == GrantActive {
		t.Fatalf("replaying a revoked grant does not restore it, got %+v", got)
	}
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading it", "", "", "", ""); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("the replayed grant cannot be used, got %v", err)
	}
}
