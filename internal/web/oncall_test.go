package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/probenow"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
	"github.com/openlinkos/beacon/internal/web/link"
)

var expectedAck = regexp.MustCompile(`name="expected_ack" value="([0-9a-f]+)"`)
var ackNoteSubmit = regexp.MustCompile(`id="ack-note"[^>]*></textarea>\s*<button type="submit" form="alert-ack"[^>]*>([^<]+)</button>`)

func ackForm(t *testing.T, page answer, note string) url.Values {
	t.Helper()
	field := expectedAck.FindStringSubmatch(page.body)
	if page.code != http.StatusOK || len(field) != 2 {
		t.Fatalf("acknowledgement form missing on page with status %d", page.code)
	}
	return url.Values{"expected_ack": {field[1]}, "note": {note}}
}

func alertResponse(t *testing.T, a answer) string {
	t.Helper()
	_, body, found := strings.Cut(a.body, `aria-labelledby="alert-response">`)
	if a.code != http.StatusOK || !found {
		t.Fatalf("the alert response was missing from the page with status %d", a.code)
	}
	body, _, _ = strings.Cut(body, "</section>")
	return body
}

func TestHumanTakeoverAndShiftHandover(t *testing.T) {
	e := newEnv(t, i18n.EN)
	owner := e.owner()
	first := e.invite(owner, "first-shift", "oncall")
	second := e.invite(owner, "next-shift", "oncall")
	reader := e.invite(owner, "reader", "viewer")
	path := link.AlertPath(e.id)
	post := path + "/ack"
	unclaimed := second.get(path)
	stale := ackForm(t, unclaimed, "A note from an earlier view.")
	if m := ackNoteSubmit.FindStringSubmatch(unclaimed.body); len(m) != 2 || m[1] != "Acknowledge with note" {
		t.Fatalf("no form submit after the acknowledgement note: %q", m)
	}

	who := bridge.ActorOf(store.Principal{User: "first-shift", Agent: "responder"}, "mcp")
	if _, err := e.b.Ack(context.Background(), e.id, who, "looking into it"); err != nil {
		t.Fatal(err)
	}
	show := first.get(path)
	if m := ackNoteSubmit.FindStringSubmatch(show.body); len(m) != 2 || m[1] != "Take over with note" {
		t.Fatalf("no form submit after the takeover note: %q", m)
	}
	for _, want := range []string{"Agent acknowledgement from first-shift / responder", "Take over",
		"A hook accepted the alert. Check the timeline for Agent activity.", "Notification accepted by the group bot"} {
		if !strings.Contains(show.body, want) {
			t.Errorf("takeover page lacks %q", want)
		}
	}
	if a := reader.get(path); strings.Contains(a.body, `id="alert-ack"`) {
		t.Fatal("viewer can see takeover controls")
	}
	if a := second.post(post, stale); a.header.Get("Location") != path+"?error=ack_changed" {
		t.Fatalf("stale form redirected to %q", a.header.Get("Location"))
	}
	if a := first.get(path); strings.Contains(a.body, stale.Get("note")) {
		t.Fatal("a note from a stale takeover was saved")
	}
	if a := first.post(post, ackForm(t, show, "Checked the failed dependency.")); a.header.Get("Location") != path+"?done=ack" {
		t.Fatalf("first shift could not claim it: %q", a.header.Get("Location"))
	}
	if _, err := e.b.Ack(context.Background(), e.id, who, "finished initial checks"); err != nil {
		t.Fatal(err)
	}
	show = second.get(path)
	if !strings.Contains(show.body, "Acknowledged by first-shift at") || !strings.Contains(show.body, "Take over") {
		t.Fatal("Agent update displaced the first shift or omitted handover")
	}
	response := alertResponse(t, show)
	for _, want := range []string{"Handover note", "Checked the failed dependency.", "Escalation is paused while this alert is acknowledged."} {
		if !strings.Contains(response, want) {
			t.Errorf("the takeover response lacks %q", want)
		}
	}
	if strings.Contains(response, "finished initial checks") || strings.Contains(response, "Read full handover note") {
		t.Fatal("the response added an Agent note or a disclosure for a short handover")
	}
	if a := second.post(post, ackForm(t, show, "Continuing the dependency investigation.")); a.header.Get("Location") != path+"?done=ack" {
		t.Fatalf("next shift could not take over: %q", a.header.Get("Location"))
	}
	show = second.get(path)
	response = alertResponse(t, show)
	if !strings.Contains(response, "Continuing the dependency investigation.") || strings.Contains(response, "Checked the failed dependency.") {
		t.Fatal("takeover did not replace the current handover")
	}
	for _, want := range []string{"Acknowledged by next-shift at", "Acknowledged by first-shift via the console",
		"Checked the failed dependency.", "Continuing the dependency investigation.", "finished initial checks"} {
		if !strings.Contains(show.body, want) {
			t.Errorf("handover timeline lacks %q", want)
		}
	}
	for _, person := range []string{"first-shift", "next-shift"} {
		if !audited(e.st, person, "ack") {
			t.Errorf("missing durable acknowledgement for %s", person)
		}
	}
}

