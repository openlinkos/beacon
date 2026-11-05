package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/store"
)

func (g *grantsVault) reopen(t *testing.T) {
	t.Helper()
	dir, mode := g.cfg.Dir, g.cfg.Mode
	_ = g.Close()
	s, err := Open(context.Background(), SealConfig{Mode: mode, Dir: dir},
		Deps{Store: g.st, Settings: g.sset, Lang: i18n.EN, Log: testLog(t), Now: g.clk.Now,
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
	g.Service = s
}

func TestReplayRevokedGrantStaysInactive(t *testing.T) {
	// red-before: putting a revoked grant row back, then an unrelated use, restored the grant.
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
	id := out.Granted[0].ID
	var old []byte
	_ = g.st.Storage().View(func(tx kv.Tx) error {
		old = append([]byte(nil), tx.Get(bucketGrants, []byte(id))...)
		return nil
	})
	if err := g.RevokeGrant(g.owner, id, "the drill is over"); err != nil {
		t.Fatal(err)
	}
	_ = g.st.Storage().Update(func(tx kv.Tx) error {
		return tx.Put(bucketGrants, []byte(id), old)
	})
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", ""); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("replaying a revoked grant must not restore it, got %v", err)
	}
	out, err = g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns again"})
	if err != nil || len(out.Granted) != 1 {
		t.Fatalf("an unrelated grant: %v %+v", err, out)
	}
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	// the replayed revoked row is still not the live grant
	live, ok := g.activeGrant("worker", token.ID, "", ActRead)
	if !ok || live.ID == id {
		t.Fatalf("the live grant is the new one, got %+v ok=%v", live, ok)
	}
	g.reopen(t)
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	live, ok = g.activeGrant("worker", token.ID, "", ActRead)
	if !ok || live.ID == id {
		t.Fatalf("after reopen the revoked row is still gone, got %+v", live)
	}
}

func TestDeletedDenyEntitlementStaysDeny(t *testing.T) {
	// red-before: deleting a deny row, then SetAgentTags, washed the deny out of a scan-rebuilt head.
	g := newGrantsVault(t)
	infra, _, prod, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.ent(t, Entitlement{Name: "no-prod", Deny: true,
		Principals: Principals{Agents: []string{"worker"}},
		Resources:  Resources{Tags: []string{"env/prod"}}})
	g.newAgent(t, "worker", "own")
	agent, _ := g.agentInfoOf("worker")
	if d := g.decidePolicy(agent, prod, infra, ActRead, ""); d.OK {
		t.Fatalf("the deny holds, got %+v", d)
	}
	_ = g.st.Storage().Update(func(tx kv.Tx) error {
		return tx.Delete(bucketEntitlements, []byte("no-prod"))
	})
	if err := g.SetAgentTags(g.owner, "worker", []string{"role/on-call"}); err != nil {
		t.Fatal(err)
	}
	if d := g.decidePolicy(agent, prod, infra, ActRead, ""); d.OK {
		t.Fatalf("after an unrelated write the deny still holds, got %+v", d)
	}
	if !g.bot.heard("strictest") && len(g.Bad()) == 0 {
		t.Error("the missing deny was reported")
	}
	g.reopen(t)
	agent, _ = g.agentInfoOf("worker")
	if d := g.decidePolicy(agent, prod, infra, ActRead, ""); d.OK {
		t.Fatalf("after reopen the deny still holds, got %+v", d)
	}
}

func TestCorruptEligibilityHeadRefusesWrite(t *testing.T) {
	g := newGrantsVault(t)
	g.ent(t, oncallEnt())
	_ = g.st.Storage().Update(func(tx kv.Tx) error {
		return tx.Delete(bucketEntitlements, headKey)
	})
	if err := g.SetAgentTags(g.owner, "worker", []string{"role/on-call"}); !errors.Is(err, ErrHead) {
		t.Fatalf("an unrelated eligibility write must refuse while the head is missing, got %v", err)
	}
	if _, err := g.SetEntitlement(g.owner, Entitlement{Name: "later", Principals: Principals{Agents: []string{"worker"}},
		Resources: Resources{Collections: []string{"Infra"}}, Actions: []string{ActDescribe}}); !errors.Is(err, ErrHead) {
		t.Fatalf("a later entitlement write still refuses, got %v", err)
	}
	g.reopen(t)
	if err := g.SetAgentTags(g.owner, "worker", []string{"role/on-call"}); !errors.Is(err, ErrHead) {
		t.Fatalf("open again still refuses, got %v", err)
	}
}

func TestCorruptGrantsHeadRefusesWrite(t *testing.T) {
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
	_ = g.st.Storage().Update(func(tx kv.Tx) error {
		return tx.Delete(bucketGrants, headKey)
	})
	if _, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns again"}); err == nil || !errors.Is(err, ErrHead) {
		t.Fatalf("an unrelated grant write must refuse while the head is missing, got %v", err)
	}
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", ""); !errors.Is(err, ErrNoGrant) && !errors.Is(err, ErrHead) {
		t.Fatalf("a use against a missing grants head is not active, got %v", err)
	}
	g.reopen(t)
	if _, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{token.Title}, Reason: "fix the dns later"}); err == nil || !errors.Is(err, ErrHead) {
		t.Fatalf("open again still refuses, got %v", err)
	}
}

