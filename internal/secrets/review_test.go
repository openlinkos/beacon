package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The review chain: rules, a review agent, then people. Each test is one of the acceptance
// cases — a rule that settles, deny first, person-level is people's, pass, the threshold,
// any denial ends the ask, the agent timeout, the secret.request hook, and get_request's
// context.

func TestRulesApproveAReviewAsk(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.setSetting(t, "review.rules", []ReviewRule{{Name: "always", Decide: "approve"}})

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Pending != nil {
		t.Fatalf("a matching approve settles a review-level ask at once, pending %+v", out.Pending)
	}
	if len(out.Granted) != 1 {
		t.Fatalf("the rules granted, got %+v", out)
	}
}

func TestRulesDenyBeatsApprove(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.setSetting(t, "review.rules", []ReviewRule{
		{Name: "always", Decide: "approve"},
		{Name: "forbidden", Decide: "deny", ReasonPattern: "forbidden"},
	})

	denied, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "forbidden by the window rules"})
	if err != nil {
		t.Fatal(err)
	}
	if denied.Pending != nil || len(denied.Granted) != 0 || len(denied.Refused) != 1 {
		t.Fatalf("a matching deny wins over any number of approves, got %+v", denied)
	}
	if !strings.Contains(denied.Refused[0].Reason, "forbidden") {
		t.Fatalf("the refusal names the rule, got %q", denied.Refused[0].Reason)
	}

	granted, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil {
		t.Fatal(err)
	}
	if granted.Pending != nil || len(granted.Granted) != 1 {
		t.Fatalf("without the deny pattern the approve still settles, got %+v", granted)
	}
}

func TestPersonLevelIsPeoples(t *testing.T) {
	g := newGrantsVault(t)
	_, _, prod, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.addReviewer(t, "reviewer", "own")
	g.setSetting(t, "review.rules", []ReviewRule{{Name: "always", Decide: "approve"}})

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{prod.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("person-level asks wait for people, got %v %+v", err, out)
	}
	if out.Pending.layer() != LayerPeople {
		t.Fatalf("person-level drops the agents layer and a rule approve is a pass, got layer %q chain %v",
			out.Pending.layer(), out.Pending.Chain)
	}
	if containsString(out.Pending.Chain, LayerAgents) {
		t.Fatalf("person-level asks are people's, chain %v", out.Pending.Chain)
	}
	if _, _, err := g.Decide(DecideActor{Name: "reviewer", Kind: "agent", Via: "mcp"},
		out.Pending.ID, true, "the review agent tries", nil, nil); err == nil {
		t.Fatal("a review agent does not approve a person-level ask")
	}
}

func TestPassMovesToPeople(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.addReviewer(t, "reviewer", "own")

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the request waits, got %v %+v", err, out)
	}
	if out.Pending.layer() != LayerAgents {
		t.Fatalf("with a review agent the ask stands at agents, got %q %v", out.Pending.layer(), out.Pending.Chain)
	}
	passed, err := g.Pass(DecideActor{Name: "reviewer", Kind: "agent", Via: "mcp"}, out.Pending.ID, "not mine to judge")
	if err != nil {
		t.Fatal(err)
	}
	if passed.layer() != LayerPeople {
		t.Fatalf("pass moves the ask to people, got %q", passed.layer())
	}
	if _, _, err := g.Decide(DecideActor{Name: "own", Kind: "person", Via: "console", Role: "owner"},
		passed.ID, true, "go ahead", nil, nil); err != nil {
		t.Fatalf("people decide after a pass, got %v", err)
	}
}

func TestApprovalsThresholdTwo(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.addReviewer(t, "reviewer", "own")
	g.setSetting(t, "review.approvals_required", 2)

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the request waits, got %v %+v", err, out)
	}
	mid, grant, err := g.Decide(DecideActor{Name: "reviewer", Kind: "agent", Via: "mcp"},
		out.Pending.ID, true, "the review agent agrees", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ID != "" || mid.Status != ReqPending || mid.Approvals != 1 || mid.layer() != LayerPeople {
		t.Fatalf("the first of two approvals moves the ask on, got status %s approvals %d layer %q grant %q",
			mid.Status, mid.Approvals, mid.layer(), grant.ID)
	}
	final, grant, err := g.Decide(DecideActor{Name: "own", Kind: "person", Via: "console", Role: "owner"},
		mid.ID, true, "people agree too", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != ReqGranted || grant.ID == "" {
		t.Fatalf("the second approval makes the grant, got status %s grant %q", final.Status, grant.ID)
	}
}

func TestAnyDenialEndsTheAsk(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.addReviewer(t, "reviewer", "own")
	g.setSetting(t, "review.approvals_required", 2)

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the request waits, got %v %+v", err, out)
	}
	denied, grant, err := g.Decide(DecideActor{Name: "reviewer", Kind: "agent", Via: "mcp"},
		out.Pending.ID, false, "this one is wrong", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if denied.Status != ReqDenied || grant.ID != "" {
		t.Fatalf("any denial ends the ask, got status %s grant %q", denied.Status, grant.ID)
	}
}

func TestAgentTimeoutDeny(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.addReviewer(t, "reviewer", "own")
	g.setSetting(t, "review.agent.timeout", "1s")
	g.setSetting(t, "review.agent.on_timeout", "deny")

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the request waits, got %v %+v", err, out)
	}
	g.clk.Add(2 * time.Second)
	if n := g.Sweep(); n == 0 {
		t.Fatal("the sweep applies the timeout")
	}
	cur, ok := g.Request(out.Pending.ID)
	if !ok || cur.Status != ReqDenied {
		t.Fatalf("the setting says deny, got ok=%v status %q", ok, cur.Status)
	}
}

