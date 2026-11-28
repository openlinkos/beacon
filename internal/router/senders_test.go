package router

import (
	"maps"
	"strings"
	"testing"
	"time"
)

// post sends alerts as sender (an alerts:send token's name), firing for a few minutes.
func (e *env) post(sender string, annotations map[string]string, labels ...map[string]string) {
	var ps []Postable
	for _, l := range labels {
		ps = append(ps, Postable{Labels: maps.Clone(l), Annotations: annotations, StartsAt: e.now, EndsAt: e.now.Add(4 * time.Minute)})
	}
	_ = e.r.Ingest(sender, ps)
}

// TestSendersKeepToTheirOwnAlerts: another sender posting the rule engine's labels neither resolves nor
// re-annotates the rule engine's alert; its alert is its own, in a group of its own, labelled with it.
func TestSendersKeepToTheirOwnAlerts(t *testing.T) {
	e := newEnv(t, base)
	e.fire(down("git"))
	e.tick(31 * time.Second)
	if hooks := e.s.take(); len(hooks) != 1 || hooks[0].Status != "firing" {
		t.Fatalf("firing: %+v", hooks)
	}
	// the same labels, resolved and re-annotated, from someone else, with a sender label of its own choosing
	forged := down("git")
	forged[SenderLabel] = ""
	_ = e.r.Ingest("flux", []Postable{{Labels: forged, Annotations: map[string]string{"summary": "all is well"},
		StartsAt: e.now.Add(-time.Hour), EndsAt: e.now.Add(-time.Second)}})
	e.fire(down("git"))
	e.tick(3 * time.Minute)
	if hooks := e.s.take(); len(hooks) != 0 {
		t.Fatalf("the rule engine's group changed: %+v", hooks)
	}
	if a := e.r.alerts[Fingerprint(down("git"))]; a == nil || a.annotations["summary"] != "git is down" || !e.now.Before(a.endsAt) {
		t.Fatalf("the rule engine's alert: %+v", a)
	}
	// a firing alert from flux is flux's, in its own group
	e.post("flux", map[string]string{"summary": "from flux"}, down("git"))
	e.tick(31 * time.Second)
	hooks := e.s.take()
	if len(hooks) != 1 || hooks[0].GroupLabels[SenderLabel] != "flux" || hooks[0].CommonLabels[SenderLabel] != "flux" ||
		!strings.Contains(hooks[0].GroupKey, `sender="flux"`) || hooks[0].CommonAnnotations["summary"] != "from flux" {
		t.Fatalf("flux's alert: %+v", hooks)
	}
	// the rule engine cannot name a sender: its alerts carry none
	tagged := down("hub")
	tagged[SenderLabel] = "flux"
	e.fire(down("git"), tagged)
	e.tick(31 * time.Second)
	hooks = e.s.take()
	if len(hooks) != 1 || hooks[0].CommonLabels["service"] != "hub" {
		t.Fatalf("hub: %+v", hooks)
	}
	if _, ok := hooks[0].CommonLabels[SenderLabel]; ok || strings.Contains(hooks[0].GroupKey, SenderLabel) {
		t.Fatalf("the rule engine's alert carries a sender: %+v", hooks[0])
	}
}

// TestWatchdogIsTheRuleEngines: only the rule engine's Watchdog feeds the dead man's switch, and only
// the rule engine may send severity none, which is never shown.
func TestWatchdogIsTheRuleEngines(t *testing.T) {
	e := newEnv(t, base)
	watchdog := map[string]string{"alertname": "Watchdog", "severity": "none"}
	e.post("flux", nil, watchdog, map[string]string{"alertname": "Hidden", "service": "git", "severity": "none", "failure_domain": "pve-us1"})
	if e.s.watchdog != 0 || len(e.r.alerts) != 0 {
		t.Fatalf("from a sender: watchdog %d, alerts %d", e.s.watchdog, len(e.r.alerts))
	}
	e.fire(watchdog)
	if e.s.watchdog != 1 {
		t.Fatalf("from the rule engine: watchdog %d", e.s.watchdog)
	}
}

// TestInhibitionStaysWithItsSender: a sender's alert inhibits only its own alerts; the rule engine's
// inhibit everyone's.
func TestInhibitionStaysWithItsSender(t *testing.T) {
	cfg := base
	src, _ := CompileLabels(map[string]string{"alertname": "HostDown"})
	dst, _ := CompileLabels(map[string]string{"alertname": "ServiceDown"})
	cfg.Inhibit = []Inhibit{{Source: src, Target: dst, Equal: []string{"failure_domain"}}}
	host := map[string]string{"alertname": "HostDown", "service": "pve-us1", "severity": "critical", "failure_domain": "pve-us1"}

	e := newEnv(t, cfg)
	e.post("flux", nil, host)
	e.fire(down("git"))
	e.tick(31 * time.Second)
	var services []string
	for _, h := range e.s.take() {
		if h.Status == "firing" && len(h.Alerts) > 0 {
			services = append(services, h.CommonLabels["alertname"]+"/"+h.CommonLabels[SenderLabel])
		}
	}
	if !strings.Contains(strings.Join(services, " "), "ServiceDown/") {
		t.Fatalf("another sender's HostDown muted the rule engine's ServiceDown: %v", services)
	}

	// within one sender, and from the rule engine to a sender, inhibition holds
	e = newEnv(t, cfg)
	e.post("flux", nil, host, down("git"))
	e.tick(31 * time.Second)
	for _, h := range e.s.take() {
		if h.CommonLabels["alertname"] == "ServiceDown" {
			t.Fatalf("flux's own HostDown did not mute its ServiceDown: %+v", h)
		}
	}
	e = newEnv(t, cfg)
	e.fire(host)
	e.post("flux", nil, down("git"))
	e.tick(31 * time.Second)
	for _, h := range e.s.take() {
		if h.CommonLabels["alertname"] == "ServiceDown" {
			t.Fatalf("the rule engine's HostDown did not mute flux's ServiceDown: %+v", h)
		}
	}
}
