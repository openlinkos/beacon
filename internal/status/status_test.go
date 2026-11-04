package status

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
	"github.com/openlinkos/beacon/internal/vm"
)

// fakeVM answers the agreement query from a model: the state of each service at each minute (the
// minute that ends at t), as VictoriaMetrics would have computed it from the probes.
type fakeVM struct {
	mu      sync.Mutex
	calls   int
	queries []string
	state   func(svc string, t time.Time) byte
}

var servicesRe = regexp.MustCompile(`service=~?"([^"]*)"`)

// servicesIn reads the services a selector of the agreement query names.
func servicesIn(q string) []string {
	m := servicesRe.FindStringSubmatch(q)
	if m == nil {
		return nil
	}
	var out []string
	for _, s := range strings.Split(m[1], "|") {
		out = append(out, strings.ReplaceAll(s, `\\`, ""))
	}
	return out
}

func (f *fakeVM) QueryRange(_ context.Context, q string, start, end time.Time, step time.Duration) ([]vm.Series, error) {
	f.mu.Lock()
	f.calls++
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	var out []vm.Series
	for _, svc := range servicesIn(q) {
		s := vm.Series{Labels: map[string]string{"service": svc}}
		for t := start; !t.After(end); t = t.Add(step) {
			if v := f.state(svc, t); v != minNone {
				s.Points = append(s.Points, vm.Point{T: t, V: float64(v)})
			}
		}
		if len(s.Points) > 0 {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeVM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

const catalogue = `locations:
  - {id: edge-1, name: United States - Los Angeles, region: overseas, network: CN2 GIA}
  - {id: edge-2, name: Germany - Frankfurt, region: overseas}
  - {id: edge-3, name: Mainland China - Shanghai, region: mainland}
status_pages:
  - name: public
    title: {en: Example Status, zh: Example Status ZH}
    description: Whether Example's public services work.
    host: uptime.example.com
    languages: [en, zh]
    components:
      - {name: mirrors, title: Mirrors, page: mirrors}
      - {name: api, title: API, description: The public API, services: [api.example.com]}
    groups:
      - {title: Registries, components: [mirrors]}
  - name: all-notes
    title: Everything
    notes: all
    components:
      - {name: api, title: API, services: [api.example.com]}
`

const webTargets = `- targets: [https://docker.example.com/v2/]
  labels: {module: http_head, service: docker.example.com, page: mirrors}
- targets: [https://ghcr.example.com/v2/]
  labels: {module: http_head, service: ghcr.example.com, page: mirrors}
- targets: [https://mirror-origin.example.net/healthz]
  labels: {module: http_head, service: mirror-origin, page: mirrors, visibility: internal}
- targets: [https://api.example.com/healthz]
  labels: {module: http_head, service: api.example.com, severity: critical}
`

type env struct {
	t   *testing.T
	now time.Time
	st  *store.Store
	cdb *configdb.Service
	b   *bridge.Bridge
	am  *testutil.AM
	vm  *fakeVM
	s   *Service
	// state is what the probes say of a service at t; operational everywhere by default
	state map[string]func(t time.Time) byte
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, now: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), am: &testutil.AM{}, state: map[string]func(time.Time) byte{}}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e.st = st
	now := func() time.Time { return e.now }
	e.cdb = configdb.Open(configdb.Deps{Store: st, Lang: i18n.EN, Now: now})
	_, err = e.cdb.Import(context.Background(), configdb.Actor{Name: "test", Via: "cli"},
		configdb.Files{"catalog/catalog.yaml": catalogue, "targets/all-web.yaml": webTargets}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Language: "en", Location: time.UTC, AckTTL: config.Duration(4 * time.Hour), Feishu: config.Feishu{MonthlyBudget: 1000},
		OnCallByID: map[string]config.OnCaller{}, Escalation: map[string]config.Policy{"critical": {Channel: "webhook"}, "warning": {Channel: "webhook"}}}
	e.b = bridge.New(bridge.Deps{Config: cfg, Store: st, AM: e.am, Bot: &testutil.Bot{}, Now: now})
	e.vm = &fakeVM{state: func(svc string, t time.Time) byte {
		if f, ok := e.state[svc]; ok {
			return f(t)
		}
		return minOK
	}}
	e.s = New(Deps{Config: e.cdb, Bridge: e.b, Store: st, VM: e.vm, PublicURL: "https://beacon.example.com", Location: time.UTC, Now: now})
	return e
}

// fire posts an alert group for the services, as the router would.
func (e *env) fire(alertname, severity string, starts time.Time, svcs ...string) string {
	e.t.Helper()
	key := "{}:{alertname=\"" + alertname + "\",services=\"" + strings.Join(svcs, ",") + "\"}"
	w := alertmanager.Webhook{Version: "4", GroupKey: key, Status: "firing",
		GroupLabels:  map[string]string{"alertname": alertname},
		CommonLabels: map[string]string{"alertname": alertname, "severity": severity}}
	if len(svcs) == 1 {
		w.GroupLabels["service"], w.CommonLabels["service"] = svcs[0], svcs[0]
	}
	for i, s := range svcs {
		w.Alerts = append(w.Alerts, alertmanager.Alert{Status: "firing", Fingerprint: s + string(rune('a'+i)), StartsAt: starts,
			Labels: map[string]string{"alertname": alertname, "service": s, "severity": severity}})
	}
	if err := e.b.HandleWebhook(context.Background(), w); err != nil {
		e.t.Fatal(err)
	}
	return bridge.GroupID(key)
}

func (e *env) resolve(alertname string, svcs ...string) {
	e.t.Helper()
	key := "{}:{alertname=\"" + alertname + "\",services=\"" + strings.Join(svcs, ",") + "\"}"
	w := alertmanager.Webhook{Version: "4", GroupKey: key, Status: "resolved",
		GroupLabels: map[string]string{"alertname": alertname}, CommonLabels: map[string]string{"alertname": alertname}}
	for i, s := range svcs {
		w.Alerts = append(w.Alerts, alertmanager.Alert{Status: "resolved", Fingerprint: s + string(rune('a'+i)), Labels: map[string]string{"service": s}})
	}
	if err := e.b.HandleWebhook(context.Background(), w); err != nil {
		e.t.Fatal(err)
	}
}

// page computes a page afresh, the probes' minutes too, as it is after the caches have run out.
func (e *env) page(name, lang string) *Page {
	e.t.Helper()
	e.s.mu.Lock()
	clear(e.s.pages)
	e.s.mu.Unlock()
	e.s.hist = newHistory()
	p, err := e.s.Page(context.Background(), name, lang)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func component(t *testing.T, p *Page, name string) Component {
	t.Helper()
	c, ok := p.Component(name)
	if !ok {
		t.Fatalf("no component %s", name)
	}
	return c
}

// between is a state for [from, to), operational outside.
func between(from, to time.Time, state byte) func(time.Time) byte {
	return func(t time.Time) byte {
		minute := t.Add(-time.Minute) // the minute that ends at t
		if !minute.Before(from) && minute.Before(to) {
			return state
		}
		return minOK
	}
}

func TestAgreementQuery(t *testing.T) {
	q := agreement(selector([]string{"api.example.com", "docker.example.com"}))
	for _, want := range []string{
		`max by (service, location) (max_over_time(probe_success{job="probe",service=~"api\\.example\\.com|docker\\.example\\.com"}[3m])) == bool 0`,
		"sum by (service) ", "count by (service) ", ">= bool 2", "* 2 >= bool", "1 + (",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("the query lacks %s:\n%s", want, q)
		}
	}
	if s := selector([]string{"one"}); s != `service="one"` {
		t.Errorf("one service: %s", s)
	}
	// a label from the targets that could change the query is never put into one; many services take
	// several queries, so that none grows too long
	f := &fakeVM{state: func(string, time.Time) byte { return minOK }}
	svcs := []string{`bad"} or vector(1) #`}
	for i := range perQuery + 1 {
		svcs = append(svcs, fmt.Sprintf("svc-%d", i))
	}
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	mins, err := query(context.Background(), f, svcs, dayStart(now, time.UTC, 0), now)
	if err != nil || f.calls != 2 || len(mins) != perQuery+2 || mins[svcs[0]][0] != minNone || mins["svc-0"][0] != minOK {
		t.Errorf("queries: %d, %v", f.calls, err)
	}
	for _, q := range f.queries {
		if strings.Contains(q, "vector(1)") {
			t.Errorf("an unsafe label reached a query: %s", q)
		}
	}
}

func TestComponentsFromTargets(t *testing.T) {
	e := newEnv(t)
	p := e.page("public", "")
	if got := component(t, p, "mirrors").services; !slices.Equal(got, []string{"docker.example.com", "ghcr.example.com"}) {
		t.Errorf("the mirrors component is backed by %v: a target labelled visibility: internal is never on a page", got)
	}
	if p.Title != "Example Status" || p.Language != "en" || !slices.Equal(p.Languages, []string{"en", "zh"}) {
		t.Errorf("page: %+v", p)
	}
	if p.URL != "https://uptime.example.com/" || p.Links.JSON != "https://uptime.example.com/status.json" || p.Links.Feed != "https://uptime.example.com/feed.atom" {
		t.Errorf("a page with its own host is linked there: %+v", p.Links)
	}
	if zh := e.page("public", "zh-CN"); zh.Title != "Example Status ZH" || zh.Language != "zh" || zh.Summary != i18n.T(i18n.ZH, "All systems operational") {
		t.Errorf("Chinese: %q %q %q", zh.Title, zh.Language, zh.Summary)
	}
	if fr := e.page("public", "fr"); fr.Language != "en" {
		t.Errorf("a language the page lacks: %s", fr.Language)
	}
	if len(p.Locations) != 3 || p.Locations[2] != "United States - Los Angeles (CN2 GIA)" {
		t.Errorf("locations: %v", p.Locations)
	}
	if len(p.Groups) != 1 || component(t, p, "mirrors").Group != "Registries" || component(t, p, "api").Group != "" {
		t.Errorf("groups: %+v", p.Groups)
	}
	if def, ok := e.s.Def(""); !ok || def.Name != "all-notes" {
		t.Errorf("without a default, /status is the first page by name: %v", def.Name)
	}
	if def, ok := e.s.ByHost("uptime.example.com"); !ok || def.Name != "public" {
		t.Errorf("by host: %v %v", def.Name, ok)
	}
	if _, err := e.s.Page(context.Background(), "nope", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown page: %v", err)
	}
}

func TestStateFromProbes(t *testing.T) {
	e := newEnv(t)
	p := e.page("public", "")
	if p.Status != Operational || p.Summary != "All systems operational" {
		t.Errorf("all up: %s %q", p.Status, p.Summary)
	}
	// the probes agree the API is down in the last minutes: the page says so before any alert fires
	e.state["api.example.com"] = between(e.now.Add(-4*time.Minute), e.now, minDown)
	e.state["docker.example.com"] = between(e.now.Add(-4*time.Minute), e.now, minDegraded)
	p = e.page("public", "")
	if c := component(t, p, "api"); c.Status != Down {
		t.Errorf("api: %s", c.Status)
	}
	if c := component(t, p, "mirrors"); c.Status != Degraded {
		t.Errorf("mirrors, one of whose services fails from some locations: %s", c.Status)
	}
	if p.Status != Down || p.Summary != "API is down" {
		t.Errorf("overall: %s %q", p.Status, p.Summary)
	}
	if len(p.Incidents) != 0 {
		t.Errorf("no alert, no incident: %+v", p.Incidents)
	}
	// nothing reports: no data
	e.state["api.example.com"] = func(time.Time) byte { return minNone }
	e.state["docker.example.com"] = func(time.Time) byte { return minNone }
	e.state["ghcr.example.com"] = func(time.Time) byte { return minNone }
	p = e.page("public", "")
	if p.Status != NoData || p.Summary != "No data yet" || component(t, p, "api").History[Days-1].Status != NoData {
		t.Errorf("no data: %s %q", p.Status, p.Summary)
	}
}

func TestIncidentsFromAlerts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	started := e.now.Add(-20 * time.Minute)
	id := e.fire("ServiceDown", "critical", started, "api.example.com")
	e.fire("CertificateExpiring", "warning", started, "api.example.com") // not an availability alert
	e.fire("ServiceDegraded", "warning", started, "docker.example.com", "other.example.com")
	if _, err := e.b.AddNote(ctx, id, bridge.Actor{Name: "claude-oncall", Via: "mcp"}, "public: The API's database filled its disk; we are freeing space. <b>hi</b>"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.AddNote(ctx, id, bridge.Actor{Name: "alex", Via: "console"}, "restarted postgres on edge-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.Ack(ctx, id, bridge.Actor{Name: "alex", Via: "console"}, ""); err != nil {
		t.Fatal(err)
	}
	p := e.page("public", "")
	// the probes are fine, but the alert fires: the page agrees with the alert
	if c := component(t, p, "api"); c.Status != Down {
		t.Errorf("api while ServiceDown fires: %s", c.Status)
	}
	if c := component(t, p, "mirrors"); c.Status != Degraded {
		t.Errorf("mirrors while ServiceDegraded fires for one of its services: %s", c.Status)
	}
	if len(p.Incidents) != 2 {
		t.Fatalf("incidents: %+v", p.Incidents)
	}
	in, ok := p.Incident(id)
	if !ok || in.Status != "active" || in.Impact != Down || in.Title != "Outage: API" || !slices.Equal(in.Components, []string{"api"}) ||
		in.URL != "https://uptime.example.com/incidents/"+id {
		t.Fatalf("the outage: %+v", in)
	}
	kinds := []string{}
	for _, u := range in.Updates {
		kinds = append(kinds, u.Kind)
	}
	if !slices.Equal(kinds, []string{"note", "acknowledged", "detected"}) {
		t.Errorf("updates, the newest first, only the note marked public: %v", kinds)
	}
	if u := in.Updates[0]; u.By != "agent" || u.Text != "The API's database filled its disk; we are freeing space. <b>hi</b>" {
		t.Errorf("the agent's public note: %+v", u)
	}
	for _, other := range p.Incidents {
		if other.ID != id && (other.Impact != Degraded || other.Title != "Partial outage: Mirrors" || !slices.Equal(other.Components, []string{"mirrors"})) {
			t.Errorf("the partial outage: %+v", other)
		}
	}
	// a page that publishes every note
	all := e.page("all-notes", "")
	if in, _ := all.Incident(id); len(in.Updates) != 4 || in.Updates[0].By != "person" || in.Updates[0].Text != "restarted postgres on edge-1" {
		t.Errorf("with notes: all: %+v", in.Updates)
	}
	if zh, _ := e.page("public", "zh").Incident(id); zh.Title != i18n.T(i18n.ZH, "Outage: %s", "API") {
		t.Errorf("Chinese title: %q", zh.Title)
	}

	// it resolves: the API is back, the incident is in the past with its end
	e.now = e.now.Add(10 * time.Minute)
	e.resolve("ServiceDown", "api.example.com")
	p = e.page("public", "")
	if c := component(t, p, "api"); c.Status != Operational {
		t.Errorf("api after the outage: %s", c.Status)
	}
	in, _ = p.Incident(id)
	if in.Status != "resolved" || in.Resolved == nil || !in.Resolved.Equal(e.now) || in.Updates[0].Kind != "resolved" {
		t.Errorf("resolved: %+v", in)
	}
	today := component(t, p, "api").History[Days-1]
	if today.Status != Down || !slices.Contains(today.Incidents, id) || today.DownMinutes != 30 {
		t.Errorf("today's history holds the outage, minute by minute: %+v", today)
	}

	// the alert group is forgotten after a week; the incident stays as long as the history
	e.st.DeleteGroups(func(store.Group) bool { return true })
	e.now = e.now.Add(8 * 24 * time.Hour)
	p = e.page("public", "")
	if in, ok := p.Incident(id); !ok || in.Status != "resolved" {
		t.Errorf("the incident outlives its group: %+v %v", in, ok)
	}
	day := component(t, p, "api").History[Days-1-8]
	if day.Status != Down {
		t.Errorf("eight days on, that day is down: %+v", day)
	}
}

func TestHistory(t *testing.T) {
	e := newEnv(t)
	day := func(d int) time.Time { return dayStart(e.now, time.UTC, d) }
	e.state["api.example.com"] = func(t time.Time) byte {
		m := t.Add(-time.Minute)
		switch {
		case m.Before(day(Days - 1)):
			return minNone
		case !m.Before(day(10)) && m.Before(day(9)): // ten days ago: nothing reported
			return minNone
		case !m.Before(day(4).Add(time.Hour)) && m.Before(day(4).Add(time.Hour+3*time.Minute)):
			return minDegraded // three minutes partly down
		case !m.Before(day(3).Add(2*time.Hour)) && m.Before(day(3).Add(2*time.Hour+5*time.Minute)):
			return minDown // five minutes down
		case m.Equal(day(2).Add(3 * time.Hour)):
			return minDown // a single minute: not enough to mark the day
		}
		return minOK
	}
	if err := e.s.refresh(context.Background(), 0); err != nil { // as Run does at the start
		t.Fatal(err)
	}
	p, err := e.s.Page(context.Background(), "public", "")
	if err != nil {
		t.Fatal(err)
	}
	h := component(t, p, "api").History
	if len(h) != Days || h[Days-1].Date != "2026-11-02" || h[0].Date != "2026-08-05" {
		t.Fatalf("history: %d days, %s to %s", len(h), h[0].Date, h[len(h)-1].Date)
	}
	at := func(d int) Day { return h[Days-1-d] }
	if d := at(3); d.Status != Down || d.DownMinutes != 5 || *d.Availability != 99.65 {
		t.Errorf("five minutes down: %+v %v", d, *d.Availability)
	}
	if d := at(4); d.Status != Degraded || d.DegradedMinutes != 3 || *d.Availability != 100 {
		t.Errorf("three minutes partly down: %+v", d)
	}
	if d := at(2); d.Status != Operational || d.DownMinutes != 1 || *d.Availability != 99.93 {
		t.Errorf("one minute down: %+v %v", d, *d.Availability)
	}
	if d := at(10); d.Status != NoData || d.Availability != nil {
		t.Errorf("no data: %+v", d)
	}
	if d := at(1); d.Status != Operational || *d.Availability != 100 {
		t.Errorf("a good day: %+v", d)
	}
	if d := at(0); d.Status != Operational {
		t.Errorf("today, partly: %+v", d)
	}
	if a := component(t, p, "api").Availability; a == nil || *a >= 100 || *a < 99.99 {
		t.Errorf("availability over 90 days: %v", a)
	}

	// the past days are kept; within the cache nothing is asked again, after it only today
	calls := e.vm.count()
	if calls != Days {
		t.Errorf("90 days took %d queries, one a day for every page's services", calls)
	}
	if _, err := e.s.Page(context.Background(), "public", ""); err != nil || e.vm.count() != calls {
		t.Errorf("within the cache: %d calls", e.vm.count()-calls)
	}
	e.now = e.now.Add(refreshToday + time.Second)
	if _, err := e.s.Page(context.Background(), "public", ""); err != nil || e.vm.count() != calls+1 {
		t.Errorf("after the cache, only today is asked again: %d calls", e.vm.count()-calls)
	}
}

func TestMaintenance(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	who := bridge.Actor{Name: "claude-oncall", Via: "mcp"}
	in := bridge.MaintenanceInput{Services: []string{"api.example.com"}, StartsAt: e.now.Add(time.Hour), EndsAt: e.now.Add(3 * time.Hour),
		Title: "Database upgrade", Description: "The API answers read-only."}
	m, err := e.b.ScheduleMaintenance(ctx, who, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.am.Created) != 1 || e.am.Created[0]["service"] != "api.example.com" || len(m.Silences) != 1 {
		t.Errorf("a silence per service: %v %v", e.am.Created, m.Silences)
	}
	p := e.page("public", "")
	if len(p.Maintenance) != 1 || p.Maintenance[0].Status != "scheduled" || p.Maintenance[0].Title != "Database upgrade" ||
		!slices.Equal(p.Maintenance[0].Components, []string{"api"}) {
		t.Fatalf("shown ahead of time: %+v", p.Maintenance)
	}
	if component(t, p, "api").Status != Operational {
		t.Error("not yet in maintenance")
	}
	// during the window the component is in maintenance, even when the work breaks it
	e.now = e.now.Add(90 * time.Minute)
	e.state["api.example.com"] = between(e.now.Add(-10*time.Minute), e.now, minDown)
	p = e.page("public", "")
	if c := component(t, p, "api"); c.Status != Maintenance || c.History[Days-1].Status != Maintenance || c.History[Days-1].DownMinutes != 0 {
		t.Errorf("during maintenance: %s %+v", c.Status, c.History[Days-1])
	}
	if p.Status != Maintenance || p.Summary != "Maintenance in progress" || p.Maintenance[0].Status != "active" {
		t.Errorf("overall: %s %q %+v", p.Status, p.Summary, p.Maintenance)
	}
	// ended early: its silences end, and the window is completed
	if _, err := e.b.EndMaintenance(ctx, m.ID, who); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.am.Expired, m.Silences) {
		t.Errorf("silences ended: %v", e.am.Expired)
	}
	delete(e.state, "api.example.com")
	p = e.page("public", "")
	if len(p.Maintenance) != 1 || p.Maintenance[0].Status != "completed" || !p.Maintenance[0].Ends.Equal(e.now) {
		t.Errorf("ended: %+v", p.Maintenance)
	}
	if _, err := e.b.EndMaintenance(ctx, m.ID, who); !errors.Is(err, bridge.ErrMaintenanceEnded) {
		t.Errorf("ending it twice: %v", err)
	}

	// a silence made another way whose comment begins with maintenance: is a window too
	err = e.st.PutSilence(store.SilenceRecord{ID: "s1", Matchers: []store.Matcher{{Name: "service", Value: "docker.example.com|other", IsRegex: true, IsEqual: true}},
		StartsAt: e.now.Add(-time.Minute), EndsAt: e.now.Add(time.Hour), CreatedBy: "alex", Comment: "Maintenance: moving the mirror's DNS"})
	if err != nil {
		t.Fatal(err)
	}
	p = e.page("public", "")
	if c := component(t, p, "mirrors"); c.Status != Maintenance {
		t.Errorf("a maintenance silence: %s", c.Status)
	}
	if w := p.Maintenance[0]; w.ID != "s-s1" || w.Title != "moving the mirror's DNS" || w.Status != "active" {
		t.Errorf("the silence as a window: %+v", w)
	}
	// called off before it began: not on the pages
	later, err := e.b.ScheduleMaintenance(ctx, who, bridge.MaintenanceInput{Services: []string{"ghcr.example.com"}, StartsAt: e.now.Add(24 * time.Hour),
		EndsAt: e.now.Add(25 * time.Hour), Title: "Later"})
	if err != nil {
		t.Fatal(err)
	}
	if later, err = e.b.EndMaintenance(ctx, later.ID, who); err != nil || !later.Cancelled {
		t.Errorf("called off: %+v %v", later, err)
	}
	for _, w := range e.page("public", "").Maintenance {
		if w.ID == later.ID {
			t.Error("a window called off is not shown")
		}
	}
}

