package bridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/probenow"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

func TestProbeObservationsDoNotCountMissingOrAmbiguousTargetsAsPassing(t *testing.T) {
	const target, module = "https://git.example.com/", "http_head"
	const location = "edge-1"
	want := probenow.Target{Instance: target, Module: module, Locations: []string{location}, Service: "git-example-com"}
	duplicate := want
	duplicate.Service = "another-service"
	v := probenow.View{Done: true, Targets: []probenow.Target{want, duplicate, {Instance: "https://git.example.com/healthz", Module: module, Locations: []string{location}}},
		Results: []probenow.Result{{Location: location, TargetResult: targets.TargetResult{Target: target, Module: module, Success: true}}}}
	observed := probeObservations(v)
	run := store.ProbeRun{ID: "ambiguous-or-missing", At: time.Now(), Results: observed}
	if counted := verificationCounts(&run); counted.Passed != 0 || counted.Failed != 0 || counted.Missing != 3 || verifyPassingCheck(&run) != nil {
		t.Fatalf("one location reply cannot verify omitted or ambiguous targets: %+v", observed)
	}
	run.Results = nil
	if verifyPassingCheck(&run) != nil {
		t.Fatal("an empty request cannot be a passing check")
	}
}

func TestPassingCheckPreferenceUsesCompletionAndKeepsTiesStable(t *testing.T) {
	resolved := time.Date(2026, time.November, 25, 0, 0, 0, 0, time.UTC)
	before := &store.Verification{At: resolved.Add(-time.Minute)}
	at := &store.Verification{At: resolved}
	after := &store.Verification{At: resolved.Add(time.Minute)}
	later := &store.Verification{At: after.At.Add(time.Minute)}
	for _, tc := range []struct {
		name                     string
		current, candidate, want *store.Verification
		resolved                 time.Time
	}{
		{"last while firing", before, at, at, time.Time{}},
		{"first completion after recovery", later, after, after, resolved},
		{"later pass cannot replace first after recovery", after, later, after, resolved},
		{"completion before recovery wins over a post-recovery pass", after, before, before, resolved},
		{"completion at recovery counts with pre-recovery passes", before, at, at, resolved},
		{"older completion cannot replace a closer pass", at, before, at, resolved},
		{"same completion time keeps first stored", at, &store.Verification{At: at.At}, at, resolved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := preferPassingCheck(tc.current, tc.candidate, tc.resolved); got != tc.want {
				t.Fatalf("passing check at %s, want %s", got.At, tc.want.At)
			}
		})
	}
}

