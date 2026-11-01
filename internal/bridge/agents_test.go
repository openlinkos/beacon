package bridge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
)

// hookServer answers with the statuses in turn (the last one again once they run out) and keeps
// what it was sent.
type hookServer struct {
	mu       sync.Mutex
	statuses []int
	reqs     []*http.Request
	bodies   [][]byte
	srv      *httptest.Server
}

func newHookServer(t *testing.T, statuses ...int) *hookServer {
	h := &hookServer{statuses: statuses}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.reqs = append(h.reqs, r)
		h.bodies = append(h.bodies, b)
		code := h.statuses[min(len(h.reqs), len(h.statuses))-1]
		h.mu.Unlock()
		w.WriteHeader(code)
		if code >= 400 {
			_, _ = w.Write([]byte(`{"message":"Unexpected inputs provided: <at user_id=\"all\">"}`))
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func fastRetries(t *testing.T) {
	old := wakeBackoff
	wakeBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { wakeBackoff = old })
}

const hookSecret = "0123456789abcdef0123456789abcdef"

func webhookAgent(url string, resolved bool) config.Agent {
	return config.Agent{Name: "claude-oncall", Kind: "webhook", URL: url, Secret: hookSecret, Severities: []string{"critical"},
		Service: "git-.*", ServiceRe: regexp.MustCompile(`^(?:git-.*)$`), Resolved: resolved}
}

func TestSignature(t *testing.T) {
	body := []byte(`{"event":"firing"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(body)
	if got, want := signature("secret", body), "sha256="+hex.EncodeToString(mac.Sum(nil)); got != want {
		t.Fatalf("signature %s, want %s", got, want)
	}
	// a known value, so receivers in any language can check theirs
	if got := signature("It's a Secret to Everybody", []byte("Hello, World!")); got != "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17" {
		t.Fatalf("signature of the known example: %s", got)
	}
}

func TestWakeAgentRetries(t *testing.T) {
	fastRetries(t)
	e := newEnv(t)
	hs := newHookServer(t, 503, 503, 202)
	e.b.d.Config.PublicURL = "https://status.example.com"
	e.b.d.Config.Agents = []config.Agent{webhookAgent(hs.srv.URL+"/hook", false)}
	ctx := context.Background()
	if err := e.b.HandleWebhook(ctx, hook("firing", "critical", "f1")); err != nil {
		t.Fatal(err)
	}
	if len(e.fs.sends) != 1 {
		t.Fatal("the card goes out whatever the hook does")
	}
	e.b.waitWakes()
	id := GroupID(hook("firing", "critical").GroupKey)
	g, _ := e.st.Group(id)
	if len(g.Agents) != 1 || g.Agents[0].Status != "ok" || g.Agents[0].Attempts != 3 || g.Agents[0].Code != 202 || g.Agents[0].Error != "" {
		t.Fatalf("call: %+v", g.Agents)
	}
	if len(hs.reqs) != 3 {
		t.Fatalf("attempts: %d", len(hs.reqs))
	}
	for i, r := range hs.reqs {
		if r.Header.Get("X-Beacon-Signature") != signature(hookSecret, hs.bodies[i]) || r.Header.Get("X-Beacon-Event") != "firing" ||
			r.Header.Get("X-Beacon-Delivery") != g.Agents[0].Delivery || string(hs.bodies[i]) != string(hs.bodies[0]) {
			t.Fatalf("attempt %d: %v %s", i, r.Header, hs.bodies[i])
		}
	}
	var p HookPayload
	if err := json.Unmarshal(hs.bodies[0], &p); err != nil {
		t.Fatal(err)
	}
	if p.Alert.ID != id || p.Alert.Severity != "critical" || p.Alert.Service != "git-linkos-io" || p.Alert.Title != "git.linkos.io is down" ||
		p.Links.Evidence != "https://status.example.com/api/v1/alerts/"+id+"/evidence" || p.Links.MCP != "https://status.example.com/mcp" ||
		p.Hook != "claude-oncall" || !strings.Contains(p.Hint, "get_evidence") || len(p.Untrusted.Fields) == 0 || p.Alert.StartsAt.IsZero() {
		t.Fatalf("payload: %+v", p)
	}
	if c := cardText(e.fs.patches[len(e.fs.patches)-1]); !strings.Contains(c, "Agent claude-oncall woken at") {
		t.Errorf("the card shows the agent was woken: %s", c)
	}
	if last := e.bot.texts[len(e.bot.texts)-1]; last != `Woke agent claude-oncall for "git.linkos.io is down"` {
		t.Errorf("echo: %q", last)
	}
	if a := e.st.AuditLog(1); a[0].Action != "wake_agent" || a[0].Target != id || !strings.Contains(a[0].Detail, "claude-oncall (webhook, firing): ok, 3 attempts") {
		t.Errorf("audit: %+v", a)
	}
	if v, _ := e.b.Group(id); len(v.Agents) != 1 {
		t.Error("the API shows the calls")
	}
	// the same group again, still firing: nobody is woken twice
	_ = e.b.HandleWebhook(ctx, hook("firing", "critical", "f1"))
	e.b.waitWakes()
	if len(hs.reqs) != 3 {
		t.Errorf("woken again: %d", len(hs.reqs))
	}
}

func TestWakeAgentGivesUp(t *testing.T) {
	fastRetries(t)
	e := newEnv(t)
	refuses := newHookServer(t, 401)
	down := newHookServer(t, 502)
	e.b.d.Config.PublicURL = "https://status.example.com"
	a, b := webhookAgent(refuses.srv.URL, false), webhookAgent(down.srv.URL, false)
	b.Name = "second"
	e.b.d.Config.Agents = []config.Agent{a, b}
	_ = e.b.HandleWebhook(context.Background(), hook("firing", "critical", "f1"))
	e.b.waitWakes()
	g, _ := e.st.Group(GroupID(hook("firing", "critical").GroupKey))
	if len(g.Agents) != 2 || g.Agents[0].Status != "failed" || g.Agents[0].Attempts != 1 || g.Agents[0].Code != 401 {
		t.Fatalf("a refusal is not retried: %+v", g.Agents)
	}
	if !strings.Contains(g.Agents[0].Error, "Unexpected inputs provided") || g.Agents[1].Status != "failed" || g.Agents[1].Attempts != 5 {
		t.Fatalf("a server error is retried five times: %+v", g.Agents)
	}
	var failed []string
	for _, s := range e.bot.texts {
		if strings.HasPrefix(s, "Could not reach agent") {
			failed = append(failed, s)
		}
	}
	if len(failed) != 2 || strings.Contains(failed[0], "<at") || !strings.Contains(failed[0], "401 Unauthorized") {
		t.Errorf("failures echoed, without mentions: %q", failed)
	}
	if c := cardText(e.fs.patches[len(e.fs.patches)-1]); !strings.Contains(c, "Could not wake agent second") {
		t.Errorf("card: %s", c)
	}
}

func TestWhichHooksWake(t *testing.T) {
	fastRetries(t)
	e := newEnv(t)
	hs := newHookServer(t, 204)
	e.b.d.Config.PublicURL = "https://status.example.com"
	other := webhookAgent(hs.srv.URL, false)
	other.Name, other.Service, other.ServiceRe = "hub-only", "hub-.*", nil // compiled when needed
	warn := webhookAgent(hs.srv.URL, true)
	warn.Name, warn.Severities = "warnings", []string{"warning"}
	e.b.d.Config.Agents = []config.Agent{webhookAgent(hs.srv.URL, true), other, warn}
	ctx := context.Background()
	_ = e.b.HandleWebhook(ctx, hook("firing", "critical", "f1"))
	e.b.waitWakes()
	if len(hs.reqs) != 1 {
		t.Fatalf("only the hook for critical git-* alerts: %d calls", len(hs.reqs))
	}
	// resolving calls back the hook that was woken and asked to hear of the end
	_ = e.b.HandleWebhook(ctx, hook("resolved", "critical", "f1"))
	e.b.waitWakes()
	if len(hs.reqs) != 2 || hs.reqs[1].Header.Get("X-Beacon-Event") != "resolved" {
		t.Fatalf("resolved: %d calls", len(hs.reqs))
	}
	var p HookPayload
	_ = json.Unmarshal(hs.bodies[1], &p)
	if p.Event != "resolved" || p.Alert.Status != "resolved" || p.Alert.ResolvedAt == nil || p.Hint != hintResolved {
		t.Fatalf("resolved payload: %+v", p)
	}
	if !strings.Contains(e.bot.texts[len(e.bot.texts)-1], `Told agent claude-oncall that "git.linkos.io is down" resolved`) {
		t.Errorf("echo: %v", e.bot.texts)
	}
	// firing again is a new episode: the old calls and notes go, the hook is woken again
	e.now = e.now.Add(time.Hour)
	_ = e.b.HandleWebhook(ctx, hook("firing", "critical", "f9"))
	e.b.waitWakes()
	g, _ := e.st.Group(GroupID(hook("firing", "critical").GroupKey))
	if len(hs.reqs) != 3 || len(g.Agents) != 1 || g.Agents[0].Event != "firing" {
		t.Fatalf("new episode: %d calls, %+v", len(hs.reqs), g.Agents)
	}
}

func TestGitHubDispatch(t *testing.T) {
	fastRetries(t)
	e := newEnv(t)
	hs := newHookServer(t, 204)
	e.b.d.Config.Agents = []config.Agent{{Name: "ops", Kind: "github", Repository: "example/ops", Workflow: "on call.yml", Ref: "main",
		Secret: "github_pat_x", API: hs.srv.URL + "/", Severities: []string{"critical"}}}
	_ = e.b.HandleWebhook(context.Background(), hook("firing", "critical", "f1"))
	e.b.waitWakes()
	if len(hs.reqs) != 1 {
		t.Fatalf("calls: %d", len(hs.reqs))
	}
	r := hs.reqs[0]
	if r.URL.EscapedPath() != "/repos/example/ops/actions/workflows/on%20call.yml/dispatches" || r.Header.Get("Authorization") != "Bearer github_pat_x" ||
		r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-Beacon-Signature") != "" {
		t.Fatalf("request: %s %v", r.URL, r.Header)
	}
	id := GroupID(hook("firing", "critical").GroupKey)
	if string(hs.bodies[0]) != `{"ref":"main","inputs":{"alert":"`+id+`","event":"firing"}}` {
		t.Fatalf("body: %s", hs.bodies[0])
	}
}

func TestResumeWakes(t *testing.T) {
	fastRetries(t)
	e := newEnv(t)
	hs := newHookServer(t, 204)
	e.b.d.Config.PublicURL = "https://status.example.com"
	e.b.d.Config.Agents = []config.Agent{webhookAgent(hs.srv.URL, false)}
	g := store.Group{ID: "g1", Status: "firing", Severity: "critical", StartsAt: e.now.Add(-time.Hour), Labels: map[string]string{"alertname": "ServiceDown", "service": "git-a"},
		Agents: []store.AgentCall{
			{Hook: "claude-oncall", Kind: "webhook", Event: "firing", Delivery: "recent", Status: "pending", Attempts: 1, At: e.now.Add(-time.Minute)},
			{Hook: "claude-oncall", Kind: "webhook", Event: "firing", Delivery: "old", Status: "pending", At: e.now.Add(-time.Hour)},
			{Hook: "gone", Kind: "webhook", Event: "firing", Delivery: "gone", Status: "pending", At: e.now.Add(-time.Minute)},
			{Hook: "claude-oncall", Kind: "webhook", Event: "firing", Delivery: "done", Status: "ok", At: e.now.Add(-time.Minute)},
		}}
	if err := e.st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	e.b.resumeWakes(context.Background())
	e.b.waitWakes()
	g, _ = e.st.Group("g1")
	got := map[string]store.AgentCall{}
	for _, c := range g.Agents {
		got[c.Delivery] = c
	}
	if len(hs.reqs) != 1 || hs.reqs[0].Header.Get("X-Beacon-Delivery") != "recent" || got["recent"].Status != "ok" || got["recent"].Attempts != 2 {
		t.Fatalf("the recent call is taken up: %d calls, %+v", len(hs.reqs), got["recent"])
	}
	if got["old"].Status != "failed" || !strings.Contains(got["old"].Error, "restart") || got["gone"].Status != "failed" ||
		!strings.Contains(got["gone"].Error, "no longer configured") || got["done"].Status != "ok" {
		t.Fatalf("the others: %+v", got)
	}
}