func TestStaleEligibilityHeadAndRowsAreFailsafe(t *testing.T) {
	g := newGrantsVault(t)
	infra, _, prod, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.ent(t, Entitlement{Name: "no-prod", Deny: true,
		Principals: Principals{Agents: []string{"worker"}},
		Resources:  Resources{Tags: []string{"env/prod"}}})
	g.newAgent(t, "worker", "own")
	var oldHead, oldRow []byte
	_ = g.st.Storage().View(func(tx kv.Tx) error {
		oldHead = append([]byte(nil), tx.Get(bucketEntitlements, headKey)...)
		oldRow = append([]byte(nil), tx.Get(bucketEntitlements, []byte("no-prod"))...)
		return nil
	})
	if err := g.DeleteEntitlement(g.owner, "no-prod"); err != nil {
		t.Fatal(err)
	}
	_ = g.st.Storage().Update(func(tx kv.Tx) error {
		if err := tx.Put(bucketEntitlements, headKey, oldHead); err != nil {
			return err
		}
		return tx.Put(bucketEntitlements, []byte("no-prod"), oldRow)
	})
	agent, _ := g.agentInfoOf("worker")
	if d := g.decidePolicy(agent, prod, infra, ActRead, ""); d.OK {
		t.Fatalf("stale deny pair must stay deny, got %+v", d)
	}
	g.reopen(t)
	agent, _ = g.agentInfoOf("worker")
	if d := g.decidePolicy(agent, prod, infra, ActRead, ""); d.OK {
		t.Fatalf("open again: stale deny pair must stay deny, got %+v", d)
	}
}

func TestStaleGrantsHeadAndRowsAreFailsafe(t *testing.T) {
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
	id := out.Granted[0].ID
	var oldHead, oldRow []byte
	_ = g.st.Storage().View(func(tx kv.Tx) error {
		oldHead = append([]byte(nil), tx.Get(bucketGrants, headKey)...)
		oldRow = append([]byte(nil), tx.Get(bucketGrants, []byte(id))...)
		return nil
	})
	if err := g.RevokeGrant(g.owner, id, "done"); err != nil {
		t.Fatal(err)
	}
	_ = g.st.Storage().Update(func(tx kv.Tx) error {
		if err := tx.Put(bucketGrants, headKey, oldHead); err != nil {
			return err
		}
		return tx.Put(bucketGrants, []byte(id), oldRow)
	})
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", ""); !errors.Is(err, ErrNoGrant) && !errors.Is(err, ErrHead) {
		t.Fatalf("stale grant pair must not restore the grant, got %v", err)
	}
	g.reopen(t)
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", ""); !errors.Is(err, ErrNoGrant) && !errors.Is(err, ErrHead) {
		t.Fatalf("open again: stale grant pair must not restore the grant, got %v", err)
	}
}

