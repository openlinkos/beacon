package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/feishu"
	"github.com/openlinkos/beacon/internal/notify"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/vm"
)

type fakeFeishu struct {
	mu      sync.Mutex
	sends   []feishu.Card
	patches []feishu.Card
	buzzes  [][]string
	fail    bool
	count   func()
}

func (f *fakeFeishu) SendCard(_ context.Context, chat string, c feishu.Card, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count()
	if f.fail {
		return "", errors.New("feishu down")
	}
	f.sends = append(f.sends, c)
	return fmt.Sprintf("om_%d", len(f.sends)), nil
}

func (f *fakeFeishu) PatchCard(_ context.Context, id string, c feishu.Card) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count()
	f.patches = append(f.patches, c)
	return nil
}

func (f *fakeFeishu) UrgentApp(_ context.Context, id string, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count()
	f.buzzes = append(f.buzzes, ids)
	return nil
}

type fakeAM struct {
	created []map[string]string
	expired []string
}

func (a *fakeAM) CreateSilence(_ context.Context, l map[string]string, s, e time.Time, by, c string) (string, error) {
	a.created = append(a.created, l)
	return fmt.Sprintf("sil-%d", len(a.created)), nil
}
func (a *fakeAM) ExpireSilence(_ context.Context, id string) error {
	a.expired = append(a.expired, id)
	return nil
}
func (a *fakeAM) Silences(context.Context) ([]alertmanager.Silence, error) {
	return []alertmanager.Silence{{ID: "sil-1"}}, nil
}

type fakePush struct {
	sent      []notify.Emergency
	cancelled []string
	acked     bool
	fail      bool
}

func (p *fakePush) Send(_ context.Context, m notify.Emergency) (string, error) {
	p.sent = append(p.sent, m)
	if p.fail {
		return "", errors.New("pushover unavailable")
	}
	return "rc1", nil
}
func (p *fakePush) Receipt(context.Context, string) (notify.Receipt, error) {
	return notify.Receipt{Acknowledged: p.acked, Device: "iphone"}, nil
}
func (p *fakePush) CancelByTag(_ context.Context, tag string) error {
	p.cancelled = append(p.cancelled, tag)
	return nil
}

type fakeBot struct{ texts []string }

func (b *fakeBot) Send(_ context.Context, t string) error { b.texts = append(b.texts, t); return nil }

type fakeVM struct{}

func (fakeVM) Query(context.Context, string) ([]vm.Sample, error) {
	return []vm.Sample{
		{Labels: map[string]string{"location": "us-los-angeles"}, Value: 0},
		{Labels: map[string]string{"location": "cn-shanghai-bgp"}, Value: 1},
	}, nil
}

type env struct {
	b    *Bridge
	fs   *fakeFeishu
	am   *fakeAM
	push *fakePush
	bot  *fakeBot
	st   *store.Store
	now  time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	cfg := &config.Config{
		Language: "en", Location: loc, AckTTL: config.Duration(4 * time.Hour),
		Feishu:     config.Feishu{ChatID: "oc_1", MonthlyBudget: 1000, OnCall: []config.OnCaller{{OpenID: "ou_a", Name: "Alice"}}},
		OnCallByID: map[string]config.OnCaller{"ou_a": {OpenID: "ou_a", Name: "Alice"}},
		Escalation: map[string]config.Policy{
			"critical": {Channel: "app", Buzz: config.Duration(10 * time.Minute), Pushover: config.Duration(15 * time.Minute)},
			"warning":  {Channel: "app"},
			"info":     {Channel: "webhook"},
		},
	}
	cfg.Links.Runbook = "https://ops.example/runbooks/{alertname}.md"
	cfg.Links.GrafanaService = "https://grafana.example/d/svc?var-service={service}"
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{fs: &fakeFeishu{}, am: &fakeAM{}, push: &fakePush{}, bot: &fakeBot{}, st: st, now: time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)}
	cat := &catalog.Catalog{Locations: []catalog.Location{{ID: "us-los-angeles", Name: "United States - Los Angeles", Region: "overseas"}}}
	e.b = New(Deps{Config: cfg, Store: st, AM: e.am, Feishu: e.fs, Pushover: e.push, Bot: e.bot, VM: fakeVM{},
		Catalog: func() *catalog.Catalog { return cat }, Now: func() time.Time { return e.now }})
	e.fs.count = e.b.CountFeishuCall
	return e
}