func TestMaintenanceChecks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	who := bridge.Actor{Name: "claude-oncall", Via: "api"}
	for name, in := range map[string]bridge.MaintenanceInput{
		"no title":       {Services: []string{"a"}, EndsAt: e.now.Add(time.Hour)},
		"no services":    {Title: "x", EndsAt: e.now.Add(time.Hour)},
		"unsafe service": {Title: "x", Services: []string{`a"} or vector(1) #`}, EndsAt: e.now.Add(time.Hour)},
		"no end":         {Title: "x", Services: []string{"a"}},
		"ended":          {Title: "x", Services: []string{"a"}, StartsAt: e.now.Add(-2 * time.Hour), EndsAt: e.now.Add(-time.Hour)},
		"too long":       {Title: "x", Services: []string{"a"}, EndsAt: e.now.Add(8 * 24 * time.Hour)},
		"too far ahead":  {Title: "x", Services: []string{"a"}, StartsAt: e.now.Add(100 * 24 * time.Hour), EndsAt: e.now.Add(101 * 24 * time.Hour)},
	} {
		if _, err := e.b.ScheduleMaintenance(ctx, who, in); !errors.Is(err, bridge.ErrBadInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a start in the past is now: a window cannot cover what already happened
	m, err := e.b.ScheduleMaintenance(ctx, who, bridge.MaintenanceInput{Title: "x", Services: []string{"a", "a", "b"}, StartsAt: e.now.Add(-time.Hour), EndsAt: e.now.Add(time.Hour)})
	if err != nil || !m.StartsAt.Equal(e.now) || !slices.Equal(m.Services, []string{"a", "b"}) {
		t.Errorf("from the past: %+v %v", m, err)
	}
	if _, err := e.b.EndMaintenance(ctx, "m-nope", who); !errors.Is(err, bridge.ErrNoMaintenance) {
		t.Errorf("unknown: %v", err)
	}
	if got := e.st.AuditLog(10); len(got) == 0 || got[0].Action != "maintenance" || got[0].Actor != "claude-oncall" {
		t.Errorf("audited: %+v", got)
	}
}

// Run keeps the history while pages are computed beside it (for the race detector).
func TestRunBesidePages(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.s.Run(ctx); close(done) }()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 5 {
				if _, err := e.s.Page(context.Background(), "public", "zh"); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	cancel()
	<-done
}
