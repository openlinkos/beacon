package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/router"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

type seen struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func (s *seen) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, r)
		s.body = append(s.body, string(b))
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *seen) last() (*http.Request, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reqs) == 0 {
		return nil, ""
	}
	return s.reqs[len(s.reqs)-1], s.body[len(s.body)-1]
}

type env struct {
	h       http.Handler
	st      *store.Store
	vm      *seen
	posted  []router.Postable
	m       *metrics.Metrics
	probe   string // cdn-pre-us1
	expired string // cdn-pre-cn1, whose machine expired
	host    string
	beat    string
	reader  string
	flux    string
	human   string
}

func newEnv(t *testing.T, withAM bool) *env {
	t.Helper()
	d := t.TempDir()
	cat := filepath.Join(d, "catalog")
	tg := filepath.Join(d, "targets")
	for _, p := range []string{cat, tg} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(cat, "c.yaml"): `
locations:
  - {id: cdn-pre-us1, name: United States - Los Angeles CN2 GIA, region: overseas, network: CN2 GIA, probe: {timeout: 45s, retries: 1}}
  - {id: cdn-pre-cn1, name: Mainland China - Zhejiang Telecom, region: mainland, network: Telecom}
resources:
  - {name: cdn-pre-us1, kind: vps, expires: 2027-04-02}
  - {name: cdn-pre-cn1, kind: vps, expires: 2026-11-02}
`,
		filepath.Join(tg, "all-web.yaml"):          "- targets: [https://linkos.org/]\n  labels: {module: http_head, service: linkos.org}\n",
		filepath.Join(tg, "mainland-only.yaml"):    "- targets: [https://cn.example/]\n  labels: {module: http_head, service: cn}\n",
		filepath.Join(tg, "cdn-pre-us1-node.yaml"): "- targets: ['203.0.113.1:443']\n  labels: {module: tcp_443, service: node}\n",
		filepath.Join(tg, "overseas-nodes.yaml"):   "- targets: ['203.0.113.2:443']\n  labels: {module: tcp_443, service: node2}\n",
	}
	for p, b := range files {
		if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := catalog.Load(cat)
	if err != nil {
		t.Fatal(err)
	}
	set, err := targets.Load(tg, []string{"cdn-pre-us1", "cdn-pre-cn1"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(d, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{st: st, vm: &seen{}, m: metrics.New("test")}
	vmURL, _ := url.Parse(e.vm.server(t).URL + "/prefix")
	var alerts func([]router.Postable) error
	if withAM {
		alerts = func(ps []router.Postable) error { e.posted = append(e.posted, ps...); return nil }
	}
	mk := func(name, subject string, scopes ...string) string {
		s, _, err := st.CreateSubjectToken(name, subject, scopes, time.Hour, "test")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	e.probe = mk("probe-cdn-pre-us1", "cdn-pre-us1", "probe:write")
	e.expired = mk("probe-cdn-pre-cn1", "cdn-pre-cn1", "probe:write")
	e.host = mk("host-worker-us", "worker-us", "host:write")
	e.beat = mk("heartbeat-nas", "nas", "heartbeat:write")
	e.reader = mk("mcp-vm", "", "metrics:read")
	e.flux = mk("flux", "", "alerts:send")
	e.human = mk("claude-code", "", "status:read", "alerts:write")
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC) // cdn-pre-cn1 expired on 11-02
	e.h = Handler(Deps{Auth: &api.Auth{Store: st}, Catalog: func() *catalog.Catalog { return c },
		Targets: func() *targets.Set { return set }, Modules: func() []byte { return []byte("modules: {}\n") }, VM: vmURL, Alerts: alerts, RulesToken: "rules-0123456789abcdef0123456789",
		Metrics: e.m, Now: func() time.Time { return now }})
	return e
}

func (e *env) do(method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Cookie", "a=b")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func code(t *testing.T, rec *httptest.ResponseRecorder, want int, errCode string) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
	if errCode != "" && !strings.Contains(rec.Body.String(), `"code":"`+errCode+`"`) {
		t.Fatalf("body %s lacks code %s", rec.Body, errCode)
	}
}

func TestProbeWriteIsLabelledByTheGateway(t *testing.T) {
	e := newEnv(t, false)
	// the probe tries to pass its own extra_label: the gateway drops the client's query entirely
	rec := e.do("POST", "/api/v1/write?extra_label=location=evil", e.probe, "snappy-bytes")
	code(t, rec, http.StatusNoContent, "")
	r, body := e.vm.last()
	if r.URL.Path != "/prefix/api/v1/write" || body != "snappy-bytes" {
		t.Fatalf("forwarded %s %q", r.URL.Path, body)
	}
	got := r.URL.Query()["extra_label"]
	want := []string{"location=cdn-pre-us1", "region=overseas", "network=CN2 GIA", "host=cdn-pre-us1"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("extra_label = %v", got)
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
		t.Fatal("the token and cookies must not reach VictoriaMetrics")
	}
	if !strings.Contains(scrape(e), `linkos_gateway_last_write_timestamp_seconds{subject="cdn-pre-us1"}`) ||
		!strings.Contains(scrape(e), `linkos_gateway_requests_total{route="write",code="204"} 1`) {
		t.Fatalf("metrics:\n%s", scrape(e))
	}
}

func scrape(e *env) string {
	rec := httptest.NewRecorder()
	e.m.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func TestExpiredMachineCannotWrite(t *testing.T) {
	e := newEnv(t, false)
	code(t, e.do("POST", "/api/v1/write", e.expired, "x"), http.StatusForbidden, "location.inactive")
	code(t, e.do("GET", "/api/v1/targets", e.expired, ""), http.StatusForbidden, "location.inactive")
	if r, _ := e.vm.last(); r != nil {
		t.Fatal("nothing may reach VictoriaMetrics")
	}
}

func TestHostWrite(t *testing.T) {
	e := newEnv(t, false)
	code(t, e.do("POST", "/api/v1/write", e.host, "x"), http.StatusNoContent, "")
	r, _ := e.vm.last()
	if got := r.URL.Query()["extra_label"]; len(got) != 1 || got[0] != "host=worker-us" {
		t.Fatalf("extra_label = %v", got)
	}
	code(t, e.do("GET", "/api/v1/targets", e.host, ""), http.StatusForbidden, "auth.scope")
}

func TestAgentSettings(t *testing.T) {
	e := newEnv(t, false)
	rec := e.do("GET", "/api/v1/agent", e.probe, "")
	code(t, rec, http.StatusOK, "")
	var s targets.Settings
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil || s.Location != "cdn-pre-us1" || s.ProbeTimeoutSeconds != 45 || s.ProbeRetries != 1 {
		t.Fatalf("settings %+v %v", s, err)
	}
	code(t, e.do("GET", "/api/v1/agent", e.expired, ""), http.StatusForbidden, "location.inactive")
	code(t, e.do("GET", "/api/v1/agent", e.host, ""), http.StatusForbidden, "auth.scope")
}

func TestTargetsForTheLocation(t *testing.T) {
	e := newEnv(t, false)
	rec := e.do("GET", "/api/v1/targets", e.probe, "")
	code(t, rec, http.StatusOK, "")
	var groups []targets.Group
	if err := json.Unmarshal(rec.Body.Bytes(), &groups); err != nil {
		t.Fatal(err)
	}
	var svc []string
	for _, g := range groups {
		svc = append(svc, g.Labels["service"])
	}
	// all-web, cdn-pre-us1-node and overseas-nodes; not mainland-only
	if strings.Join(svc, ",") != "linkos.org,node,node2" {
		t.Fatalf("services %v", svc)
	}
}

func TestHeartbeatReadAndAlerts(t *testing.T) {
	e := newEnv(t, false)
	code(t, e.do("POST", "/api/v1/import/prometheus", e.beat, "linkos_heartbeat_last_success_timestamp_seconds{name=\"x\"} 1"), http.StatusNoContent, "")
	r, _ := e.vm.last()
	if r.URL.Path != "/prefix/api/v1/import/prometheus" || strings.Join(r.URL.Query()["extra_label"], ",") != "source=heartbeat,sender=nas" {
		t.Fatalf("heartbeat forwarded as %s?%s", r.URL.Path, r.URL.RawQuery)
	}
	code(t, e.do("GET", "/api/v1/query?query=up", e.reader, ""), http.StatusNoContent, "")
	if r, _ := e.vm.last(); r.URL.Path != "/prefix/api/v1/query" || r.URL.Query().Get("query") != "up" {
		t.Fatalf("query forwarded as %s?%s", r.URL.Path, r.URL.RawQuery)
	}
	code(t, e.do("GET", "/api/v1/label/job/values", e.reader, ""), http.StatusNoContent, "")
	// a reader cannot write, a writer cannot read
	code(t, e.do("POST", "/api/v1/write", e.reader, "x"), http.StatusForbidden, "auth.scope")
	code(t, e.do("GET", "/api/v1/query?query=up", e.probe, ""), http.StatusForbidden, "auth.scope")
	// nothing takes alerts here
	code(t, e.do("POST", "/api/v2/alerts", e.flux, "[]"), http.StatusServiceUnavailable, "upstream")
}

func TestAlertsToTheRouter(t *testing.T) {
	e := newEnv(t, true)
	body := `[{"labels":{"alertname":"ServiceDown","service":"git"},"startsAt":"2026-11-03T00:00:00Z"}]`
	code(t, e.do("POST", "/api/v2/alerts", e.flux, body), http.StatusOK, "")
	code(t, e.do("POST", "/api/v2/alerts", "rules-0123456789abcdef0123456789", body), http.StatusOK, "")
	if len(e.posted) != 2 || e.posted[0].Labels["service"] != "git" {
		t.Fatalf("posted %+v", e.posted)
	}
	// the rule engine's posts are counted too: a deployment checks there that its alerts arrive
	if !strings.Contains(scrape(e), `linkos_gateway_requests_total{route="alerts",code="200"} 2`) {
		t.Errorf("alert posts not counted:\n%s", scrape(e))
	}
	code(t, e.do("POST", "/api/v2/alerts", "rules-wrong", body), http.StatusUnauthorized, "auth.invalid")
	code(t, e.do("POST", "/api/v2/alerts", e.probe, body), http.StatusForbidden, "auth.scope")
	code(t, e.do("POST", "/api/v2/alerts", e.flux, "{not json"), http.StatusBadRequest, "bad_input")
}

func TestAuth(t *testing.T) {
	e := newEnv(t, false)
	code(t, e.do("POST", "/api/v1/write", "", "x"), http.StatusUnauthorized, "auth.required")
	code(t, e.do("POST", "/api/v1/write", "lbt_nope", "x"), http.StatusUnauthorized, "auth.invalid")
	code(t, e.do("POST", "/api/v1/write", e.human, "x"), http.StatusForbidden, "auth.scope")
	code(t, e.do("GET", "/api/v1/nothing", e.probe, ""), http.StatusNotFound, "not_found")
	code(t, e.do("DELETE", "/api/v1/admin/tsdb/delete_series", e.reader, ""), http.StatusNotFound, "not_found")
}

func TestProbeImportAndModules(t *testing.T) {
	e := newEnv(t, false)
	code(t, e.do("POST", "/api/v1/import/prometheus?extra_label=location=evil", e.probe, "probe_success 1 1793436000000"), http.StatusNoContent, "")
	r, body := e.vm.last()
	if r.URL.Path != "/prefix/api/v1/import/prometheus" || body != "probe_success 1 1793436000000" ||
		strings.Join(r.URL.Query()["extra_label"], "|") != "location=cdn-pre-us1|region=overseas|network=CN2 GIA|host=cdn-pre-us1" {
		t.Fatalf("imported as %s?%s %q", r.URL.Path, r.URL.RawQuery, body)
	}
	code(t, e.do("POST", "/api/v1/import/prometheus", e.expired, "x 1"), http.StatusForbidden, "location.inactive")
	rec := e.do("GET", "/api/v1/modules", e.probe, "")
	code(t, rec, http.StatusOK, "")
	if rec.Body.String() != "modules: {}\n" {
		t.Fatalf("modules %q", rec.Body)
	}
	code(t, e.do("GET", "/api/v1/modules", e.beat, ""), http.StatusForbidden, "auth.scope")
}