func hook(status, sev string, fps ...string) alertmanager.Webhook {
	w := alertmanager.Webhook{Version: "4", GroupKey: `{}:{alertname="ServiceDown",service="git-example-com"}`, Status: status,
		GroupLabels:       map[string]string{"alertname": "ServiceDown", "service": "git-example-com"},
		CommonLabels:      map[string]string{"alertname": "ServiceDown", "service": "git-example-com", "severity": sev},
		CommonAnnotations: map[string]string{"summary": "git.example.com is down", "description": "No answer from 2 locations"}}
	for _, fp := range fps {
		w.Alerts = append(w.Alerts, alertmanager.Alert{Status: status, Fingerprint: fp, StartsAt: time.Date(2026, 10, 30, 1, 58, 0, 0, time.UTC)})
	}
	return w
}

func cardText(c feishu.Card) string { b, _ := json.Marshal(c); return string(b) }

func TestFiringAckResolve(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.b.HandleWebhook(ctx, hook("firing", "critical", "f1")); err != nil {
		t.Fatal(err)
	}
	if len(e.fs.sends) != 1 {
		t.Fatalf("one card expected, got %d", len(e.fs.sends))
	}
	c := cardText(e.fs.sends[0])
	for _, want := range []string{"git.example.com is down", `"template":"red"`, "Acknowledge", "Silence 4h", "❌ us-los-angeles", "✅ cn-shanghai-bgp", "runbooks/ServiceDown.md", "var-service=git-example-com", "10-30 09:58"} {
		if !strings.Contains(c, want) {
			t.Errorf("card lacks %q: %s", want, c)
		}
	}
	id := GroupID(hook("firing", "critical").GroupKey)

	// a repeat with the same alerts does not touch the card
	e.now = e.now.Add(6 * time.Minute)
	_ = e.b.HandleWebhook(ctx, hook("firing", "critical", "f1"))
	if len(e.fs.patches) != 0 {
		t.Error("an unchanged group should not be patched")
	}
	// a new alert in the group updates it (more than five minutes after the card)
	_ = e.b.HandleWebhook(ctx, hook("firing", "critical", "f1", "f2"))
	if len(e.fs.patches) != 1 || !strings.Contains(cardText(e.fs.patches[0]), "2 firing") {
		t.Fatalf("patch expected with 2 alerts, got %d", len(e.fs.patches))
	}

	// a stranger cannot act; the on-call person can
	if r := e.b.HandleAction(ctx, feishu.Action{OpenID: "ou_x", Value: map[string]any{"a": "ack", "g": id}}); r.ToastKind != "error" || r.Card != nil {
		t.Errorf("stranger: %+v", r)
	}
	r := e.b.HandleAction(ctx, feishu.Action{OpenID: "ou_a", Value: map[string]any{"a": "note", "g": id}, Form: map[string]any{"note": " restarting gitea "}})
	if r.ToastKind != "success" || r.Card == nil {
		t.Fatalf("ack: %+v", r)
	}
	if c := cardText(r.Card); !strings.Contains(c, "Acknowledged by Alice") || !strings.Contains(c, "restarting gitea") || strings.Contains(c, `"a":"ack"`) || !strings.Contains(c, `"template":"yellow"`) {
		t.Errorf("acknowledged card: %s", c)
	}
	// acknowledged: no escalation even after the delays
	e.now = e.now.Add(20 * time.Minute)
	e.b.Tick(ctx)
	if len(e.fs.buzzes) != 0 || len(e.push.sent) != 0 {
		t.Error("an acknowledged alert escalated")
	}

	// resolved: the card turns green
	e.now = e.now.Add(time.Minute)
	_ = e.b.HandleWebhook(ctx, hook("resolved", "critical", "f1", "f2"))
	last := cardText(e.fs.patches[len(e.fs.patches)-1])
	if !strings.Contains(last, `"template":"green"`) || !strings.Contains(last, "Resolved at") || strings.Contains(last, `"a":"silence"`) {
		t.Errorf("resolved card: %s", last)
	}
	if r := e.b.HandleAction(ctx, feishu.Action{OpenID: "ou_a", Value: map[string]any{"a": "ack", "g": id}}); r.Toast != "This alert has already resolved" {
		t.Errorf("click after resolve: %+v", r)
	}
	if got := e.st.AuditLog(5); len(got) != 1 || got[0].Actor != "Alice" || got[0].Action != "note" {
		t.Errorf("audit: %+v", got)
	}
}