func TestSecretRequestWakesTheReviewAgent(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.addReviewer(t, "reviewer", "own")

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the request waits, got %v %+v", err, out)
	}
	got := g.hooks.waitHooks(t, 1)
	found := false
	for _, e := range got {
		if e == "reviewer secret.request" {
			found = true
		}
		if strings.Contains(e, "worker secret.request") {
			t.Fatalf("the requester is not woken for its own ask, got %v", got)
		}
	}
	if !found {
		t.Fatalf("the review agent hears secret.request, got %v", got)
	}
}

func TestHookHTTPSignsAndTheStubDecides(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.addReviewer(t, "reviewer", "own")

	decided := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Beacon-Signature") != SecretHookSignature("hook-secret", body) {
			decided <- fmt.Errorf("bad signature %q", r.Header.Get("X-Beacon-Signature"))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Header.Get("X-Beacon-Event") != "secret.request" {
			w.WriteHeader(http.StatusOK)
			return
		}
		var p hookPayload
		if err := json.Unmarshal(body, &p); err != nil {
			decided <- err
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _, err := g.Decide(DecideActor{Name: "reviewer", Kind: "agent", Via: "hook"},
			p.Request, true, "the stub allows it", nil, nil)
		decided <- err
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	g.SetGrantsDeps(GrantsDeps{Hooks: &HookHTTP{URL: srv.URL, Secret: "hook-secret"}})

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the request waits at the agents layer, got %v %+v", err, out)
	}
	select {
	case err := <-decided:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stub did not hear the hook")
	}
	cur, ok := g.Request(out.Pending.ID)
	if !ok || cur.Status != ReqGranted {
		t.Fatalf("the stub's decide granted, got ok=%v status %q", ok, cur.Status)
	}
}

func TestReviewContextCarriesTheChain(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.addReviewer(t, "reviewer", "own")

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the request waits, got %v %+v", err, out)
	}
	ctx := g.ReviewContextOf(*out.Pending)
	if !containsString(ctx.Chain.Layers, LayerAgents) || ctx.Chain.At < 0 {
		t.Fatalf("the context names the chain, got %+v", ctx.Chain)
	}
	if !ctx.History.FirstAsk {
		t.Fatal("the first ask of the collection is a signal")
	}
	if len(ctx.Signals) == 0 {
		t.Fatal("the first-ask signal is raised")
	}
}

func TestRequesterCannotPassOwnAsk(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.reviewers["worker"] = true // even a requester that holds the review scope

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the request waits, got %v %+v", err, out)
	}
	if _, err := g.Pass(DecideActor{Name: "worker", Kind: "agent", Via: "mcp"}, out.Pending.ID, "passing my own"); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("the requester cannot pass its own ask, got %v", err)
	}
	if _, _, err := g.Decide(DecideActor{Name: "worker", Kind: "agent", Via: "mcp"},
		out.Pending.ID, true, "i allow this", nil, nil); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("the requester cannot review its own request, got %v", err)
	}
}

func TestPassNeedsAReason(t *testing.T) {
	g := newGrantsVault(t)
	_, plain, _, _, _, _ := g.standardShape(t)
	g.ent(t, oncallEnt())
	g.newAgent(t, "worker", "own")
	g.addReviewer(t, "reviewer", "own")

	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{plain.Title}, Reason: "fix the dns outage"})
	if err != nil || out.Pending == nil {
		t.Fatalf("the request waits, got %v %+v", err, out)
	}
	if _, err := g.Pass(DecideActor{Name: "reviewer", Kind: "agent", Via: "mcp"}, out.Pending.ID, "  "); !errors.Is(err, ErrBadInput) {
		t.Fatalf("a pass says why, got %v", err)
	}
}