func TestAlertResponseKeepsNotificationAndOwnershipSeparate(t *testing.T) {
	e := newEnv(t, i18n.EN)
	viewer := e.invite(e.owner(), "duty", "oncall")
	path := link.AlertPath(e.id)
	g, _ := e.st.Group(e.id)
	g.Agents[0].Status = "failed"
	g.Agents[0].Error = "HTTP 503"
	if err := e.st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	response := alertResponse(t, viewer.get(path))
	for _, want := range []string{
		"Awaiting human acknowledgement", "Notification accepted by the group bot",
		`class="ol-status ol-status-danger" href="#timeline"><span class="ol-dot" aria-hidden="true"></span>Agent hook delivery failed.`,
	} {
		if !strings.Contains(response, want) {
			t.Errorf("the response lacks %q", want)
		}
	}
	actor := bridge.ActorOf(store.Principal{User: "duty", Agent: "responder"}, "mcp")
	if _, err := e.b.Ack(context.Background(), e.id, actor, "Agent acknowledgement note"); err != nil {
		t.Fatal(err)
	}
	response = alertResponse(t, viewer.get(path))
	if !strings.Contains(response, "Awaiting a person. Agent acknowledgement") ||
		strings.Contains(response, "Handover note") || strings.Contains(response, "Escalation is paused") {
		t.Fatal("an Agent acknowledgement implied a human handover or paused escalation")
	}

	note := strings.Repeat("Check the upstream. ", 15) + hostile
	if a := viewer.post(path+"/ack", ackForm(t, viewer.get(path), note)); a.header.Get("Location") != path+"?done=ack" {
		t.Fatalf("could not acknowledge the alert: %q", a.header.Get("Location"))
	}
	response = alertResponse(t, viewer.get(path))
	if !strings.Contains(response, "Read full handover note") || !strings.Contains(response, "&lt;script&gt;alert(1)&lt;/script&gt;") ||
		strings.Contains(response, hostile) {
		t.Fatal("the current handover does not expose its full text safely")
	}
	if _, err := e.b.Silence(context.Background(), e.id, time.Hour, bridge.Actor{Name: "duty", Via: "console"}, "known outage"); err != nil {
		t.Fatal(err)
	}
	response = alertResponse(t, viewer.get(path))
	if !strings.Contains(response, "Silenced until 2026-11-02 11:00:00 by duty") || strings.Contains(response, "Escalation is paused") {
		t.Fatal("the response did not show the active silence")
	}
	g, _ = e.st.Group(e.id)
	g.Status, g.ResolvedAt = "resolved", e.now
	if err := e.st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	response = alertResponse(t, viewer.get(path))
	if strings.Contains(response, "Handover note") || strings.Contains(response, "Agent hook delivery failed.") {
		t.Fatal("a resolved alert showed handover or wake delivery as current")
	}
}