func TestEscalation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_ = e.b.HandleWebhook(ctx, hook("firing", "critical", "f1"))
	id := GroupID(hook("firing", "critical").GroupKey)

	e.now = e.now.Add(9 * time.Minute)
	e.b.Tick(ctx)
	if len(e.fs.buzzes) != 0 {
		t.Fatal("buzzed too early")
	}
	e.now = e.now.Add(2 * time.Minute)
	e.b.Tick(ctx)
	if len(e.fs.buzzes) != 1 || e.fs.buzzes[0][0] != "ou_a" {
		t.Fatalf("buzz at 10 minutes: %v", e.fs.buzzes)
	}
	e.b.Tick(ctx)
	if len(e.fs.buzzes) != 1 {
		t.Fatal("buzzed twice")
	}
	e.now = e.now.Add(5 * time.Minute)
	e.b.Tick(ctx)
	if len(e.push.sent) != 1 || e.push.sent[0].Tag != id || !strings.Contains(e.push.sent[0].Title, "Critical: git.example.com is down") {
		t.Fatalf("pushover at 15 minutes: %+v", e.push.sent)
	}
	// acknowledged in Pushover → acknowledged here
	e.push.acked = true
	e.now = e.now.Add(31 * time.Second)
	e.b.Tick(ctx)
	g, _ := e.st.Group(id)
	if g.Ack == nil || g.Ack.Via != "pushover" || !strings.Contains(g.Ack.By, "iphone") {
		t.Fatalf("pushover ack: %+v", g.Ack)
	}
	if c := cardText(e.fs.patches[len(e.fs.patches)-1]); !strings.Contains(c, "via Pushover") {
		t.Errorf("card after pushover ack: %s", c)
	}

	// the acknowledgement lapses after four hours while still firing: escalation restarts
	e.now = e.now.Add(4*time.Hour + time.Minute)
	e.b.Tick(ctx)
	g, _ = e.st.Group(id)
	if g.Ack != nil || !g.Buzzed.IsZero() || !g.Pushed.IsZero() {
		t.Fatalf("escalation did not restart: %+v", g)
	}
	e.now = e.now.Add(11 * time.Minute)
	e.b.Tick(ctx)
	if len(e.fs.buzzes) != 2 {
		t.Errorf("second buzz expected, got %d", len(e.fs.buzzes))
	}
}

