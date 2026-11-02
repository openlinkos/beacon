package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/probenow"
	"github.com/openlinkos/beacon/internal/targets"
)

// probeBoard has the env's location and a mainland one, both probing git.linkos.io (the hook's service)
// and a second target of the same service.
func probeBoard(t *testing.T) *probenow.Board {
	t.Helper()
	cat := &catalog.Catalog{Locations: []catalog.Location{
		{ID: "us-los-angeles", Name: "United States - Los Angeles", Region: "overseas"},
		{ID: "cn-shanghai-bgp", Name: "Mainland China - Shanghai", Region: "mainland"},
	}}
	set, err := targets.Build(map[string][]targets.Group{
		"all-git.yaml": {{Targets: []string{"https://git.linkos.io/", "https://git.linkos.io/api/healthz"}, Labels: map[string]string{"module": "http_head", "service": "git-linkos-io"}}},
	}, []string{"us-los-angeles", "cn-shanghai-bgp"})
	if err != nil {
		t.Fatal(err)
	}
	return probenow.New(probenow.Deps{Catalog: func() *catalog.Catalog { return cat }, Targets: func() *targets.Set { return set }})
}

// answerAs plays a probe location for the rest of the test: it polls as a probe does and answers every
// target with ok or a failure.
func answerAs(t *testing.T, b *probenow.Board, location string, ok bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for ctx.Err() == nil {
			for _, r := range b.Wait(ctx, location, time.Second) {
				var results []targets.TargetResult
				for _, g := range r.Groups {
					for _, tg := range g.Targets {
						res := targets.TargetResult{Target: tg, Module: g.Labels["module"], Success: ok, DurationSeconds: 0.1234, HTTPStatus: 200, Attempts: 1}
						if !ok {
							res.Reason, res.HTTPStatus, res.Attempts = "http_status\x1b[31m", 502, 2
						}
						results = append(results, res)
					}
				}
				_ = b.Answer(r.ID, location, results)
			}
		}
	}()
}

func TestProbeNowInputs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	who := Actor{Name: "claude-code", Via: "mcp"}
	if _, err := e.b.ProbeNow(ctx, who, ProbeNowInput{Target: "https://git.linkos.io/"}); !errors.Is(err, ErrProbeUnavailable) {
		t.Fatalf("without a board: %v", err)
	}
	e.b.d.ProbeNow = probeBoard(t)
	for _, c := range []struct {
		in   ProbeNowInput
		want error
		msg  string
	}{
		{ProbeNowInput{}, ErrBadInput, "one of alert_id, target or service"},
		{ProbeNowInput{Target: "https://git.linkos.io/", Service: "git-linkos-io"}, ErrBadInput, "one of"},
		{ProbeNowInput{Service: "git-linkos-io", Module: "http_head"}, ErrBadInput, "module goes with target"},
		{ProbeNowInput{Target: "https://git.linkos.io/", Wait: 91}, ErrBadInput, "wait is 1 to 90 seconds"},
		{ProbeNowInput{Target: "https://git.linkos.io/", Wait: -1}, ErrBadInput, "wait is 1 to 90 seconds"},
		{ProbeNowInput{AlertID: "nope", Wait: 1}, ErrNotFound, ""},
		{ProbeNowInput{Target: "https://nope.example.com/", Wait: 1}, ErrProbeNotFound, "no target set has target"},
		{ProbeNowInput{Service: "nope", Wait: 1}, ErrProbeNotFound, "service nope"},
	} {
		_, err := e.b.ProbeNow(ctx, who, c.in)
		if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%+v: %v", c.in, err)
		}
	}
	if len(e.bot.texts) != 0 || len(e.st.AuditLog(10)) != 0 {
		t.Errorf("nothing echoed or audited for refused inputs: %v %v", e.bot.texts, e.st.AuditLog(10))
	}
}

