package configdb_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/gateway"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

// files are a deployment's configuration as it was kept before the database, with what could trip a
// conversion: flow and block styles, comments, a label written as a number, file names whose order
// changes without .yaml (all-web-x.yaml sorts before all-web.yaml), a location prefix that is the
// start of another (cdn-pre and cdn-pre-us1), probe settings, an expired machine.
var files = map[string]string{
	"catalog/locations.yaml": `# probe locations
locations:
  - id: cdn-pre-us1
    name: United States - Los Angeles CN2 GIA
    region: overseas
    network: CN2 GIA
    probe: {timeout: 45s, retries: 1}   # a lossy path
  - {id: cdn-pre, name: United States - Seattle, region: overseas, network: "9929"}
  - {id: cdn-pre-cn1, name: Mainland China - Zhejiang Telecom, region: mainland, network: Telecom, probe: {retries: 2}}
  - {id: cdn-old-cn2, name: Mainland China - Jiangsu, region: mainland, network: Unicom}
`,
	"catalog/resources.yaml": `resources:
  - {name: cdn-pre-us1, kind: vps, expires: 2027-04-02}
  - {name: cdn-old-cn2, kind: vps, expires: 2026-11-01}
heartbeats:
  - {name: nas-backup, max_age: 26h, severity: warning}
`,
	"targets/all-web.yaml": `# public sites
- targets: [https://linkos.org/, "https://example.com/?a=1&b=2"]
  labels: {module: http_head, service: linkos.org, severity: critical}
- targets:
    - https://docs.example.com/
  labels:
    module: http_head
    service: docs
    port: 443
`,
	"targets/all-web-x.yaml":        "- targets: [https://x.example/]\n  labels: {module: http_head, service: x}\n",
	"targets/mainland-only.yaml":    "- targets: [https://cn.example/]\n  labels: {module: http_head, service: cn}\n",
	"targets/overseas-nodes.yaml":   "- targets: ['203.0.113.2:443']\n  labels: {module: tcp_443, service: node2}\n",
	"targets/cdn-pre-us1-node.yaml": "- targets: ['203.0.113.1:443']\n  labels: {module: tcp_443, service: node}\n",
	"targets/cdn-pre-x.yaml":        "- targets: ['203.0.113.9:443']\n  labels: {module: tcp_443, service: seattle}\n",
	"targets/modules.yml": `# modules, as the Blackbox Exporter has them
modules:
  http_head:
    prober: http
    timeout: 20s
    http:
      method: HEAD
      valid_status_codes: [200]   # nothing else
      follow_redirects: false
  tcp_443:
    prober: tcp
    tcp: {tls: true}
`,
}

type probeGateway struct {
	h      http.Handler
	tokens map[string]string
}

func (g probeGateway) get(t *testing.T, who, path string) (int, string, []byte) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer "+g.tokens[who])
	w := httptest.NewRecorder()
	g.h.ServeHTTP(w, r)
	return w.Code, w.Header().Get("Content-Type"), w.Body.Bytes()
}

var probes = []string{"cdn-pre-us1", "cdn-pre", "cdn-pre-cn1", "cdn-old-cn2", "cdn-gone"}

func gatewayFor(t *testing.T, st *store.Store, tokens map[string]string, cat func() *catalog.Catalog, set func() *targets.Set, mods func() []byte) probeGateway {
	vm, _ := url.Parse("http://127.0.0.1:1")
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) // cdn-old-cn2's machine expired yesterday
	return probeGateway{h: gateway.Handler(gateway.Deps{Auth: &api.Auth{Store: st}, Catalog: cat, Targets: set, Modules: mods, VM: vm,
		Now: func() time.Time { return now }}), tokens: tokens}
}