func TestCriticalEscalationWithoutHumanAcknowledgement(t *testing.T) {
	fastRetries(t)
	for _, tc := range []struct {
		name   string
		status int
		ack    bool
	}{
		{name: "no assigned hook"},
		{name: "hook accepted without activity", status: 202},
		{name: "hook failed", status: 503},
		{name: "agent acknowledged but did not restore service", status: 202, ack: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			if tc.status != 0 {
				hs := newHookServer(t, tc.status)
				e.b.d.Config.Agents = []config.Agent{webhookAgent(hs.srv.URL+"/hook", false)}
			}
			ctx := context.Background()
			w := hook("firing", "critical", "f1")
			if err := e.b.HandleWebhook(ctx, w); err != nil {
				t.Fatal(err)
			}
			e.b.waitWakes()
			if len(e.fs.sends) != 1 {
				t.Fatal("chat was not attempted independently of the hook")
			}
			id := GroupID(w.GroupKey)
			group, _ := e.st.Group(id)
			switch tc.status {
			case 0:
				if len(group.Agents) != 0 {
					t.Fatalf("a hook was called without an assignment: %+v", group.Agents)
				}
			case 202:
				if len(group.Agents) != 1 || group.Agents[0].Status != "ok" || group.Agents[0].Code != 202 {
					t.Fatalf("test hook did not accept the alert: %+v", group.Agents)
				}
			case 503:
				if len(group.Agents) != 1 || group.Agents[0].Status != "failed" || group.Agents[0].Attempts < 2 {
					t.Fatalf("test hook did not fail all its attempts: %+v", group.Agents)
				}
			}
			if tc.ack {
				who := ActorOf(store.Principal{User: "alex", Agent: "oncall"}, "mcp")
				ack, err := e.b.Ack(ctx, id, who, "looking into it")
				if err != nil || ack.Ack == nil || ack.Ack.Kind != "agent" || ack.Ack.StopsEscalation() {
					t.Fatalf("Agent acknowledgement: %+v, %v", ack.Ack, err)
				}
			}
			e.now = e.now.Add(15 * time.Minute)
			e.push.fail = true
			e.b.Tick(ctx)
			timeline, err := e.b.Timeline(ctx, id, "")
			if err != nil {
				t.Fatal(err)
			}
			var failed bool
			for _, event := range timeline.Events {
				if event.Kind == "pushover" {
					t.Fatal("failed request recorded as an accepted Pushover message")
				}
				failed = failed || (event.Kind == "notification" && event.Via == "pushover" && event.Result == "failed")
			}
			if !failed {
				t.Fatal("missed the Pushover failure in the timeline")
			}
			e.push.fail = false
			e.b.Tick(ctx)
			if len(e.push.sent) != 1 {
				t.Fatal("an immediate retry would page on every tick")
			}
			e.now = e.now.Add(time.Minute)
			e.b.Tick(ctx)
			g, _ := e.st.Group(id)
			if len(e.push.sent) != 2 || g.Receipt == "" || g.Ack.StopsEscalation() || len(e.push.cancelled) != 0 {
				t.Fatalf("unclaimed alert did not retry, or acceptance became acknowledgement: %+v", g)
			}
			timeline, err = e.b.Timeline(ctx, id, "")
			if err != nil {
				t.Fatal(err)
			}
			var accepted bool
			for _, event := range timeline.Events {
				accepted = accepted || (event.Kind == "pushover" && event.Result == "ok")
			}
			if !accepted {
				t.Fatal("missed the Pushover API acceptance in the timeline")
			}
			e.push.acked = true
			e.now = e.now.Add(receiptEvery)
			e.b.Tick(ctx)
			g, _ = e.st.Group(id)
			if !g.Ack.StopsEscalation() || g.Ack.Kind != "person" || g.Ack.Via != "pushover" {
				t.Fatalf("phone acknowledgement did not establish human ownership: %+v", g.Ack)
			}
		})
	}
}

func TestLegacyAcknowledgementAndFailedPage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w := hook("firing", "critical", "f1")
	if err := e.b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	id := GroupID(w.GroupKey)
	g, _ := e.st.Group(id)
	g.Pushed = e.now
	g.Ack = &store.Ack{By: "previous responder", Via: "mcp", At: e.now}
	if err := e.st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(15 * time.Minute)
	e.b.Tick(ctx)
	view, err := e.b.Group(id)
	if err != nil || view.Pushed != nil || !view.Ack.StopsEscalation() || len(e.push.sent) != 0 {
		t.Fatalf("legacy failed attempt or acknowledgement was misclassified: %+v, %v", view, err)
	}
}

