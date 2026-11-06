package web

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/status"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/vm"
)

const statusCatalogue = `locations:
  - {id: edge-1, name: United States - Los Angeles, region: overseas, network: CN2 GIA}
  - {id: edge-2, name: Germany - Frankfurt, region: overseas}
status_pages:
  - name: public
    title: {en: Example Status, zh: Example Status ZH}
    description: Whether Example's public services work.
    host: uptime.example.com
    default: true
    brand: linkos-status
    languages: [en, zh]
    components:
      - {name: mirrors, title: Mirrors, description: "Pull through the mirror <script>alert(1)</script>", page: mirrors}
      - {name: api, title: "API <img src=x onerror=alert(1)>", services: [api.example.com]}
    groups:
      - {title: Registries, components: [mirrors]}
    links: {home: "https://example.com/", privacy: "https://example.com/privacy", abuse: "mailto:abuse@example.com"}
    menu:
      title: Example
      groups:
        - links: [{title: Example home, url: "https://example.com/"}]
        - title: Free public services
          links: [{title: Example Mirrors, url: "https://mirrors.example.com/"}]
`

const statusTargets = `- targets: [https://docker.example.com/v2/]
  labels: {module: http_head, service: docker.example.com, page: mirrors}
- targets: [https://api.example.com/healthz]
  labels: {module: http_head, service: api.example.com}
`

// probeTarget is the target of each service of statusTargets.
var probeTarget = map[string]string{"docker.example.com": "https://docker.example.com/v2/", "api.example.com": "https://api.example.com/healthz"}