// TestProbeProtocolUnchanged builds the gateway twice from the same files, once from the files as the
// service always loaded them and once from the database after importing them: every probe must get
// the same answers, byte for byte.
func TestProbeProtocolUnchanged(t *testing.T) {
	dir := t.TempDir()
	for p, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := catalog.Load(filepath.Join(dir, "catalog"))
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, l := range cat.Locations {
		ids = append(ids, l.ID)
	}
	set, err := targets.Load(filepath.Join(dir, "targets"), ids)
	if err != nil {
		t.Fatal(err)
	}
	mods, err := os.ReadFile(filepath.Join(dir, "targets", "modules.yml"))
	if err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tokens := map[string]string{}
	for _, p := range probes {
		secret, _, err := st.CreateSubjectToken("probe-"+p, p, []string{"probe:write"}, time.Hour, "test")
		if err != nil {
			t.Fatal(err)
		}
		tokens[p] = secret
	}
	fromFiles := gatewayFor(t, st, tokens, func() *catalog.Catalog { return cat }, func() *targets.Set { return set }, func() []byte { return mods })

	db := configdb.Open(configdb.Deps{Store: st})
	in, err := configdb.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Import(context.Background(), configdb.Actor{Name: "linkos-beacon", Via: "start"}, in, "", false); err != nil {
		t.Fatal(err)
	}
	fromDB := gatewayFor(t, st, tokens, db.Catalog, db.Targets, db.Modules)

	compare := func(t *testing.T, a, b probeGateway) {
		t.Helper()
		for _, p := range probes {
			for _, path := range []string{"/api/v1/targets", "/api/v1/modules", "/api/v1/agent"} {
				c1, t1, b1 := a.get(t, p, path)
				c2, t2, b2 := b.get(t, p, path)
				if c1 != c2 || t1 != t2 || !bytes.Equal(b1, b2) {
					t.Errorf("%s %s:\nfiles:    %d %s %q\ndatabase: %d %s %q", p, path, c1, t1, b1, c2, t2, b2)
				}
			}
		}
	}
	compare(t, fromFiles, fromDB)
	if _, _, b := fromDB.get(t, "cdn-pre-us1", "/api/v1/targets"); !strings.Contains(string(b), "203.0.113.1") || strings.Contains(string(b), "seattle") {
		t.Errorf("the longest location prefix wins: %s", b)
	}
	if c, _, _ := fromDB.get(t, "cdn-old-cn2", "/api/v1/targets"); c != http.StatusForbidden {
		t.Errorf("an expired machine is refused: %d", c)
	}

	// an export imported into another database answers the same again
	out, _, err := db.Export()
	if err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(filepath.Join(t.TempDir(), "s2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	db2 := configdb.Open(configdb.Deps{Store: st2})
	if _, err := db2.Import(context.Background(), configdb.Actor{Name: "test", Via: "cli"}, out, "", false); err != nil {
		t.Fatal(err)
	}
	compare(t, fromFiles, gatewayFor(t, st, tokens, db2.Catalog, db2.Targets, db2.Modules))

	// a change takes effect at once, without a reload
	who := configdb.Actor{Name: "claude-code", Via: "mcp"}
	ctx := context.Background()
	if _, err := db.Put(ctx, who, "targets", "mainland-only", json.RawMessage(`[{"targets": ["https://cn2.example/"], "labels": {"module": "http_head", "service": "cn2"}}]`), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, b := fromDB.get(t, "cdn-pre-cn1", "/api/v1/targets"); !strings.Contains(string(b), "cn2.example") || strings.Contains(string(b), "https://cn.example/") {
		t.Errorf("targets after a change: %s", b)
	}
	if _, err := db.Put(ctx, who, "locations", "cdn-pre-cn1", json.RawMessage(`{"name": "Mainland China - Zhejiang Telecom", "region": "mainland", "probe": {"timeout": "50s"}}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, b := fromDB.get(t, "cdn-pre-cn1", "/api/v1/agent"); string(b) != `{"location":"cdn-pre-cn1","probe_timeout_seconds":50}`+"\n" {
		t.Errorf("settings after a change: %s", b)
	}
	if _, err := db.Put(ctx, who, "modules", "tcp_443", json.RawMessage(`{"prober": "tcp", "timeout": "5s", "tcp": {"tls": true}}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, b := fromDB.get(t, "cdn-pre", "/api/v1/modules"); !strings.Contains(string(b), "timeout: 5s") || !strings.Contains(string(b), "# nothing else") {
		t.Errorf("modules after a change:\n%s", b)
	}
	if _, err := db.Delete(ctx, who, "locations", "cdn-pre", ""); err == nil || !strings.Contains(err.Error(), "cdn-pre-x.yaml") {
		t.Errorf("a location with a target set named after it cannot go: %v", err)
	}
	if c, _, _ := fromDB.get(t, "cdn-gone", "/api/v1/agent"); c != http.StatusForbidden {
		t.Errorf("a location not in the catalogue: %d", c)
	}
}