func TestExplicitSilenceAfterAgentAcknowledgement(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w := hook("firing", "critical", "f1")
	if err := e.b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	id := GroupID(w.GroupKey)
	who := ActorOf(store.Principal{User: "alex", Agent: "oncall"}, "mcp")
	e.now = e.now.Add(15 * time.Minute)
	e.b.Tick(ctx)
	if _, err := e.b.Ack(ctx, id, who, "looking into it"); err != nil {
		t.Fatal(err)
	}
	if len(e.push.sent) != 1 || len(e.push.cancelled) != 0 {
		t.Fatal("an Agent acknowledgement cancelled the emergency page")
	}
	silenced, err := e.b.Silence(ctx, id, time.Hour, who, "explicit maintenance")
	if err != nil || silenced.Silence == nil || len(e.push.cancelled) != 1 {
		t.Fatalf("explicit silence did not pause the page: %+v, %v", silenced.Silence, err)
	}
	e.now = e.now.Add(40 * time.Minute)
	e.b.Tick(ctx)
	if len(e.push.sent) != 1 {
		t.Fatal("page sent during explicit silence")
	}
	if err := e.b.ExpireSilence(ctx, silenced.Silence.ID, Actor{Name: "alex", Via: "console", Key: "user:alex"}); err != nil {
		t.Fatal(err)
	}
	e.b.Tick(ctx)
	if len(e.push.sent) != 2 {
		t.Fatal("escalation did not resume when explicit silence ended")
	}
}

func TestSilenceFromCard(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_ = e.b.HandleWebhook(ctx, hook("firing", "critical", "f1"))
	id := GroupID(hook("firing", "critical").GroupKey)
	e.now = e.now.Add(16 * time.Minute)
	e.b.Tick(ctx) // buzz and pushover
	r := e.b.HandleAction(ctx, feishu.Action{OpenID: "ou_a", Value: map[string]any{"a": "silence", "g": id, "d": "4h"}})
	if r.ToastKind != "success" || len(e.am.created) != 1 || e.am.created[0]["service"] != "git-example-com" {
		t.Fatalf("silence: %+v %v", r, e.am.created)
	}
	if len(e.push.cancelled) != 1 || e.push.cancelled[0] != id {
		t.Errorf("silencing should stop Pushover's retries: %v", e.push.cancelled)
	}
	c := cardText(r.Card)
	if !strings.Contains(c, "Silenced until") || strings.Contains(c, `"a":"silence"`) {
		t.Errorf("silenced card: %s", c)
	}
	if r := e.b.HandleAction(ctx, feishu.Action{OpenID: "ou_a", Value: map[string]any{"a": "silence", "g": id, "d": "48h"}}); r.ToastKind != "error" {
		t.Errorf("a 48h silence should be refused: %+v", r)
	}
}

func TestRoutingAndFallbacks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// info goes to the group bot
	w := hook("firing", "info", "f1")
	w.GroupKey = "info-group"
	_ = e.b.HandleWebhook(ctx, w)
	if len(e.bot.texts) != 1 || !strings.Contains(e.bot.texts[0], "Firing (Info)") || len(e.fs.sends) != 0 {
		t.Fatalf("info routing: %v / %d cards", e.bot.texts, len(e.fs.sends))
	}
	w.Status = "resolved"
	_ = e.b.HandleWebhook(ctx, w)
	if len(e.bot.texts) != 2 || !strings.Contains(e.bot.texts[1], "Resolved:") {
		t.Errorf("info resolved: %v", e.bot.texts)
	}
	// the Watchdog only proves the pipeline works
	_ = e.b.HandleWebhook(ctx, alertmanager.Webhook{Version: "4", GroupKey: "wd", Status: "firing", GroupLabels: map[string]string{"alertname": "Watchdog"}})
	if ok, why := e.b.Ready(); !ok {
		t.Errorf("ready after the Watchdog: %v", why)
	}
	// the app fails: a critical alert goes to the bot and straight to Pushover
	e.fs.fail = true
	_ = e.b.HandleWebhook(ctx, hook("firing", "critical", "f1"))
	if len(e.push.sent) != 1 || len(e.bot.texts) != 3 {
		t.Fatalf("fallback: pushover %d, bot %v", len(e.push.sent), e.bot.texts)
	}
	if g, ok := e.st.Group(GroupID(hook("firing", "critical", "f1").GroupKey)); !ok ||
		latestNotification(&g, "app").Result != "failed" || g.Channel != "webhook" {
		t.Fatalf("the failed initial card and accepted fallback were not recorded: %+v, exists=%v", g, ok)
	}
	if ok, why := e.b.Ready(); ok || !strings.Contains(strings.Join(why, ";"), "Feishu API call failed") {
		t.Errorf("not ready after a Feishu failure: %v", why)
	}
	// a warning over 70% of the budget goes to the bot
	e.fs.fail = false
	e.st.Add(e.b.quotaKey(), 800)
	w2 := hook("firing", "warning", "x")
	w2.GroupKey = "warn-group"
	_ = e.b.HandleWebhook(ctx, w2)
	if len(e.fs.sends) != 0 || !strings.Contains(e.bot.texts[len(e.bot.texts)-1], "Firing (Warning)") {
		t.Errorf("budget routing: cards %d, bot %v", len(e.fs.sends), e.bot.texts)
	}
}