// fakeVictoria answers /api/v1/query_range as VictoriaMetrics would the pages' agreement query: every
// service it names operational at every minute, except api.example.com, down in the last ten minutes.
func fakeVictoria(t *testing.T, now func() time.Time) *httptest.Server {
	t.Helper()
	re := regexp.MustCompile(`service=~?"([^"]*)"`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/query" { // the instant queries of the probes' details: nothing more to say
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{}}})
			return
		}
		if r.URL.Path != "/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		start, _ := strconv.ParseFloat(q.Get("start"), 64)
		end, _ := strconv.ParseFloat(q.Get("end"), 64)
		step, _ := strconv.ParseFloat(q.Get("step"), 64)
		m := re.FindStringSubmatch(q.Get("query"))
		var result []map[string]any
		if strings.HasPrefix(q.Get("query"), "probe_success{") { // each location's probes, as the console reads them
			for _, svc := range strings.Split(m[1], "|") {
				svc = strings.ReplaceAll(svc, `\\`, "")
				for _, loc := range []string{"edge-1", "edge-2"} {
					var values [][2]any
					for ts := start; ts <= end; ts += step {
						v := "1"
						if svc == "api.example.com" && time.Unix(int64(ts), 0).After(now().Add(-10*time.Minute)) {
							v = "0"
						}
						values = append(values, [2]any{ts, v})
					}
					result = append(result, map[string]any{"metric": map[string]string{"service": svc, "location": loc, "instance": probeTarget[svc],
						"module": "http_head", "job": "probe"}, "values": values})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": result}})
			return
		}
		for _, svc := range strings.Split(m[1], "|") {
			svc = strings.ReplaceAll(svc, `\\`, "")
			var values [][2]any
			for ts := start; ts <= end; ts += step {
				v := "1"
				if svc == "api.example.com" && time.Unix(int64(ts), 0).After(now().Add(-10*time.Minute)) {
					v = "3"
				}
				values = append(values, [2]any{ts, v})
			}
			result = append(result, map[string]any{"metric": map[string]string{"service": svc}, "values": values})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": result}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

type statusEnv struct {
	t        *testing.T
	now      time.Time
	srv      *Server
	h        http.Handler
	b        *bridge.Bridge
	incident string
	st       *store.Store
}

func newStatusEnv(t *testing.T) *statusEnv {
	t.Helper()
	return newStatusEnvWith(t, statusCatalogue)
}

// newStatusEnvWith is newStatusEnv on another catalogue.
func newStatusEnvWith(t *testing.T, catalogue string) *statusEnv {
	t.Helper()
	e := &statusEnv{t: t, now: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)}
	now := func() time.Time { return e.now }
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e.st = st
	cdb := configdb.Open(configdb.Deps{Store: st, Lang: i18n.EN, Now: now})
	if _, err := cdb.Import(context.Background(), configdb.Actor{Name: "test", Via: "cli"},
		configdb.Files{"catalog/catalog.yaml": catalogue, "targets/all-web.yaml": statusTargets}, "", false); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Language: "en", Location: time.UTC, PublicURL: publicURL, AckTTL: config.Duration(4 * time.Hour),
		Feishu: config.Feishu{MonthlyBudget: 1000}, OnCallByID: map[string]config.OnCaller{},
		Escalation: map[string]config.Policy{"critical": {Channel: "webhook"}}}
	vmSrv := fakeVictoria(t, now)
	e.b = bridge.New(bridge.Deps{Config: cfg, Store: st, AM: &fakeAM{}, Bot: &fakeBot{}, VM: vm.New(vmSrv.URL, nil), Now: now})
	key := `{}:{alertname="ServiceDown",service="api.example.com"}`
	err = e.b.HandleWebhook(context.Background(), alertmanager.Webhook{Version: "4", GroupKey: key, Status: "firing",
		GroupLabels:  map[string]string{"alertname": "ServiceDown", "service": "api.example.com"},
		CommonLabels: map[string]string{"alertname": "ServiceDown", "service": "api.example.com", "severity": "critical"},
		Alerts:       []alertmanager.Alert{{Status: "firing", Fingerprint: "f1", StartsAt: e.now.Add(-8 * time.Minute)}}})
	if err != nil {
		t.Fatal(err)
	}
	e.incident = bridge.GroupID(key)
	if _, err := e.b.AddNote(context.Background(), e.incident, bridge.Actor{Name: "claude-oncall", Via: "mcp"}, "public: Fixing it "+hostile); err != nil {
		t.Fatal(err)
	}
	pages := status.New(status.Deps{Config: cdb, Bridge: e.b, Store: st, VM: vm.New(vmSrv.URL, nil), PublicURL: publicURL, Location: time.UTC, Now: now})
	e.srv = &Server{Bridge: e.b, Store: st, Config: cdb, Lang: i18n.EN, Location: time.UTC, Now: now, Version: "test", Status: pages,
		Auth: nil}
	mux := http.NewServeMux()
	e.srv.Register(mux)
	mux.HandleFunc("GET /api/v1/bridge", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("the API")) })
	e.h = e.srv.Hosts(mux)
	return e
}

type statusAnswer struct {
	code   int
	header http.Header
	body   string
}

func (e *statusEnv) do(method, host, path string, headers map[string]string) statusAnswer {
	e.t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Host = host
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	b, _ := io.ReadAll(w.Result().Body)
	return statusAnswer{code: w.Code, header: w.Result().Header, body: string(b)}
}

func (e *statusEnv) get(path string) statusAnswer {
	return e.do(http.MethodGet, "beacon.example.com", path, nil)
}

func noInlineScripts(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, "<script") || strings.Contains(body, " style=") || strings.Contains(body, "javascript:") {
		t.Errorf("the page has script or inline style")
	}
}