func TestSignedAlertReturnsToAlertAfterSignIn(t *testing.T) {
	e := newEnv(t, i18n.EN)
	owner := e.owner()
	e.invite(owner, "operator", "oncall")
	b := e.browser()
	path := link.AlertPath(e.id)
	a := b.get(e.link(path))
	signIns := hrefs(a.body, "/console/sign-in?")
	if a.code != http.StatusOK || len(signIns) == 0 || strings.Contains(a.body, `id="alert-ack"`) {
		t.Fatalf("signed alert: status %d, sign-in links %v", a.code, signIns)
	}
	for _, href := range signIns {
		u, err := url.Parse(href)
		if err != nil || u.Path != "/console/sign-in" || u.Query().Get("next") != path {
			t.Fatalf("sign-in destination %q: %v", href, err)
		}
	}
	p := b.get(signIns[0])
	form := formRe.FindStringSubmatch(p.body)
	if p.code != http.StatusOK || form == nil || !strings.Contains(p.body, `name="next" value="`+path+`"`) {
		t.Fatalf("sign-in page did not retain the unsigned alert path (status %d)", p.code)
	}
	bad := b.do(http.MethodPost, "/console/sign-in", url.Values{
		"username": {"operator"}, "password": {"wrong password"}, "form": {form[1]}, "next": {path},
	})
	form = formRe.FindStringSubmatch(bad.body)
	if bad.code != http.StatusUnauthorized || form == nil || !strings.Contains(bad.body, `role="alert" id="sign-in-error"`) ||
		!strings.Contains(bad.body, `aria-describedby="sign-in-error"`) {
		t.Fatalf("sign-in error is not associated with the form (status %d)", bad.code)
	}
	signedIn := b.do(http.MethodPost, "/console/sign-in", url.Values{
		"username": {"operator"}, "password": {password}, "form": {form[1]}, "next": {path},
	})
	if signedIn.code != http.StatusSeeOther || signedIn.header.Get("Location") != path {
		t.Fatalf("sign-in returned status %d, location %q", signedIn.code, signedIn.header.Get("Location"))
	}
	a = b.get(path)
	ack := strings.Index(a.body, `id="alert-ack"`)
	evidence := strings.Index(a.body, `id="timeline"`)
	if a.code != http.StatusOK || ack < 0 || ack >= evidence || !strings.Contains(a.body, "Acknowledging stops escalation") {
		t.Fatalf("signed-in on-call controls missing or below evidence (status %d, positions %d/%d)", a.code, ack, evidence)
	}
}