func TestReadyWithoutWatchdog(t *testing.T) {
	e := newEnv(t)
	if ok, _ := e.b.Ready(); !ok {
		t.Error("ready during the grace period")
	}
	e.now = e.now.Add(4 * time.Minute)
	if ok, why := e.b.Ready(); ok || !strings.Contains(why[0], "Watchdog") {
		t.Errorf("not ready without a Watchdog: %v", why)
	}
}

func TestReadyAndHumanDeliveryConfiguration(t *testing.T) {
	e := newEnv(t)
	e.b.d.Feishu, e.b.d.Bot, e.b.d.Pushover = nil, nil, nil
	if s := e.b.Summarize(); !s.Ready || s.Delivery.CriticalToPeople {
		t.Fatalf("Watchdog grace period with no delivery route: %+v", s)
	}
	e.b.d.Bot = e.bot
	if d := e.b.Delivery(); !d.CriticalToPeople || !d.GroupBot || d.FeishuApp || d.Pushover {
		t.Fatalf("group bot delivery route: %+v", d)
	}
}

type routeTestBark struct{ sent int }

func (b *routeTestBark) Send(context.Context, string, string, string) error {
	b.sent++
	return nil
}

func TestCriticalDeliveryWithoutInitialChat(t *testing.T) {
	e := newEnv(t)
	bark := &routeTestBark{}
	e.b.d.Bark = bark
	e.b.d.Config.Escalation["critical"] = config.Policy{Channel: "none"}
	if d := e.b.Delivery(); d.CriticalToPeople || d.CriticalChat != "none" || len(d.CriticalPhone) != 0 ||
		!d.FeishuApp || !d.GroupBot || !d.Pushover || !d.Bark {
		t.Fatalf("available providers changed the record-only policy: %+v", d)
	}

	ctx := context.Background()
	recorded := hook("firing", "critical", "recorded")
	recorded.GroupKey = "recorded-only"
	if err := e.b.HandleWebhook(ctx, recorded); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(30 * time.Minute)
	e.b.Tick(ctx)
	if g, ok := e.st.Group(GroupID(recorded.GroupKey)); !ok || !g.Firing() || g.Channel != "none" {
		t.Fatalf("recorded alert: %+v, exists=%v", g, ok)
	}
	if len(e.fs.sends) != 0 || len(e.bot.texts) != 0 || len(e.push.sent) != 0 || bark.sent != 0 {
		t.Fatalf("record-only delivery: %d cards, %d bot messages, %d Pushover alerts, %d Bark alerts",
			len(e.fs.sends), len(e.bot.texts), len(e.push.sent), bark.sent)
	}

	recorded.Status = "resolved"
	if err := e.b.HandleWebhook(ctx, recorded); err != nil {
		t.Fatal(err)
	}
	e.b.d.Config.Escalation["critical"] = config.Policy{Channel: "none", Bark: config.Duration(time.Minute)}
	if d := e.b.Delivery(); !d.CriticalToPeople || strings.Join(d.CriticalPhone, ",") != "Bark" {
		t.Fatalf("Bark-only delivery route: %+v", d)
	}
	if err := e.b.HandleWebhook(ctx, hook("firing", "critical", "phone")); err != nil {
		t.Fatal(err)
	}
	if bark.sent != 0 {
		t.Fatal("Bark was sent before its delay")
	}
	e.now = e.now.Add(time.Minute)
	e.b.Tick(ctx)
	if bark.sent != 1 || len(e.bot.texts) != 0 || len(e.push.sent) != 0 {
		t.Fatalf("scheduled Bark-only route: %d Bark alerts, %d bot messages, %d Pushover alerts",
			bark.sent, len(e.bot.texts), len(e.push.sent))
	}
}