// probeBoard has the env's location and a mainland one, both probing git.example.com (the hook's service)
// and a second target of the same service.
func probeBoard(t *testing.T) *probenow.Board {
	t.Helper()
	cat := &catalog.Catalog{Locations: []catalog.Location{
		{ID: "us-los-angeles", Name: "United States - Los Angeles", Region: "overseas"},
		{ID: "cn-shanghai-bgp", Name: "Mainland China - Shanghai", Region: "mainland"},
	}}
	set, err := targets.Build(map[string][]targets.Group{
		"all-git.yaml": {{Targets: []string{"https://git.example.com/", "https://git.example.com/api/healthz"}, Labels: map[string]string{"module": "http_head", "service": "git-example-com"}}},
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
	if _, err := e.b.ProbeNow(ctx, who, ProbeNowInput{Target: "https://git.example.com/"}); !errors.Is(err, ErrProbeUnavailable) {
		t.Fatalf("without a board: %v", err)
	}
	e.b.d.ProbeNow = probeBoard(t)
	for _, c := range []struct {
		in   ProbeNowInput
		want error
		msg  string
	}{
		{ProbeNowInput{}, ErrBadInput, "one of alert_id, target or service"},
		{ProbeNowInput{Target: "https://git.example.com/", Service: "git-example-com"}, ErrBadInput, "one of"},
		{ProbeNowInput{Service: "git-example-com", Module: "http_head"}, ErrBadInput, "module goes with target"},
		{ProbeNowInput{Target: "https://git.example.com/", Wait: 91}, ErrBadInput, "wait is 1 to 90 seconds"},
		{ProbeNowInput{Target: "https://git.example.com/", Wait: -1}, ErrBadInput, "wait is 1 to 90 seconds"},
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
	out, err := e.b.ProbeNow(ctx, who, ProbeNowInput{Target: "https://git.example.com/", Wait: 5})
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
	if last := e.bot.texts[len(e.bot.texts)-1]; last != "claude-code had 2 locations probe https://git.example.com/ again via MCP: 1 OK, 1 failing (http_status)" {
		t.Errorf("echo %q", last)
	}
	if a := e.st.AuditLog(1); len(a) != 1 || a[0].Action != "probe_now" || a[0].Actor != "claude-code" || a[0].Via != "mcp" ||
		a[0].Target != "https://git.example.com/" || a[0].Detail != "1 OK, 1 failing (http_status)" {
		t.Errorf("audit %+v", a)
	}
	// the same target at once is busy
	if _, err := e.b.ProbeNow(ctx, who, ProbeNowInput{Target: "https://git.example.com/", Wait: 1}); !errors.Is(err, ErrProbeBusy) {
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
	if last := e.bot.texts[len(e.bot.texts)-1]; last != "codex had 2 locations probe 2 targets of git-example-com again via API: 1 OK, 1 did not answer" {
		t.Errorf("echo %q", last)
	}
}

// An alert whose labels name the target (instance and module) has that target probed, not the whole service.
func TestProbeNowByAlertInstance(t *testing.T) {
	e := newEnv(t)
	e.b.d.ProbeNow = probeBoard(t)
	ctx := context.Background()
	w := hook("firing", "critical")
	labels := map[string]string{"alertname": "ServiceDown", "service": "git-example-com", "severity": "critical", "instance": "https://git.example.com/api/healthz", "module": "http_head"}
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
	if len(out.Targets) != 1 || out.Targets[0].Instance != "https://git.example.com/api/healthz" || !out.Complete || out.Summary != "2 OK" {
		t.Fatalf("by instance: %+v", out)
	}
}

func TestIncidentProbeKeepsMemberFailureDomains(t *testing.T) {
	const (
		service = "git-example-com"
		green   = "https://green.example.com/healthz"
		blue    = "https://blue.example.com/healthz"
	)
	target := func(instance, domain, svc, module string) targets.Group {
		return targets.Group{Targets: []string{instance}, Labels: map[string]string{"service": svc, "failure_domain": domain, "module": module}}
	}
	greenTarget, blueTarget := target(green, "green", service, "http_head"), target(blue, "blue", service, "http_head")
	member := func(instance, domain string) map[string]string {
		l := map[string]string{"failure_domain": domain}
		if instance != "" {
			l["instance"], l["module"], l["service"] = instance, "http_head", service
		}
		return l
	}
	for _, tc := range []struct {
		name, commonDomain, errorPart string
		members                       []map[string]string
		extras                        []targets.Group
		inactive                      bool
		input                         ProbeNowInput
		want                          []string
	}{
		{name: "removed aggregate", commonDomain: "blue", errorPart: "no target set has service git-example-com in failure domain blue"},
		{name: "only one grouped member matches", members: []map[string]string{member(green, "green"), member("", "blue")}, errorPart: "failure domain blue"},
		{name: "domain assigned only to an expired location", commonDomain: "blue", inactive: true, errorPart: "no active location probes service git-example-com in failure domain blue"},
		{name: "same instance with different domain service or module", members: []map[string]string{member(green, "blue")}, extras: []targets.Group{
			target(green, "blue", "another-service", "http_head"), target(green, "blue", service, "tcp_connect"),
		}, errorPart: "target " + green + " for service git-example-com with module http_head in failure domain blue"},
		{name: "current aggregate keeps just its domain", commonDomain: "blue", extras: []targets.Group{blueTarget}, want: []string{blue}},
		{name: "both different grouped members match", members: []map[string]string{member(green, "green"), member("", "blue")}, extras: []targets.Group{blueTarget}, want: []string{green, blue}},
		{name: "domainless legacy aggregate stays broad", extras: []targets.Group{blueTarget}, want: []string{green, blue}},
		{name: "explicit service stays broad", extras: []targets.Group{blueTarget}, input: ProbeNowInput{Service: service}, want: []string{green, blue}},
		{name: "explicit target keeps every module", extras: []targets.Group{target(green, "blue", service, "tcp_connect")},
			input: ProbeNowInput{Target: green}, want: []string{green, green}},
		{name: "single instance qualifies overlapping target labels", members: []map[string]string{member(green, "blue")},
			extras: []targets.Group{target(green, "blue", service, "http_head")}, want: []string{green}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			cat := &catalog.Catalog{Locations: []catalog.Location{
				{ID: "us-los-angeles", Name: "United States - Los Angeles", Region: "overseas"},
				{ID: "cn-shanghai-bgp", Name: "Mainland China - Shanghai", Region: "mainland"},
			}}
			files := map[string][]targets.Group{"all-scoped.yaml": append([]targets.Group{greenTarget}, tc.extras...)}
			if tc.inactive {
				cat.Resources = []catalog.Resource{{Name: "us-los-angeles", Kind: "vps", Expires: "2026-01-01"}}
				files["us-los-angeles-expired.yaml"] = []targets.Group{blueTarget}
			}
			if err := cat.Validate(); err != nil {
				t.Fatal(err)
			}
			configured, err := targets.Build(files, []string{"us-los-angeles", "cn-shanghai-bgp"})
			if err != nil {
				t.Fatal(err)
			}
			board := probenow.New(probenow.Deps{Catalog: func() *catalog.Catalog { return cat }, Targets: func() *targets.Set { return configured }, Now: func() time.Time { return e.now }})
			e.b.d.ProbeNow = board
			alert := hook("firing", "critical", "one")
			alert.CommonLabels["failure_domain"] = tc.commonDomain
			if len(tc.members) > 0 {
				alert.Alerts = nil
				for idx, labels := range tc.members {
					alert.Alerts = append(alert.Alerts, alertmanager.Alert{Status: "firing", Fingerprint: fmt.Sprint(idx), Labels: labels, StartsAt: e.now})
				}
			}
			if err := e.b.HandleWebhook(t.Context(), alert); err != nil {
				t.Fatal(err)
			}
			id := GroupID(alert.GroupKey)
			in := tc.input
			if in.Service == "" && in.Target == "" {
				in.AlertID = id
			}
			in.Wait = 1
			if tc.errorPart == "" {
				answerAs(t, board, "us-los-angeles", true)
				answerAs(t, board, "cn-shanghai-bgp", true)
			}
			result, err := e.b.ProbeNow(t.Context(), Actor{Name: "operator", Via: "console"}, in)
			saved, _ := e.st.Group(id)
			if tc.errorPart != "" {
				if !errors.Is(err, ErrProbeNotFound) || !strings.Contains(err.Error(), tc.errorPart) || board.Open() != 0 ||
					len(saved.ProbeRuns) != 0 || saved.PassingCheck != nil {
					t.Fatalf("missing scope must fail before dispatch or recording: result=%+v err=%v runs=%+v", result, err, saved.ProbeRuns)
				}
				return
			}
			var instances []string
			for _, match := range result.Targets {
				instances = append(instances, match.Instance)
			}
			want := slices.Clone(tc.want)
			slices.Sort(instances)
			slices.Sort(want)
			if err != nil || !result.Complete || !slices.Equal(instances, want) {
				t.Fatalf("probe was not confined to the expected targets: %+v, %v", result, err)
			}
			if in.AlertID != "" && (saved.PassingCheck == nil || saved.PassingCheck.Passed != 2*len(tc.want)) {
				t.Fatalf("complete observations were not retained: %+v", saved.PassingCheck)
			}
		})
	}
}

func TestGuardedProbeCompletesAfterEpisodeReplacement(t *testing.T) {
	e := newEnv(t)
	board := probeBoard(t)
	e.b.d.ProbeNow = board
	ctx := t.Context()
	first := hook("firing", "critical", "first")
	if err := e.b.HandleWebhook(ctx, first); err != nil {
		t.Fatal(err)
	}
	id := GroupID(first.GroupKey)
	g, ok := e.st.Group(id)
	if !ok || g.Episode == "" {
		t.Fatal("missing first episode")
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.b.ProbeNow(ctx, Actor{Name: "recovery", Via: "api"}, ProbeNowInput{AlertID: id, Episode: &g.Episode, Wait: 5})
		done <- err
	}()
	reqs := board.Wait(ctx, "us-los-angeles", 2*time.Second)
	if len(reqs) != 1 {
		t.Fatalf("the first episode's check was not queued: %v", reqs)
	}
	if err := e.b.HandleWebhook(ctx, hook("resolved", "critical", "first")); err != nil {
		t.Fatal(err)
	}
	if err := e.b.HandleWebhook(ctx, hook("firing", "critical", "second")); err != nil {
		t.Fatal(err)
	}
	var passing []targets.TargetResult
	for _, group := range reqs[0].Groups {
		for _, target := range group.Targets {
			passing = append(passing, targets.TargetResult{Target: target, Module: group.Labels["module"], Success: true, HTTPStatus: 200})
		}
	}
	for _, location := range []string{"us-los-angeles", "cn-shanghai-bgp"} {
		if err := board.Answer(reqs[0].ID, location, passing); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; !errors.Is(err, ErrEpisodeChanged) {
		t.Fatalf("a replaced episode returned a successful verification: %v", err)
	}
	current, _ := e.st.Group(id)
	if current.Episode == g.Episode || len(current.ProbeRuns) != 0 || current.PassingCheck != nil {
		t.Fatalf("the passing check changed the replacement episode: %q, %v, %+v", current.Episode, current.ProbeRuns, current.PassingCheck)
	}
	audit := e.st.AuditLog(1)
	if len(audit) != 1 || audit[0].Action != "probe_now" || !strings.Contains(audit[0].Detail, g.Episode) {
		t.Fatalf("the abandoned check lacks an audit record for the first episode: %+v", audit)
	}
}