func TestTamperEligibilityThenUnrelatedWriteStaysInvalid(t *testing.T) {
	g := newGrantsVault(t)
	infra, _, prod, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.ent(t, Entitlement{Name: "no-prod", Deny: true,
		Principals: Principals{Agents: []string{"worker"}},
		Resources:  Resources{Tags: []string{"env/prod"}}})
	g.newAgent(t, "worker", "own")
	_ = g.st.Storage().Update(func(tx kv.Tx) error {
		v := tx.Get(bucketEntitlements, []byte("no-prod"))
		v[len(v)-1] ^= 0xff
		return tx.Put(bucketEntitlements, []byte("no-prod"), v)
	})
	agent, _ := g.agentInfoOf("worker")
	if d := g.decidePolicy(agent, prod, infra, ActRead, ""); d.OK {
		t.Fatalf("tampered deny stays deny, got %+v", d)
	}
	if err := g.SetAgentTags(g.owner, "worker", []string{"role/on-call"}); err != nil {
		t.Fatal(err)
	}
	if d := g.decidePolicy(agent, prod, infra, ActRead, ""); d.OK {
		t.Fatalf("after an unrelated write the tamper still stays deny, got %+v", d)
	}
	if !g.bot.heard("strictest") && len(g.Bad()) == 0 {
		t.Error("the tamper was reported")
	}
	g.reopen(t)
	agent, _ = g.agentInfoOf("worker")
	if d := g.decidePolicy(agent, prod, infra, ActRead, ""); d.OK {
		t.Fatalf("after reopen the tamper still stays deny, got %+v", d)
	}
}

func TestRebuildEligibilityAndGrantsHeadsRestoresAccept(t *testing.T) {
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
	_ = g.st.Storage().Update(func(tx kv.Tx) error {
		if err := tx.Delete(bucketEntitlements, headKey); err != nil {
			return err
		}
		return tx.Delete(bucketGrants, headKey)
	})
	g.reopen(t)
	if err := g.SetAgentTags(g.owner, "worker", []string{"role/on-call"}); !errors.Is(err, ErrHead) {
		t.Fatalf("without rebuild eligibility writes refuse, got %v", err)
	}
	if err := g.RebuildHead(g.owner); err != nil {
		t.Fatal(err)
	}
	if err := g.SetAgentTags(g.owner, "worker", []string{"role/on-call"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", ""); err != nil {
		t.Fatalf("after rebuild the grant is active, got %v", err)
	}
	entries, _, err := g.st.AuditQuery(store.AuditFilter{Action: "secrets_head_rebuild"})
	if err != nil || len(entries) == 0 {
		t.Fatalf("rebuild not audited: %+v %v", entries, err)
	}
}

func TestGetSecretDoesNotNameSneakedGrant(t *testing.T) {
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
	live := out.Granted[0]
	sk, err := g.sk()
	if err != nil {
		t.Fatal(err)
	}
	sneak := live
	sneak.ID = "deadbeefdeadbeef"
	sneak.Sig = signRow(sk, sneak.signedParts()...)
	b, err := json.Marshal(sneak)
	if err != nil {
		t.Fatal(err)
	}
	_ = g.st.Storage().Update(func(tx kv.Tx) error {
		return tx.Put(bucketGrants, []byte(sneak.ID), b)
	})
	if _, err := g.GetSecret("worker", "test", "Infra/"+token.Title, "password", "reading", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	named := false
	g.grants(func(gr Grant) bool {
		if gr.ID == sneak.ID {
			named = true
		}
		return true
	})
	if named {
		t.Fatal("a sneaked grant row must not become active after get_secret")
	}
	if _, ok := g.activeGrant("worker", token.ID, "", ActRead); !ok {
		t.Fatal("the real grant is still active")
	}
}

func (p *poster) heard(sub string) bool {
	for _, m := range p.all() {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}