func TestProbeNowAnswersAndSummary(t *testing.T) {
	e := newEnv(t)
	e.b.d.ProbeNow = probeBoard(t)
	ctx := context.Background()
	who := Actor{Name: "claude-code", Via: "mcp"}
	// one location fine, the other failing
	answerAs(t, e.b.d.ProbeNow, "us-los-angeles", true)
	answerAs(t, e.b.d.ProbeNow, "cn-shanghai-bgp", false)
	out, err := e.b.ProbeNow(ctx, who, ProbeNowInput{Target: "https://git.linkos.io/", Wait: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Complete || len(out.Missing) != 0 || len(out.Results) != 2 || strings.Join(out.Locations, ",") != "cn-shanghai-bgp,us-los-angeles" ||
		len(out.Targets) != 1 || out.Targets[0].Module != "http_head" || strings.Join(out.Targets[0].Locations, ",") != "cn-shanghai-bgp,us-los-angeles" {
		t.Fatalf("answer: %+v", out)
	}
	// failing first, the reason cleaned, the duration rounded
	f, ok := out.Results[0], out.Results[1]
	if f.OK || f.Location != "cn-shanghai-bgp" || f.Reason != "http_status" || f.HTTPStatus != 502 || f.Attempts != 2 || f.DurationSeconds != 0.123 || f.ProbedAt.IsZero() ||
		!ok.OK || ok.Location != "us-los-angeles" || ok.Reason != "" || ok.HTTPStatus != 200 {
		t.Fatalf("results: %+v %+v", f, ok)
	}
	if out.Summary != "1 OK, 1 failing (http_status)" {
		t.Errorf("summary %q", out.Summary)
	}
	if !strings.Contains(strings.Join(out.Untrusted.Fields, " "), "results[].reason") {
		t.Errorf("untrusted: %v", out.Untrusted.Fields)
	}
	if last := e.bot.texts[len(e.bot.texts)-1]; last != "claude-code had 2 locations probe https://git.linkos.io/ again via MCP: 1 OK, 1 failing (http_status)" {
		t.Errorf("echo %q", last)
	}
	if a := e.st.AuditLog(1); len(a) != 1 || a[0].Action != "probe_now" || a[0].Actor != "claude-code" || a[0].Via != "mcp" ||
		a[0].Target != "https://git.linkos.io/" || a[0].Detail != "1 OK, 1 failing (http_status)" {
		t.Errorf("audit %+v", a)
	}
	// the same target at once is busy
	if _, err := e.b.ProbeNow(ctx, who, ProbeNowInput{Target: "https://git.linkos.io/", Wait: 1}); !errors.Is(err, ErrProbeBusy) {
		t.Errorf("busy: %v", err)
	}
}

// An alert without instance labels has every target of its service probed; a location that does not
// answer in time is missing.
func TestProbeNowByAlertService(t *testing.T) {
	e := newEnv(t)
	e.b.d.ProbeNow = probeBoard(t)
	ctx := context.Background()
	if err := e.b.HandleWebhook(ctx, hook("firing", "critical", "f1")); err != nil {
		t.Fatal(err)
	}
	id := GroupID(hook("firing", "critical", "f1").GroupKey)
	answerAs(t, e.b.d.ProbeNow, "us-los-angeles", true)
	out, err := e.b.ProbeNow(ctx, Actor{Name: "codex", Via: "api"}, ProbeNowInput{AlertID: id, Wait: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Complete || strings.Join(out.Missing, ",") != "cn-shanghai-bgp" || len(out.Targets) != 2 || len(out.Results) != 2 || out.Summary != "1 OK, 1 did not answer" {
		t.Fatalf("by alert: %+v", out)
	}
	if last := e.bot.texts[len(e.bot.texts)-1]; last != "codex had 2 locations probe 2 targets of git-linkos-io again via API: 1 OK, 1 did not answer" {
		t.Errorf("echo %q", last)
	}
}

// An alert whose labels name the target (instance and module) has that target probed, not the whole service.
func TestProbeNowByAlertInstance(t *testing.T) {
	e := newEnv(t)
	e.b.d.ProbeNow = probeBoard(t)
	ctx := context.Background()
	w := hook("firing", "critical")
	labels := map[string]string{"alertname": "ServiceDown", "service": "git-linkos-io", "severity": "critical", "instance": "https://git.linkos.io/api/healthz", "module": "http_head"}
	w.Alerts = []alertmanager.Alert{{Status: "firing", Fingerprint: "f1", Labels: labels, StartsAt: e.now}, {Status: "firing", Fingerprint: "f2", Labels: labels, StartsAt: e.now}}
	if err := e.b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	answerAs(t, e.b.d.ProbeNow, "us-los-angeles", true)
	answerAs(t, e.b.d.ProbeNow, "cn-shanghai-bgp", true)
	out, err := e.b.ProbeNow(ctx, Actor{Name: "claude-code", Via: "mcp"}, ProbeNowInput{AlertID: GroupID(w.GroupKey), Wait: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Targets) != 1 || out.Targets[0].Instance != "https://git.linkos.io/api/healthz" || !out.Complete || out.Summary != "2 OK" {
		t.Fatalf("by instance: %+v", out)
	}
}