type failingInitialPush struct {
	fakePush
	attempts int
}

func (p *failingInitialPush) Send(context.Context, notify.Emergency) (string, error) {
	p.attempts++
	return "", errors.New("provider unavailable")
}

func TestInitialPhoneFallbackMakesOneAttempt(t *testing.T) {
	e := newEnv(t)
	e.fs.fail = true
	e.b.d.Bot = nil
	push := &failingInitialPush{}
	e.b.d.Pushover = push
	if err := e.b.HandleWebhook(context.Background(), hook("firing", "critical", "f1")); err != nil {
		t.Fatal(err)
	}
	if push.attempts != 1 {
		t.Fatalf("initial phone fallback made %d attempts, want one", push.attempts)
	}
}

func TestAutomaticPushoverOnlyRouteRemainsImmediate(t *testing.T) {
	e := newEnv(t)
	e.b.d.Feishu, e.b.d.Bot = nil, nil
	e.b.d.Config.Escalation["critical"] = config.Policy{Channel: "none", InitialPushover: true}
	if err := e.b.HandleWebhook(context.Background(), hook("firing", "critical", "f1")); err != nil {
		t.Fatal(err)
	}
	if len(e.push.sent) != 1 || !e.b.Delivery().CriticalToPeople {
		t.Fatalf("automatically selected Pushover: %d sent, delivery %+v", len(e.push.sent), e.b.Delivery())
	}
}

func TestAPIOperations(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_ = e.b.HandleWebhook(ctx, hook("firing", "critical", "f1"))
	id := GroupID(hook("firing", "critical").GroupKey)
	who := Actor{Name: "claude-code", Via: "mcp"}
	if _, err := e.b.Ack(ctx, "nope", who, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown group: %v", err)
	}
	if _, err := e.b.Silence(ctx, id, time.Hour, who, ""); !errors.Is(err, ErrBadInput) {
		t.Errorf("a silence needs a comment: %v", err)
	}
	if _, err := e.b.Silence(ctx, id, 25*time.Hour, who, "x"); !errors.Is(err, ErrBadInput) {
		t.Errorf("a silence over 24h: %v", err)
	}
	v, err := e.b.Silence(ctx, id, time.Hour, who, "upgrading gitea")
	if err != nil || v.Silence == nil || v.Ack == nil || v.Ack.Via != "mcp" {
		t.Fatalf("Silence: %+v %v", v, err)
	}
	if !strings.Contains(e.bot.texts[len(e.bot.texts)-1], `claude-code silenced "git.example.com is down" for 1 h via MCP: upgrading gitea`) {
		t.Errorf("echo: %v", e.bot.texts)
	}
	if err := e.b.ExpireSilence(ctx, v.Silence.ID, who); err != nil || len(e.am.expired) != 1 {
		t.Fatalf("ExpireSilence: %v", err)
	}
	if g, _ := e.b.Group(id); g.Silence != nil {
		t.Error("the silence should be cleared from the group")
	}
	s := e.b.Summarize()
	if s.Firing["critical"] != 1 || s.Acknowledged != 1 || s.FeishuCalls == 0 {
		t.Errorf("summary: %+v", s)
	}
	if gs := e.b.Groups(true); len(gs) != 1 || gs[0].Runbook == "" || gs[0].Service != "git-example-com" {
		t.Errorf("groups: %+v", gs)
	}
	if len(e.b.Audit(10)) != 2 {
		t.Errorf("audit: %+v", e.b.Audit(10))
	}
}

