package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/vm"
)

// heartbeatVM answers the heartbeats' last success, and has no probes.
type heartbeatVM struct {
	mu      sync.Mutex
	queries []string
	last    []vm.Sample
	err     error
}

func (v *heartbeatVM) asked(q string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.queries = append(v.queries, q)
	return v.err
}

func (v *heartbeatVM) Query(_ context.Context, q string) ([]vm.Sample, error) { return nil, v.asked(q) }

func (v *heartbeatVM) QueryAt(_ context.Context, q string, _ time.Time) ([]vm.Sample, error) {
	if err := v.asked(q); err != nil {
		return nil, err
	}
	if strings.Contains(q, "beacon_heartbeat_last_success_timestamp_seconds") {
		return v.last, nil
	}
	return nil, nil
}

func (v *heartbeatVM) QueryRange(_ context.Context, q string, _, _ time.Time, _ time.Duration) ([]vm.Series, error) {
	return nil, v.asked(q)
}

// stale fires HeartbeatStale for heartbeat name, as the rules in deploy/rules/expiry.yml label it.
func stale(status, name string) alertmanager.Webhook {
	labels := map[string]string{"alertname": "HeartbeatStale", "heartbeat": name, "service": name, "failure_domain": name, "severity": "critical"}
	return alertmanager.Webhook{Version: "4", GroupKey: "{}:" + name, Status: status, GroupLabels: map[string]string{"alertname": "HeartbeatStale", "service": name},
		CommonLabels: labels, CommonAnnotations: map[string]string{"summary": name + " has not succeeded for 5m"},
		Alerts: []alertmanager.Alert{{Status: status, Fingerprint: name, Labels: labels, StartsAt: time.Date(2026, 10, 30, 1, 58, 0, 0, time.UTC)}}}
}

func withHeartbeats(e *env, hs ...catalog.Heartbeat) {
	cat := &catalog.Catalog{Heartbeats: hs}
	e.b.d.Catalog = func() *catalog.Catalog { return cat }
}

