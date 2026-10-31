package router

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/store"
)

type sink struct {
	mu       sync.Mutex
	hooks    []alertmanager.Webhook
	watchdog int
	fail     bool
}

func (s *sink) HandleWebhook(_ context.Context, w alertmanager.Webhook) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return context.DeadlineExceeded
	}
	s.hooks = append(s.hooks, w)
	return nil
}

func (s *sink) Watchdog() { s.mu.Lock(); s.watchdog++; s.mu.Unlock() }

func (s *sink) take() []alertmanager.Webhook {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.hooks
	s.hooks = nil
	return out
}

type env struct {
	r   *Router
	s   *sink
	st  *store.Store
	now time.Time
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{s: &sink{}, st: st, now: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	e.r = New(cfg, st, e.s, func() time.Time { return e.now }, nil)
	return e
}

func (e *env) tick(d time.Duration) {
	e.now = e.now.Add(d)
	e.r.Flush(context.Background())
}

// fire posts alerts as vmalert does: firing ones with endsAt some evaluations ahead (tests resend before it).
func (e *env) fire(labels ...map[string]string) {
	var ps []Postable
	for _, l := range labels {
		ps = append(ps, Postable{Labels: l, Annotations: map[string]string{"summary": l["service"] + " is down"}, StartsAt: e.now, EndsAt: e.now.Add(4 * time.Minute)})
	}
	_ = e.r.Ingest(ps)
}

func (e *env) resolve(labels map[string]string) {
	_ = e.r.Ingest([]Postable{{Labels: labels, StartsAt: e.now.Add(-time.Hour), EndsAt: e.now.Add(-time.Second)}})
}

func down(svc string) map[string]string {
	return map[string]string{"alertname": "ServiceDown", "service": svc, "severity": "critical", "failure_domain": "pve-us1"}
}

var base = Config{GroupBy: []string{"alertname", "service", "failure_domain"}, GroupWait: 30 * time.Second, GroupInterval: 2 * time.Minute, ResolveTimeout: 5 * time.Minute}

func TestGroupWaitThenResolve(t *testing.T) {
	e := newEnv(t, base)
	e.fire(down("git"))
	e.tick(10 * time.Second)
	if len(e.s.take()) != 0 {
		t.Fatal("nothing before group_wait")
	}
	e.tick(25 * time.Second)
	hooks := e.s.take()
	if len(hooks) != 1 || hooks[0].Status != "firing" || len(hooks[0].Alerts) != 1 || hooks[0].CommonLabels["service"] != "git" ||
		hooks[0].GroupLabels["alertname"] != "ServiceDown" || !strings.Contains(hooks[0].GroupKey, `service="git"`) {
		t.Fatalf("firing: %+v", hooks)
	}
	// the rule engine keeps resending: no change, nothing sent
	e.fire(down("git"))
	e.tick(3 * time.Minute)
	if len(e.s.take()) != 0 {
		t.Fatal("no change, no notification")
	}
	e.resolve(down("git"))
	e.tick(5 * time.Second)
	hooks = e.s.take()
	if len(hooks) != 1 || hooks[0].Status != "resolved" || hooks[0].Alerts[0].Status != "resolved" {
		t.Fatalf("resolved: %+v", hooks)
	}
	e.tick(5 * time.Minute)
	if len(e.s.take()) != 0 || e.r.Firing() != 0 || len(e.r.groups) != 0 || len(e.r.alerts) != 0 {
		t.Fatal("a resolved group is forgotten")
	}
}

func TestRouteGroupsAcrossServicesAndIntervals(t *testing.T) {
	cfg := base
	m, _ := CompileLabels(map[string]string{"alertname": "MainlandPathDegraded|ServiceDegraded"})
	cfg.Routes = []Route{{Match: m, GroupBy: []string{"alertname", "severity"}}}
	e := newEnv(t, cfg)
	path := func(svc string) map[string]string {
		return map[string]string{"alertname": "MainlandPathDegraded", "service": svc, "severity": "warning"}
	}
	e.fire(path("a"), path("b"))
	e.tick(31 * time.Second)
	hooks := e.s.take()
	if len(hooks) != 1 || len(hooks[0].Alerts) != 2 || hooks[0].CommonLabels["service"] != "" {
		t.Fatalf("one card for both services: %+v", hooks)
	}
	// a third service joins: sent at the next group_interval, not before
	e.fire(path("a"), path("b"), path("c"))
	e.tick(30 * time.Second)
	if len(e.s.take()) != 0 {
		t.Fatal("changes wait for group_interval")
	}
	e.tick(2 * time.Minute)
	if hooks = e.s.take(); len(hooks) != 1 || len(hooks[0].Alerts) != 3 {
		t.Fatalf("after group_interval: %+v", hooks)
	}
}

func TestInhibitAndSilence(t *testing.T) {
	cfg := base
	src, _ := CompileLabels(map[string]string{"alertname": "HostDown"})
	dst, _ := CompileLabels(map[string]string{"alertname": "ServiceDown|ServiceDegraded"})
	cfg.Inhibit = []Inhibit{{Source: src, Target: dst, Equal: []string{"failure_domain"}}}
	e := newEnv(t, cfg)
	host := map[string]string{"alertname": "HostDown", "service": "pve-us1", "severity": "critical", "failure_domain": "pve-us1"}
	e.fire(host, down("git"), down("hub"))
	e.tick(31 * time.Second)
	hooks := e.s.take()
	if len(hooks) != 1 || hooks[0].CommonLabels["alertname"] != "HostDown" {
		t.Fatalf("only HostDown, the services are inhibited: %+v", hooks)
	}
	// a silence matching the HostDown alert: no group left to send
	e2 := newEnv(t, base)
	if _, err := e2.r.CreateSilence(context.Background(), map[string]string{"service": "git"}, e2.now, e2.now.Add(time.Hour), "alice", "deploy"); err != nil {
		t.Fatal(err)
	}
	e2.fire(down("git"), down("hub"))
	e2.tick(31 * time.Second)
	hooks = e2.s.take()
	if len(hooks) != 1 || hooks[0].CommonLabels["service"] != "hub" {
		t.Fatalf("git is silenced: %+v", hooks)
	}
	ss, _ := e2.r.Silences(context.Background())
	if len(ss) != 1 || ss[0].Status.State != "active" || ss[0].CreatedBy != "alice" || !ss[0].Matchers[0].IsEqual {
		t.Fatalf("silences %+v", ss)
	}
	if err := e2.r.ExpireSilence(context.Background(), ss[0].ID); err != nil {
		t.Fatal(err)
	}
	e2.fire(down("git"), down("hub"))
	e2.tick(5 * time.Second)
	hooks = e2.s.take()
	if len(hooks) != 1 || hooks[0].CommonLabels["service"] != "git" {
		t.Fatalf("after the silence ends, git is sent: %+v", hooks)
	}
	if err := e2.r.ExpireSilence(context.Background(), ss[0].ID); err == nil {
		t.Error("expiring twice must fail")
	}
}

func TestSilencedGroupStaysQuietAndResolvesAfterwards(t *testing.T) {
	e := newEnv(t, base)
	e.fire(down("git"))
	e.tick(31 * time.Second)
	e.s.take()
	id, _ := e.r.CreateSilence(context.Background(), map[string]string{"alertname": "ServiceDown", "service": "git"}, e.now, e.now.Add(time.Hour), "a", "x")
	e.fire(down("git"))
	e.tick(3 * time.Minute)
	if len(e.s.take()) != 0 {
		t.Fatal("a silenced group sends nothing")
	}
	e.resolve(down("git"))
	e.tick(5 * time.Second)
	if hooks := e.s.take(); len(hooks) != 1 || hooks[0].Status != "resolved" {
		t.Fatalf("the resolution still goes out, so the card turns green: %+v", hooks)
	}
	_ = id
}

func TestResolveTimeoutAndRestore(t *testing.T) {
	e := newEnv(t, base)
	// an alert without endsAt lasts resolve_timeout
	_ = e.r.Ingest([]Postable{{Labels: down("git")}})
	e.tick(31 * time.Second)
	e.s.take()
	e.tick(5 * time.Minute)
	if hooks := e.s.take(); len(hooks) != 1 || hooks[0].Status != "resolved" {
		t.Fatalf("resolved after resolve_timeout: %+v", hooks)
	}

	// restart: the bridge's firing group comes back; the rule engine does not resend it
	r2 := newEnv(t, base)
	fp := Fingerprint(down("hub"))
	key, _ := r2.r.groupOf(down("hub"))
	r2.r.Restore([]store.Group{{ID: "x", Key: key, Status: "firing", StartsAt: r2.now.Add(-time.Hour),
		Alerts: []store.Alert{{Fingerprint: fp, Status: "firing", Labels: down("hub"), StartsAt: r2.now.Add(-time.Hour)}}}})
	r2.tick(time.Minute)
	if len(r2.s.take()) != 0 {
		t.Fatal("a restored group is not sent again")
	}
	r2.fire(down("hub")) // still firing: the rule engine resends, nothing changes
	r2.tick(3 * time.Minute)
	if len(r2.s.take()) != 0 {
		t.Fatal("still nothing to send")
	}
	r2.tick(6 * time.Minute) // no resend since: it resolved while we were down
	if hooks := r2.s.take(); len(hooks) != 1 || hooks[0].Status != "resolved" || hooks[0].GroupKey != key {
		t.Fatalf("resolved after restore: %+v", hooks)
	}
}

func TestWatchdogAndFailures(t *testing.T) {
	e := newEnv(t, base)
	_ = e.r.Ingest([]Postable{{Labels: map[string]string{"alertname": "Watchdog", "severity": "none"}, EndsAt: e.now.Add(time.Minute)}})
	if e.s.watchdog != 1 || len(e.r.alerts) != 0 {
		t.Fatalf("watchdog %d, alerts %d", e.s.watchdog, len(e.r.alerts))
	}
	// a failed delivery is retried, but not at every flush
	e.s.fail = true
	e.fire(down("git"))
	e.tick(31 * time.Second)
	e.s.fail = false
	e.tick(10 * time.Second)
	if len(e.s.take()) != 0 {
		t.Fatal("not within 30 seconds of the failure")
	}
	e.tick(25 * time.Second)
	if hooks := e.s.take(); len(hooks) != 1 || hooks[0].Status != "firing" {
		t.Fatalf("retried: %+v", hooks)
	}
	if err := e.r.Ingest(make([]Postable, MaxAlerts+1)); err == nil {
		t.Error("too many alerts accepted")
	}
}