func TestAlertProbeNowShowsResultOnTimeline(t *testing.T) {
	locations := []catalog.Location{
		{ID: "edge-1", Name: "Europe West", Region: "overseas"},
		{ID: "edge-2", Name: "Asia East", Region: "mainland"},
	}
	set, err := targets.Build(map[string][]targets.Group{
		"all-web.yaml": {{Targets: []string{"https://git.example.com/"}, Labels: map[string]string{"module": "http_head", "service": "git-example-com"}}},
	}, []string{"edge-1", "edge-2"})
	if err != nil {
		t.Fatal(err)
	}
	board := probenow.New(probenow.Deps{
		Catalog: func() *catalog.Catalog { return &catalog.Catalog{Locations: locations} },
		Targets: func() *targets.Set { return set },
	})
	e := newEnvWithProbe(t, i18n.EN, board)
	owner := e.owner()
	b := e.invite(owner, "oncall-user", "oncall")
	viewer := e.invite(owner, "reader", "viewer")
	path := link.AlertPath(e.id)
	post := path + "/probe-now"
	for _, who := range []*browser{e.browser(), viewer} {
		view := path
		if who.cookies[sessionCookie] == nil {
			view = e.link(path)
		}
		a := who.get(view)
		if a.code != http.StatusOK || strings.Contains(a.body, `action="`+post+`"`) {
			t.Fatalf("read-only alert returned status %d or offered a check", a.code)
		}
	}
	if a := viewer.post(post, nil); a.code != http.StatusForbidden {
		t.Fatalf("read-only POST returned %d", a.code)
	}
	if a := b.get(path); a.code != http.StatusOK || !strings.Contains(a.body, `action="`+post+`"`) {
		t.Fatalf("on-call probe control missing (status %d)", a.code)
	}
	if a := b.post(post, url.Values{"csrf": {"invalid"}}); a.code != http.StatusForbidden {
		t.Fatalf("invalid CSRF returned %d", a.code)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch := make(chan error, 2)
	for _, loc := range locations {
		go func(loc string) {
			req := board.Wait(ctx, loc, 4*time.Second)
			if len(req) != 1 {
				ch <- fmt.Errorf("%s got %d requests", loc, len(req))
				return
			}
			var got []targets.TargetResult
			if loc == "edge-1" {
				got = []targets.TargetResult{{Target: "https://git.example.com/", Module: "http_head", HTTPStatus: 502, Reason: "http_status"}}
			}
			ch <- board.Answer(req[0].ID, loc, got)
		}(loc.ID)
	}
	a := b.post(post, nil)
	for range locations {
		if err := <-ch; err != nil {
			t.Fatal(err)
		}
	}
	u, err := url.Parse(a.header.Get("Location"))
	if err != nil || a.code != http.StatusSeeOther || u.Path != path || u.Query().Get("probe") == "" || u.Fragment != "probe-"+u.Query().Get("probe") {
		t.Fatalf("probe redirected with %d to %q: %v", a.code, a.header.Get("Location"), err)
	}
	show := b.get(u.RequestURI())
	for _, want := range []string{u.Fragment, "oncall-user ran an on-demand check via the console", "0 passed, 1 failed, 1 missing",
		"HTTP 502", "No result", `class="bc-alert-observations" open`, "Awaiting human acknowledgement", "Alert delivered to hook oncall",
		`<summary id="` + u.Fragment + `"><span class="ol-sr-only">On-demand check results. </span>0 passed, 1 failed, 1 missing`,
		`id="timeline" tabindex="-1" aria-labelledby="alert-timeline"`, `src="/static/beacon-probe-result.js?v=`} {
		if show.code != http.StatusOK || !strings.Contains(show.body, want) {
			t.Errorf("alert result lacks %q (status %d)", want, show.code)
		}
	}
	audit := e.st.AuditLog(1)[0]
	if audit.Action != "probe_now" || audit.Actor != "oncall-user" || audit.Via != "console" || audit.Target != e.id || !strings.Contains(audit.Detail, u.Query().Get("probe")) {
		t.Errorf("audit event: %+v", audit)
	}
}

func TestAlertSeparatesPassingOnDemandCheckFromRuleRecovery(t *testing.T) {
	e := newEnv(t, i18n.EN)
	viewer := e.signedIn("reader", "status:read")
	g, ok := e.st.Group(e.id)
	if !ok {
		t.Fatal("the fixture alert was missing")
	}
	g.ProbeRuns = []store.ProbeRun{
		{ID: "passed", At: e.now, Results: []store.ProbeObservation{{Location: "edge-1", Status: "ok"}, {Location: "edge-2", Status: "ok"}}},
		{ID: "failed", At: e.now, Results: []store.ProbeObservation{{Location: "edge-1", Status: "ok"}, {Location: "edge-2", Status: "failed"}}},
		{ID: "missing", At: e.now, Results: []store.ProbeObservation{{Location: "edge-1", Status: "ok"}, {Location: "edge-2", Status: "missing"}}},
		{ID: "empty", At: e.now},
	}
	if err := e.st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	path := link.AlertPath(e.id)
	const hint = "The monitoring rule still reports this alert as firing."
	passed := viewer.get(path + "?probe=passed")
	if passed.code != http.StatusOK || strings.Count(passed.body, hint) != 1 ||
		!strings.Contains(passed.body, `id="probe-passed" aria-describedby="probe-passed-recovery"`) ||
		!strings.Contains(passed.body, `id="probe-passed-recovery">`+hint) {
		t.Fatal("selected, passing check lacks its recovery context")
	}
	for _, id := range []string{"", "failed", "missing", "empty"} {
		if page := viewer.get(path + "?probe=" + id); page.code != http.StatusOK || strings.Contains(page.body, hint) {
			t.Errorf("a check other than the selected passing one showed recovery context: %q", id)
		}
	}
	g.Status, g.ResolvedAt = "resolved", e.now
	if err := e.st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	if page := viewer.get(path + "?probe=passed"); page.code != http.StatusOK ||
		!strings.Contains(page.body, "Alert resolved") || strings.Contains(page.body, hint) {
		t.Error("finished alert still shows the passing check as awaiting rule recovery")
	}
}