// The drill: a heartbeat's timer was stopped on its machine three minutes before HeartbeatStale fired.
// The evidence and the timeline name the machine from the catalogue, and list what was done on it before
// the alert as well as after, marked so.
func TestHeartbeatEvidence(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	start := time.Date(2026, 10, 30, 1, 58, 0, 0, time.UTC)
	withHeartbeats(e, catalog.Heartbeat{Name: "drill-develop-cn", MaxAge: config.Duration(5 * time.Minute), Severity: "critical",
		Description: "Beacon's drill timer\x1b[31m on develop-cn", Machine: "develop-cn"})
	if err := e.st.PutMachine(store.Machine{Name: "develop-cn", Address: "develop-cn.example.com:22", User: "beacon"}); err != nil {
		t.Fatal(err)
	}
	commands := map[string][]ShellCommand{}
	e.b.d.ShellCommands = func(id string) []ShellCommand { return commands[id] }
	v := &heartbeatVM{last: []vm.Sample{{Labels: map[string]string{"heartbeat": "drill-develop-cn"}, Value: float64(start.Add(-5 * time.Minute).Unix())}}}
	e.b.d.VM = v
	if err := e.b.HandleWebhook(ctx, stale("firing", "drill-develop-cn")); err != nil {
		t.Fatal(err)
	}
	id := GroupID(stale("firing", "drill-develop-cn").GroupKey)

	zero, one := 0, 1
	shellID := "20261030-015000-bbbbbb-1"
	commands[shellID] = []ShellCommand{
		{Command: "systemctl list-timers\x1b[0m", Exit: &zero, Started: start.Add(-8*time.Minute + 10*time.Second)},
		{Command: "false", Exit: &one, Started: start.Add(-8*time.Minute + 20*time.Second)},
	}
	for _, r := range []store.SessionRecord{
		{ID: "20261030-010000-000000-1", Agent: "claude-code", Machine: "develop-cn", Kind: "exec", Command: "too early", Started: start.Add(-time.Hour), Ended: start.Add(-time.Hour)},
		{ID: shellID, Agent: "claude-code", Via: "ssh", Machine: "develop-cn", Kind: "shell", Commands: 2, Started: start.Add(-8 * time.Minute), Ended: start.Add(-7 * time.Minute)},
		{ID: "20261030-015500-aaaaaa-1", Agent: "claude-code", Via: "mcp", Machine: "develop-cn", Kind: "exec", Command: "sudo systemctl stop beacon-drill.timer",
			Started: start.Add(-3 * time.Minute), Ended: start.Add(-3 * time.Minute), Exit: &zero},
		{ID: "20261030-020100-cccccc-1", Agent: "grokbot-oncall", Via: "api", Machine: "develop-cn", Kind: "exec", Command: "sudo systemctl start beacon-drill.timer",
			Started: start.Add(3 * time.Minute), Ended: start.Add(3 * time.Minute), Exit: &zero},
		{ID: "20261030-020200-dddddd-1", Agent: "codex", Machine: "edge-1", Kind: "exec", Command: "uptime", Started: start.Add(4 * time.Minute)},
	} {
		if err := e.st.PutSession(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.b.RecordChange(ctx, store.Change{Source: "ci", Kind: "config", Machine: "develop-cn", Summary: "drill timer edited", Time: start.Add(-2 * time.Minute)}, "ci"); err != nil {
		t.Fatal(err)
	}
	e.now = start.Add(6 * time.Minute)

	ev, err := e.b.Evidence(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.Heartbeats) != 1 {
		t.Fatalf("heartbeats: %+v", ev.Heartbeats)
	}
	h := ev.Heartbeats[0]
	if h.Name != "drill-develop-cn" || h.Description != "Beacon's drill timer on develop-cn" || h.MaxAge != "5m" || h.Severity != "critical" ||
		h.Machine != "develop-cn" || h.Enrolled == nil || !*h.Enrolled || h.LastSuccess == nil || !h.LastSuccess.Equal(start.Add(-5*time.Minute)) ||
		!strings.Contains(h.Hint, "list_machines") || h.Error != "" {
		t.Errorf("heartbeat: %+v", h)
	}
	if !slices.ContainsFunc(v.queries, func(q string) bool {
		return q == `max by (heartbeat) (last_over_time(beacon_heartbeat_last_success_timestamp_seconds{heartbeat="drill-develop-cn"}[31d]))`
	}) {
		t.Errorf("queries: %q", v.queries)
	}
	if !slices.Contains(ev.Done.Machines, "develop-cn") {
		t.Errorf("the heartbeat's machine is among the alert's machines: %v", ev.Done.Machines)
	}
	var got []string
	for _, s := range ev.Done.Sessions {
		got = append(got, s.ID[16:22]+":"+s.Phase+":"+s.Related)
	}
	if strings.Join(got, " ") != "bbbbbb:before:machine aaaaaa:before:machine cccccc:during:machine" {
		t.Fatalf("sessions on the machine, from 30 minutes before the alert: %v", got)
	}
	shell, stop := ev.Done.Sessions[0], ev.Done.Sessions[1]
	if len(shell.ShellCommands) != 2 || shell.ShellCommands[0].Command != "systemctl list-timers" || *shell.ShellCommands[1].Exit != 1 {
		t.Errorf("a shell's commands, with their exit statuses: %+v", shell.ShellCommands)
	}
	if stop.Agent != "claude-code" || stop.Command != "sudo systemctl stop beacon-drill.timer" || *stop.Exit != 0 || stop.ShellCommands != nil {
		t.Errorf("the session that stopped the timer: %+v", stop)
	}
	if len(ev.Changes) != 1 || ev.Changes[0].Related != "machine" || ev.Changes[0].Phase != "before" {
		t.Errorf("changes: %+v", ev.Changes)
	}
	for _, f := range []string{"heartbeats[].description", "done.sessions[].shell_commands[].command"} {
		if !slices.Contains(ev.Untrusted.Fields, f) {
			t.Errorf("untrusted lacks %s", f)
		}
	}
	b, _ := json.Marshal(ev)
	for _, want := range []string{`"phase":"before"`, `"shell_commands":[{"command":"systemctl list-timers","exit":0,`, `"heartbeats":[{"name":"drill-develop-cn",`, `"max_age":"5m"`, `"enrolled":true`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("evidence lacks %s:\n%s", want, b)
		}
	}

	tl, err := e.b.Timeline(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, ev := range tl.Events {
		kinds = append(kinds, ev.Kind+":"+ev.Phase)
	}
	want := "session:before command:before command:before heartbeat:before session:before change:before started: notified: session:during"
	if strings.Join(kinds, " ") != want {
		t.Errorf("timeline:\n %s\nwant\n %s", strings.Join(kinds, " "), want)
	}
	if hb := tl.Events[3]; hb.ID != "drill-develop-cn" || hb.Machine != "develop-cn" || !hb.At.Equal(start.Add(-5*time.Minute)) {
		t.Errorf("the heartbeat's last success: %+v", hb)
	}
	if c := tl.Events[2]; c.Text != "false" || *c.Exit != 1 || c.ID != shellID || c.Machine != "develop-cn" {
		t.Errorf("a command: %+v", c)
	}
	if len(tl.Heartbeats) != 1 || tl.Heartbeats[0].Machine != "develop-cn" || !slices.Contains(tl.Untrusted.Fields, "heartbeats[].description") {
		t.Errorf("the timeline's heartbeats: %+v", tl.Heartbeats)
	}
	// once resolved, what comes after is after
	e.now = start.Add(10 * time.Minute)
	_ = e.b.HandleWebhook(ctx, stale("resolved", "drill-develop-cn"))
	_ = e.st.PutSession(store.SessionRecord{ID: "20261030-020900-eeeeee-1", Agent: "grokbot-oncall", Machine: "develop-cn", Kind: "exec", Command: "systemctl status beacon-drill.timer",
		Started: start.Add(11 * time.Minute), Ended: start.Add(11 * time.Minute), Exit: &zero})
	e.now = start.Add(12 * time.Minute)
	if ev, _ := e.b.Evidence(ctx, id, ""); ev.Done.Sessions[len(ev.Done.Sessions)-1].Phase != "after" {
		t.Errorf("after the alert resolved: %+v", ev.Done.Sessions)
	}
}

// The hook that wakes an agent for a heartbeat's alert points it at the heartbeat's machine.
func TestHeartbeatHint(t *testing.T) {
	fastRetries(t)
	e := newEnv(t)
	hs := newHookServer(t, 202)
	e.b.d.Config.PublicURL = "https://status.example.com"
	e.b.d.Config.Agents = []config.Agent{{Name: "oncall", Kind: "webhook", URL: hs.srv.URL, Secret: hookSecret, Severities: []string{"critical"}}}
	withHeartbeats(e, catalog.Heartbeat{Name: "drill-develop-cn", MaxAge: config.Duration(5 * time.Minute), Severity: "critical", Machine: "develop-cn"})
	_ = e.b.HandleWebhook(context.Background(), stale("firing", "drill-develop-cn"))
	e.b.waitWakes()
	var p HookPayload
	if len(hs.bodies) != 1 || json.Unmarshal(hs.bodies[0], &p) != nil {
		t.Fatalf("calls: %d", len(hs.bodies))
	}
	if !strings.HasPrefix(p.Hint, hintFiring) || !strings.Contains(p.Hint, "runs on machine develop-cn") || !strings.Contains(p.Hint, "list_machines") {
		t.Errorf("hint: %s", p.Hint)
	}
	// an alert about no heartbeat keeps the hint as it was
	_ = e.b.HandleWebhook(context.Background(), hook("firing", "critical", "f1"))
	e.b.waitWakes()
	if len(hs.bodies) != 2 || json.Unmarshal(hs.bodies[1], &p) != nil || p.Hint != hintFiring {
		t.Errorf("hint for a service: %s", p.Hint)
	}
}

// A heartbeat whose machine is not enrolled, one the catalogue does not have, and VictoriaMetrics down:
// the evidence says each, and still answers.
func TestHeartbeatEvidenceGaps(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	withHeartbeats(e,
		catalog.Heartbeat{Name: "nas-backup", MaxAge: config.Duration(26 * time.Hour), Severity: "warning", Machine: "nas-1"},
		catalog.Heartbeat{Name: "plain", MaxAge: config.Duration(time.Hour), Severity: "warning", Description: "Runs somewhere"})
	e.b.d.VM = &heartbeatVM{err: errors.New("connection refused")}
	w := stale("firing", "nas-backup")
	w.Alerts = append(w.Alerts, alertmanager.Alert{Status: "firing", Fingerprint: "x", StartsAt: w.Alerts[0].StartsAt,
		Labels: map[string]string{"alertname": "HeartbeatStale", "heartbeat": "gone"}})
	w.Alerts = append(w.Alerts, alertmanager.Alert{Status: "firing", Fingerprint: "y", StartsAt: w.Alerts[0].StartsAt,
		Labels: map[string]string{"alertname": "HeartbeatStale", "heartbeat": `bad"name`}})
	w.Alerts = append(w.Alerts, alertmanager.Alert{Status: "firing", Fingerprint: "z", StartsAt: w.Alerts[0].StartsAt,
		Labels: map[string]string{"alertname": "HeartbeatStale", "heartbeat": "plain"}})
	_ = e.b.HandleWebhook(ctx, w)
	ev, err := e.b.Evidence(ctx, GroupID(w.GroupKey), "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, h := range ev.Heartbeats {
		names = append(names, h.Name)
	}
	if strings.Join(names, ",") != "gone,nas-backup,plain" {
		t.Fatalf("heartbeats, a name no heartbeat could have left out: %v", names)
	}
	gone, nas, plain := ev.Heartbeats[0], ev.Heartbeats[1], ev.Heartbeats[2]
	if !strings.Contains(gone.Error, "the catalogue has no heartbeat gone") || !strings.Contains(gone.Error, "VictoriaMetrics: connection refused") {
		t.Errorf("not in the catalogue: %+v", gone)
	}
	if nas.Enrolled == nil || *nas.Enrolled || !strings.Contains(nas.Hint, "cannot reach it") || nas.LastSuccess != nil {
		t.Errorf("a machine not enrolled: %+v", nas)
	}
	if plain.Machine != "" || plain.Enrolled != nil || !strings.Contains(plain.Hint, "names no machine") || plain.Description != "Runs somewhere" {
		t.Errorf("no machine: %+v", plain)
	}
	if !slices.Contains(ev.Done.Machines, "nas-1") {
		t.Errorf("the machine is named even when not enrolled: %v", ev.Done.Machines)
	}
	if ev.Probes.Error == "" || ev.Alert.ID == "" {
		t.Errorf("the rest still answers: %+v", ev.Probes)
	}
	// without a heartbeat label there is no heartbeats section
	_ = e.b.HandleWebhook(ctx, hook("firing", "critical", "f1"))
	ev, _ = e.b.Evidence(ctx, GroupID(hook("firing", "critical").GroupKey), "")
	if b, _ := json.Marshal(ev); ev.Heartbeats != nil || strings.Contains(string(b), `"heartbeats"`) {
		t.Errorf("heartbeats without a heartbeat: %s", b)
	}
}

// The sessions on the alert's machines before it started are kept first when there are many.
func TestKeepSessions(t *testing.T) {
	start := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	var rs []store.SessionRecord
	for i := range 25 {
		rs = append(rs, store.SessionRecord{ID: fmt.Sprintf("b%02d", i), Machine: "m1", Started: start.Add(time.Duration(i-30) * time.Minute)})
	}
	rs = append(rs, store.SessionRecord{ID: "agent-before", Machine: "elsewhere", Started: start.Add(-time.Minute)})
	for i := range 60 {
		rs = append(rs, store.SessionRecord{ID: fmt.Sprintf("d%02d", i), Machine: "m1", Started: start.Add(time.Duration(i) * time.Second)})
	}
	got := keepSessions(rs, start, []string{"m1"})
	if len(got) != maxListed || got[0].ID != "b05" || got[19].ID != "b24" || got[20].ID != "d30" || got[len(got)-1].ID != "d59" {
		var ids []string
		for _, r := range got {
			ids = append(ids, r.ID)
		}
		t.Errorf("kept: %v", ids)
	}
	if few := keepSessions(rs[:10], start, []string{"m1"}); len(few) != 10 {
		t.Errorf("few are all kept: %d", len(few))
	}
}