func TestStatusPageRenders(t *testing.T) {
	e := newStatusEnv(t)
	a := e.get("/status/public")
	if a.code != http.StatusOK {
		t.Fatalf("page: %d %s", a.code, a.body)
	}
	for _, want := range []string{
		`<html lang="en">`, `data-brand="linkos-status"`, `class="ol-site-header"`, `class="ol-hero bc-status-hero"`, `class="ol-site-footer"`,
		`<meta name="viewport" content="width=device-width, initial-scale=1">`, `href="/static/status.css?v=test"`,
		"Example Status", "API &lt;img src=x onerror=alert(1)&gt; is down", "Outage: API &lt;img",
		"Fixing it &lt;script&gt;alert(1)&lt;/script&gt;", "Pull through the mirror &lt;script&gt;",
		`class="ol-uptime ol-uptime-lg bc-uptime" role="img"`, `class="ol-uptime-down"`, `class="ol-uptime-ok"`,
		`popovertarget="family"`, "Free public services", `href="https://mirrors.example.com/"`, `href="mailto:abuse@example.com"`,
		`hreflang="zh-CN" href="/status/public?lang=zh"`, `rel="canonical" href="https://uptime.example.com/"`,
		`<link rel="alternate" type="application/atom+xml"`, "Registries", "Germany - Frankfurt",
		`href="/status/public/incidents/` + e.incident + `"`, `<symbol id="i-languages"`,
	} {
		if !strings.Contains(a.body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	if strings.Contains(a.body, hostile) || strings.Contains(a.body, "<img src=x") {
		t.Error("hostile text is not escaped")
	}
	noInlineScripts(t, a.body)
	if n := strings.Count(a.body, `<span class="ol-uptime-`) + strings.Count(a.body, `<span title="`); n != 2*status.Days {
		t.Errorf("%d cells for two components, want %d", n, 2*status.Days)
	}
	// public, cacheable, indexed, under the console's CSP without its script
	h := a.header
	if h.Get("Content-Security-Policy") != statusCSP || strings.Contains(statusCSP, "script-src") || h.Get("X-Robots-Tag") != "" ||
		h.Get("Cache-Control") != "public, max-age=30" || h.Get("ETag") == "" || h.Get("Referrer-Policy") != "no-referrer" ||
		h.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("headers: %v", h)
	}
	if again := e.do(http.MethodGet, "beacon.example.com", "/status/public", map[string]string{"If-None-Match": h.Get("ETag")}); again.code != http.StatusNotModified || again.body != "" {
		t.Errorf("If-None-Match: %d", again.code)
	}
	if def := e.get("/status"); def.code != http.StatusOK || !strings.Contains(def.body, "Example Status") {
		t.Errorf("the default page at /status: %d", def.code)
	}
	if nope := e.get("/status/nope"); nope.code != http.StatusNotFound {
		t.Errorf("an unknown page: %d", nope.code)
	}

	zh := e.get("/status/public?lang=zh")
	for _, want := range []string{`<html lang="zh-CN">`, "Example Status ZH", i18n.T(i18n.ZH, "Components"), i18n.T(i18n.ZH, "Active incidents"),
		`aria-current="true">` + i18n.ZH.Name(), `href="/status/public/incidents/` + e.incident + `?lang=zh"`} {
		if !strings.Contains(zh.body, escaped(want)) && !strings.Contains(zh.body, want) {
			t.Errorf("the Chinese page lacks %s", want)
		}
	}
	if zh.header.Get("ETag") == h.Get("ETag") {
		t.Error("each language has its own entity tag")
	}
}

func escaped(s string) string { return strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(s) }

func TestStatusIncidentPage(t *testing.T) {
	e := newStatusEnv(t)
	a := e.get("/status/public/incidents/" + e.incident)
	if a.code != http.StatusOK {
		t.Fatalf("incident: %d %s", a.code, a.body)
	}
	for _, want := range []string{"Outage: API &lt;img", "Fixing it &lt;script&gt;", `<symbol id="i-bot"`, "Agent", "Detected", "Ongoing", `href="/status/public"`} {
		if !strings.Contains(a.body, want) {
			t.Errorf("the incident's page lacks %s", want)
		}
	}
	noInlineScripts(t, a.body)
	if nope := e.get("/status/public/incidents/abc"); nope.code != http.StatusNotFound {
		t.Errorf("an unknown incident: %d", nope.code)
	}
}

func TestStatusJSONFeedBadges(t *testing.T) {
	e := newStatusEnv(t)
	a := e.get("/status/public.json")
	if a.code != http.StatusOK || a.header.Get("Content-Type") != "application/json; charset=utf-8" || a.header.Get("Access-Control-Allow-Origin") != "*" ||
		a.header.Get("Cache-Control") != "public, max-age=30" || a.header.Get("Cross-Origin-Resource-Policy") != "cross-origin" {
		t.Fatalf("json: %d %v", a.code, a.header)
	}
	var p struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Components []struct {
			Name    string            `json:"name"`
			Status  string            `json:"status"`
			History []json.RawMessage `json:"history"`
		} `json:"components"`
		Incidents []struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Updates []struct {
				Kind, Text, By string
			} `json:"updates"`
		} `json:"incidents"`
		Maintenance []json.RawMessage `json:"maintenance"`
	}
	if err := json.Unmarshal([]byte(a.body), &p); err != nil {
		t.Fatal(err)
	}
	if p.Name != "public" || p.Status != status.Down || len(p.Components) != 2 || len(p.Components[1].History) != status.Days ||
		len(p.Incidents) != 1 || p.Incidents[0].Updates[0].By != "agent" || p.Maintenance == nil {
		t.Errorf("json: %+v", p)
	}
	if strings.Contains(a.body, "claude-oncall") || strings.Contains(a.body, "api.example.com") {
		t.Error("the public JSON names no token and no service label")
	}
	if def := e.get("/status.json"); def.code != http.StatusOK || def.body != a.body {
		t.Errorf("/status.json is the default page's: %d", def.code)
	}

	f := e.get("/status/public/feed.atom")
	if f.code != http.StatusOK || f.header.Get("Content-Type") != "application/atom+xml; charset=utf-8" {
		t.Fatalf("feed: %d %v", f.code, f.header)
	}
	var feed struct {
		XMLName xml.Name `xml:"feed"`
		Lang    string   `xml:"lang,attr"`
		ID      string   `xml:"id"`
		Entries []struct {
			ID      string `xml:"id"`
			Title   string `xml:"title"`
			Content string `xml:"content"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal([]byte(f.body), &feed); err != nil {
		t.Fatal(err)
	}
	if feed.XMLName.Space != "http://www.w3.org/2005/Atom" || feed.Lang != "en" || feed.ID != "https://uptime.example.com/" || len(feed.Entries) != 1 ||
		feed.Entries[0].ID != "https://uptime.example.com/incidents/"+e.incident || !strings.Contains(feed.Entries[0].Content, "Fixing it "+hostile) {
		t.Errorf("feed: %+v", feed)
	}
	if strings.Contains(f.body, hostile) {
		t.Error("the feed escapes what it carries")
	}

	b := e.get("/status/public/badge.svg")
	if b.code != http.StatusOK || b.header.Get("Content-Type") != "image/svg+xml; charset=utf-8" || !strings.Contains(b.body, ">Outage</text>") ||
		!strings.Contains(b.body, `fill="#dc2626"`) || b.header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("badge: %d %s", b.code, b.body)
	}
	c := e.get("/status/public/components/api/badge.svg")
	if c.code != http.StatusOK || strings.Contains(c.body, "<img") || !strings.Contains(c.body, "API &lt;img") {
		t.Errorf("a component's badge escapes its title: %s", c.body)
	}
	if ok := e.get("/status/public/components/mirrors/badge.svg"); !strings.Contains(ok.body, ">Operational</text>") || !strings.Contains(ok.body, `fill="#15803d"`) {
		t.Errorf("an operational badge: %s", ok.body)
	}
	if nope := e.get("/status/public/components/nope/badge.svg"); nope.code != http.StatusNotFound {
		t.Errorf("an unknown component: %d", nope.code)
	}
}

func TestStatusOwnHost(t *testing.T) {
	e := newStatusEnv(t)
	host := "uptime.example.com"
	for path, want := range map[string]int{
		"/": 200, "/?lang=zh": 200, "/incidents/" + e.incident: 200, "/status.json": 200, "/feed.atom": 200, "/badge.svg": 200,
		"/components/api/badge.svg": 200, "/static/ol/ui.css": 200, "/static/status.css": 200, "/robots.txt": 200,
		"/status/public": 404, "/sign-in": 404, "/api/v1/bridge": 404, "/alerts/x": 404, "/tokens": 404, "/incidents/nope": 404,
		"/components/a/b/badge.svg": 404, "/mcp": 404,
	} {
		if a := e.do(http.MethodGet, host, path, nil); a.code != want {
			t.Errorf("%s%s: %d, want %d", host, path, a.code, want)
		}
	}
	a := e.do(http.MethodGet, host+":443", "/", nil)
	if a.code != http.StatusOK || !strings.Contains(a.body, `href="/incidents/`+e.incident+`"`) || !strings.Contains(a.body, `<link rel="alternate" type="application/json" href="/status.json">`) {
		t.Errorf("the page at / on its host links within it: %d", a.code)
	}
	if p := e.do(http.MethodPost, host, "/", nil); p.code != http.StatusMethodNotAllowed {
		t.Errorf("a post: %d", p.code)
	}
	if r := e.do(http.MethodGet, host, "/robots.txt", nil); r.body != "User-agent: *\nAllow: /\n" {
		t.Errorf("robots on the page's host: %q", r.body)
	}
	// on Beacon's own host the rest is Beacon's, and the page is under /status
	if a := e.get("/api/v1/bridge"); a.body != "the API" {
		t.Errorf("Beacon's host: %q", a.body)
	}
}

func TestStatusRobots(t *testing.T) {
	e := newStatusEnv(t)
	r := e.get("/robots.txt")
	// the public pages are read, the console, the API, MCP and the old paths of signed links are not
	want := "User-agent: *\nAllow: /\nAllow: /status\nAllow: /static/\n" +
		"Disallow: /console\nDisallow: /api/\nDisallow: /mcp\nDisallow: /alerts/\nDisallow: /sessions/\n"
	if r.code != http.StatusOK || r.body != want {
		t.Errorf("robots: %q", r.body)
	}
	// the pages people open from the chat stay out of search engines, at their old paths too
	if a := e.get("/console/alerts/" + e.incident); a.header.Get("X-Robots-Tag") == "" {
		t.Error("the alert's page lost its X-Robots-Tag")
	}
	if a := e.get("/alerts/" + e.incident); a.header.Get("X-Robots-Tag") == "" {
		t.Error("the alert's old path lost its X-Robots-Tag")
	}
	for _, path := range []string{"/status/public", "/"} {
		if a := e.get(path); a.header.Get("X-Robots-Tag") != "" {
			t.Errorf("%s is marked noindex", path)
		}
	}
	// without status pages, / is public all the same
	empty := &Server{}
	w := httptest.NewRecorder()
	empty.robots(w, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))
	if w.Body.String() != want {
		t.Errorf("without status pages: %q", w.Body.String())
	}
}

// A page's address without ?lang= is in the reader's language, when the page has it: people who follow
// a link from a site in English read it in English, though the page's first language is another. The
// language a link names wins; programs (the JSON, the feed, the badges) get the page's first.
func TestStatusPageFollowsTheBrowser(t *testing.T) {
	e := newStatusEnv(t)
	zh := map[string]string{"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8"}
	for _, c := range []struct{ host, path, accept, lang string }{
		{"beacon.example.com", "/status/public", "zh-CN,zh;q=0.9,en;q=0.8", "zh-CN"},
		{"uptime.example.com", "/", "fr-FR, de;q=0.9, zh;q=0.5", "zh-CN"}, // the first the page has
		{"uptime.example.com", "/", "fr-FR", "en"},                        // none: the page's first
		{"uptime.example.com", "/", "", "en"},
		{"uptime.example.com", "/incidents/" + e.incident, "zh", "zh-CN"},
		{"uptime.example.com", "/?lang=en", "zh", "en"}, // the link's language wins
	} {
		a := e.do(http.MethodGet, c.host, c.path, map[string]string{"Accept-Language": c.accept})
		if a.code != http.StatusOK || !strings.Contains(a.body, `<html lang="`+c.lang+`">`) {
			t.Errorf("%s%s for %q: %d, not in %s", c.host, c.path, c.accept, a.code, c.lang)
		}
		if a.header.Get("Vary") != "Accept-Language" {
			t.Errorf("%s%s: Vary %q", c.host, c.path, a.header.Get("Vary"))
		}
	}

	// read in Chinese without asking: the links stay in Chinese without naming it, the menu names each
	// language (the address without one would follow the browser again), and search engines see each
	// language's own address
	a := e.do(http.MethodGet, "uptime.example.com", "/", zh)
	for _, want := range []string{
		`href="/incidents/` + e.incident + `"`, `href="/?lang=en" lang="en"`, `href="/?lang=zh" lang="zh-CN"`,
		`rel="canonical" href="https://uptime.example.com/?lang=zh"`,
		`<link rel="alternate" hreflang="en" href="/">`, `<link rel="alternate" hreflang="zh-CN" href="/?lang=zh">`,
		`<link rel="alternate" hreflang="x-default" href="/">`,
	} {
		if !strings.Contains(a.body, want) {
			t.Errorf("read in Chinese, the page lacks %s", want)
		}
	}
	// asked for English with a Chinese browser: the links keep to English
	if a := e.do(http.MethodGet, "uptime.example.com", "/?lang=en", zh); !strings.Contains(a.body, `href="/incidents/`+e.incident+`?lang=en"`) ||
		!strings.Contains(a.body, `rel="canonical" href="https://uptime.example.com/"`) {
		t.Error("in English by its link, the page's links do not keep to English")
	}

	for _, path := range []string{"/status.json", "/feed.atom", "/badge.svg"} {
		a := e.do(http.MethodGet, "uptime.example.com", path, zh)
		if a.code != http.StatusOK || a.header.Get("Vary") != "" {
			t.Errorf("%s: %d, Vary %q", path, a.code, a.header.Get("Vary"))
		}
		if path == "/status.json" && !strings.Contains(a.body, `"language": "en"`) {
			t.Errorf("the JSON follows the browser: %.200s", a.body)
		}
	}
}

func TestStatusShellsDifferOnlyByCSS(t *testing.T) {
	e := newStatusEnv(t)
	var first string
	for i, width := range []string{"360", "860", "1440"} {
		a := e.do(http.MethodGet, "beacon.example.com", "/status/public", map[string]string{"Sec-CH-Viewport-Width": width, "Viewport-Width": width})
		if v := a.header.Get("Vary"); strings.Contains(strings.ToLower(v), "width") || strings.Contains(v, "*") {
			t.Errorf("varies by %q", v)
		}
		if i == 0 {
			first = a.body
		} else if a.body != first {
			t.Errorf("the page differs at %spx", width)
		}
	}
	// at 360px the strip shows its last 30 days, which fit; the site layout stacks by the design system's CSS
	css, err := static.ReadFile("static/status.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "@media (max-width: 640px)") || !strings.Contains(string(css), ".bc-uptime > :nth-child(-n+60) { display: none; }") {
		t.Error("the strip has no narrow-screen rule")
	}
	ui, _ := static.ReadFile("static/ol/ui.css")
	if !strings.Contains(string(ui), "@media (max-width: 860px)") || !strings.Contains(string(ui), ".ol-site-header {") {
		t.Error("the design system's site layout is missing")
	}
}