func TestSilenceLabels(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w := hook("firing", "critical", "f1")
	w.Alerts[0].Labels = map[string]string{"alertname": "ServiceDown", "service": "git-example-com", "severity": "critical"}
	_ = e.b.HandleWebhook(ctx, w)
	id := GroupID(w.GroupKey)
	who := Actor{Name: "cli:alice", Via: "cli"}
	for _, bad := range []struct {
		labels  map[string]string
		d       time.Duration
		comment string
	}{
		{map[string]string{"service": "git-example-com"}, time.Hour, ""},
		{map[string]string{"service": "git-example-com"}, 25 * time.Hour, "x"},
		{nil, time.Hour, "x"},
		{map[string]string{"service": ""}, time.Hour, "x"},
	} {
		if _, err := e.b.SilenceLabels(ctx, bad.labels, bad.d, who, bad.comment); !errors.Is(err, ErrBadInput) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	// labels the group's alerts do not all carry: the silence exists, the group is not marked
	s, err := e.b.SilenceLabels(ctx, map[string]string{"service": "hub-example-com"}, time.Hour, who, "hub maintenance")
	if err != nil || s.ID == "" || len(s.Matchers) != 1 || s.Status.State != "active" || len(e.am.created) != 1 {
		t.Fatalf("SilenceLabels: %+v %v", s, err)
	}
	if g, _ := e.b.Group(id); g.Silence != nil || g.Ack != nil {
		t.Error("a silence for another service must not mark this group")
	}
	// labels every alert carries: the group is silenced and acknowledged, the chat hears it
	s, err = e.b.SilenceLabels(ctx, map[string]string{"alertname": "ServiceDown", "service": "git-example-com"}, 4*time.Hour, who, "upgrading gitea")
	if err != nil || len(s.Matchers) != 2 || s.Matchers[0].Name != "alertname" || s.EndsAt.Sub(s.StartsAt) != 4*time.Hour {
		t.Fatalf("SilenceLabels: %+v %v", s, err)
	}
	g, _ := e.b.Group(id)
	if g.Silence == nil || g.Silence.ID != s.ID || g.Silence.By != "cli:alice" || g.Ack == nil || g.Ack.Via != "cli" {
		t.Errorf("the matching group is silenced and acknowledged: %+v", g)
	}
	if last := e.bot.texts[len(e.bot.texts)-1]; !strings.Contains(last, `cli:alice silenced "alertname=ServiceDown,service=git-example-com" for 4 h via the command line: upgrading gitea`) {
		t.Errorf("echo: %q", last)
	}
	audit := e.b.Audit(10)
	if len(audit) != 2 || audit[0].Action != "silence" || audit[0].Via != "cli" || audit[0].Target != "alertname=ServiceDown,service=git-example-com" {
		t.Errorf("audit: %+v", audit)
	}
}

func TestGroupedPathWarning(t *testing.T) {
	e := newEnv(t)
	w := alertmanager.Webhook{Version: "4", GroupKey: `{}:{alertname="OverseasOnlyFailing"}`, Status: "firing",
		GroupLabels:  map[string]string{"alertname": "OverseasOnlyFailing"},
		CommonLabels: map[string]string{"alertname": "OverseasOnlyFailing", "severity": "warning"}}
	for _, svc := range []string{"hub.example.com", "git.example.com", "ci.example.com"} {
		w.Alerts = append(w.Alerts, alertmanager.Alert{Status: "firing", Fingerprint: svc, StartsAt: e.now,
			Labels:      map[string]string{"alertname": "OverseasOnlyFailing", "service": svc},
			Annotations: map[string]string{"summary": svc + " fails only from overseas"}})
	}
	if err := e.b.HandleWebhook(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	c := cardText(e.fs.sends[0])
	if !strings.Contains(c, "hub.example.com fails only from overseas and 2 more") || !strings.Contains(c, "ci.example.com, git.example.com, hub.example.com") || !strings.Contains(c, "3 firing") {
		t.Errorf("grouped card: %s", c)
	}
}

func TestChineseCard(t *testing.T) {
	e := newEnv(t)
	e.b.lang = "zh"
	_ = e.b.HandleWebhook(context.Background(), hook("firing", "critical", "f1"))
	c := cardText(e.fs.sends[0])
	// The catalogue is Chinese; only check that translations were used, without Chinese in this file.
	if strings.Contains(c, "Acknowledge") || strings.Contains(c, "Silence 1h") {
		t.Errorf("the card should be translated: %s", c)
	}
}
