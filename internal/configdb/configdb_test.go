package configdb

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/probe"
	"github.com/openlinkos/beacon/internal/store"
)

const modulesYAML = `# how probes check
modules:
  http_head:
    prober: http
    timeout: 20s # slow sites
    http:
      method: HEAD
      valid_status_codes: [200]
  tcp_443:
    prober: tcp
    tcp: {tls: true}
`

// testFiles is a deployment's files as they were before the database: the catalogue split over two
// files, targets for every kind of prefix, and a module file with comments.
func testFiles() Files {
	return Files{
		"catalog/locations.yaml": `locations:
  - {id: cdn-pre-us1, name: United States - Los Angeles CN2 GIA, region: overseas, network: CN2 GIA, probe: {timeout: 45s, retries: 1}}
  - {id: cdn-pre-cn1, name: Mainland China - Zhejiang Telecom, region: mainland, network: Telecom, address: 198.51.100.7, rotation: cdn.example.com}
`,
		"catalog/other.yaml": `heartbeats:
  - {name: nas-backup, max_age: 26h, severity: warning, description: Nightly backup of the NAS}
resources:
  - {name: cdn-pre-us1, kind: vps, expires: 2027-04-02}
  - {name: example.com, kind: domain, expires: 2027-01-15, note: renews automatically}
`,
		"targets/all-web.yaml":          "- targets: [https://linkos.org/, https://example.com/]\n  labels: {module: http_head, service: linkos.org, severity: critical}\n",
		"targets/all-web-x.yaml":        "- targets: [https://x.example/]\n  labels: {module: http_head, service: x}\n",
		"targets/mainland-only.yaml":    "- targets: [https://cn.example/]\n  labels: {module: http_head, service: cn}\n",
		"targets/cdn-pre-us1-node.yaml": "- targets: ['203.0.113.1:443']\n  labels: {module: tcp_443, service: node, port: 443}\n",
		"targets/overseas-nodes.yaml":   "- targets: ['203.0.113.2:443']\n  labels: {module: tcp_443, service: node2}\n",
		ModulesFile:                     modulesYAML,
	}
}

type bot struct {
	mu    sync.Mutex
	texts []string
}

func (b *bot) Send(_ context.Context, t string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.texts = append(b.texts, t)
	return nil
}

func (b *bot) last() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.texts) == 0 {
		return ""
	}
	return b.texts[len(b.texts)-1]
}

type env struct {
	s   *Service
	st  *store.Store
	bot *bot
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	b := &bot{}
	return &env{s: Open(Deps{Store: st, Bot: b, Lang: i18n.EN}), st: st, bot: b}
}

var (
	ctx = context.Background()
	api = Actor{Name: "claude-code", Via: "api"}
)

func imported(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	if _, err := e.s.Import(ctx, Actor{Name: "beacon", Via: "start"}, testFiles(), "", false); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestImportAndExportRoundTrip(t *testing.T) {
	e := newEnv(t)
	if e.s.Version() != 0 || len(e.s.Catalog().Locations) != 0 || e.s.Modules() != nil {
		t.Fatal("a new database holds nothing")
	}
	dry, err := e.s.Import(ctx, api, testFiles(), "", true)
	if err != nil || dry.Version != 0 || len(dry.Items) != 12 || e.s.Version() != 0 {
		t.Fatalf("a dry run changes nothing: %+v %v", dry, err)
	}
	ch, err := e.s.Import(ctx, Actor{Name: "beacon", Via: "start"}, testFiles(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Version != 1 || ch.Action != "import" || len(ch.Items) != 12 || ch.Items[0].Kind != KindLocation || ch.Items[0].Op != "added" {
		t.Fatalf("import: %+v", ch)
	}
	if string(e.s.Modules()) != modulesYAML {
		t.Errorf("the module file must be kept byte for byte:\n%s", e.s.Modules())
	}
	if c := e.s.Catalog(); len(c.Locations) != 2 || len(c.Heartbeats) != 1 || len(c.Resources) != 2 || c.Resources[0].ExpiresAt().IsZero() {
		t.Errorf("catalogue: %+v", c)
	}
	if n := e.s.Targets().Count(); n != 6 {
		t.Errorf("targets: %d", n)
	}
	if got := e.s.Summary(); got != "2 probe locations, 1 heartbeats, 2 resources, 6 probe targets in 5 sets, 2 modules" {
		t.Errorf("summary: %s", got)
	}

	out, v, err := e.s.Export()
	if err != nil || v != 1 {
		t.Fatalf("export: %v %v", v, err)
	}
	if out[ModulesFile] != modulesYAML || len(out) != 9 {
		t.Errorf("export: %v", slicesOf(out))
	}
	for _, want := range []string{"id: cdn-pre-us1", "timeout: 45s", "retries: 1", "rotation: cdn.example.com"} {
		if !strings.Contains(out["catalog/locations.yaml"], want) {
			t.Errorf("locations.yaml lacks %q:\n%s", want, out["catalog/locations.yaml"])
		}
	}
	if strings.Contains(out["catalog/locations.yaml"], "standby") || !strings.Contains(out["catalog/heartbeats.yaml"], "max_age: 26h\n") {
		t.Errorf("exports leave empty fields out and write durations as people do:\n%s%s", out["catalog/locations.yaml"], out["catalog/heartbeats.yaml"])
	}
	if again, err := e.s.Import(ctx, api, out, "", false); err != nil || !again.Unchanged || again.Version != 1 {
		t.Errorf("importing an export changes nothing: %+v %v", again, err)
	}
	// a module file whose comments changed is a change with no items, so probes get it as given
	f := testFiles()
	f[ModulesFile] = strings.Replace(modulesYAML, "# slow sites", "# slow sites, kept as given", 1)
	if ch, err := e.s.Import(ctx, api, f, "", false); err != nil || ch.Unchanged || len(ch.Items) != 0 || string(e.s.Modules()) != f[ModulesFile] {
		t.Errorf("comments: %+v %v", ch, err)
	}
	if _, err := e.s.Import(ctx, api, testFiles(), "", false); err != nil {
		t.Fatal(err)
	}
	out, _, _ = e.s.Export()

	// through a directory, which may hold files the export replaces
	dir := t.TempDir()
	for _, p := range []string{"catalog/old.yaml", "targets/all-gone.yaml", "targets/notes.txt"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o750)
		if err := os.WriteFile(filepath.Join(dir, p), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	written, removed, err := WriteDir(dir, out)
	if err != nil || len(written) != 9 || strings.Join(removed, ",") != "catalog/old.yaml,targets/all-gone.yaml" {
		t.Fatalf("WriteDir: %v %v %v", written, removed, err)
	}
	back, err := ReadDir(dir)
	if err != nil || !maps.Equal(back, out) {
		t.Fatalf("ReadDir gives back what was written: %v %v", slicesOf(back), err)
	}
	if _, err := os.Stat(filepath.Join(dir, "targets", "notes.txt")); err != nil {
		t.Error("files the loaders never read are left alone")
	}

	// the database keeps it
	s2 := Open(Deps{Store: e.st})
	if s2.Version() != 3 || s2.Summary() != e.s.Summary() || string(s2.Modules()) != modulesYAML || s2.Check() != nil {
		t.Errorf("reopened: %d %s %v", s2.Version(), s2.Summary(), s2.Check())
	}
}

// TestReloadTakesTheDatabase: a second service on the same store keeps its start snapshot until Reload.
func TestReloadTakesTheDatabase(t *testing.T) {
	e := imported(t)
	s2 := Open(Deps{Store: e.st, Lang: i18n.EN})
	f := testFiles()
	f["catalog/locations.yaml"] += "  - {id: cdn-pre-eu1, name: Europe, region: overseas, network: CN2 GIA, probe: {timeout: 45s, retries: 1}}\n"
	if _, err := e.s.Import(ctx, api, f, "", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Catalog().Location("cdn-pre-eu1"); ok {
		t.Fatal("the other service saw the change without Reload")
	}
	s2.Reload()
	if _, ok := s2.Catalog().Location("cdn-pre-eu1"); !ok {
		t.Fatal("Reload did not pick up the change")
	}
}

func slicesOf(f Files) []string { return sortedKeys(f) }

func TestReadFiles(t *testing.T) {
	d := t.TempDir()
	f, err := ReadFiles(filepath.Join(d, "nope"), filepath.Join(d, "nope2"), filepath.Join(d, "modules.yml"))
	if err != nil || len(f) != 0 {
		t.Errorf("missing files are left out: %v %v", f, err)
	}
	if _, err := ReadDir(d); err == nil || !strings.Contains(err.Error(), "holds no configuration") {
		t.Errorf("an empty import directory is refused: %v", err)
	}
	if _, err := ReadDir(filepath.Join(d, "nope")); err == nil {
		t.Error("a missing import directory is refused")
	}
	if _, _, err := WriteDir(d, Files{"../escape.yaml": "x"}); err == nil {
		t.Error("an export cannot write outside its directory")
	}
	if _, err := newEnv(t).s.Import(ctx, api, Files{"catalog/../../x.yaml": "x"}, "", false); !errors.Is(err, ErrBadInput) {
		t.Errorf("an import names only its own files: %v", err)
	}
}

func TestImportChecksLikeTheLoaders(t *testing.T) {
	e := newEnv(t)
	for name, f := range map[string]Files{
		"catalogue":  {"catalog/c.yaml": "locations: [{id: BAD, name: x, region: overseas}]\n"},
		"unknown":    {"catalog/c.yaml": "locations: [{id: a, name: x, region: overseas, vendor: acme}]\n"},
		"prefix":     {"targets/us-web.yaml": "- targets: [a]\n  labels: {module: m, service: s}\n"},
		"labels":     {"targets/all-web.yaml": "- targets: [a]\n  labels: {service: s}\n"},
		"module":     {ModulesFile: "modules: {x: {prober: smoke}}\n"},
		"badyaml":    {"catalog/c.yaml": "locations: [\n"},
		"wrong file": {"elsewhere/x.yaml": ""},
	} {
		if _, err := e.s.Import(ctx, api, f, "", false); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if e.s.Version() != 0 {
		t.Error("nothing was imported")
	}
}

func TestPutAndDelete(t *testing.T) {
	e := imported(t)
	loc := `{"id": "cdn-pro-us2", "name": "United States - Seattle", "region": "overseas", "network": "9929", "probe": {"timeout": "30s"}}`
	ch, err := e.s.Put(ctx, api, "locations", "cdn-pro-us2", json.RawMessage(loc), "new node")
	if err != nil || ch.Version != 2 || ch.Action != "put" || len(ch.Items) != 1 || ch.Items[0].Op != "added" || ch.Note != "new node" {
		t.Fatalf("put: %+v %v", ch, err)
	}
	if l, ok := e.s.Catalog().Location("cdn-pro-us2"); !ok || l.Probe.Timeout.D().Seconds() != 30 {
		t.Errorf("the catalogue in force changes at once: %+v", l)
	}
	if got := e.bot.last(); got != "claude-code added location cdn-pro-us2 via API (version 2)\nNote: new node" {
		t.Errorf("echo: %q", got)
	}
	if a := e.st.AuditLog(1); len(a) != 1 || a[0].Action != "config_put" || a[0].Target != "location cdn-pro-us2" || a[0].Via != "api" {
		t.Errorf("audit: %+v", a)
	}
	if again, err := e.s.Put(ctx, api, "location", "cdn-pro-us2", json.RawMessage(loc), ""); err != nil || !again.Unchanged || again.Version != 2 {
		t.Errorf("the same value again changes nothing: %+v %v", again, err)
	}
	// the id comes from the path when the body leaves it out
	if _, err := e.s.Put(ctx, api, "locations", "cdn-pro-us2", json.RawMessage(`{"name": "United States - Seattle 9929", "region": "overseas"}`), ""); err != nil {
		t.Errorf("without an id: %v", err)
	}
	raw, err := e.s.Item("locations", "cdn-pro-us2")
	if err != nil || string(raw) != `{"id":"cdn-pro-us2","name":"United States - Seattle 9929","region":"overseas"}` {
		t.Errorf("item: %s %v", raw, err)
	}

	for name, c := range map[string]struct {
		kind, key, value string
		want             error
	}{
		"unknown field": {"locations", "a", `{"name": "x", "region": "overseas", "vendor": "acme"}`, ErrBadInput},
		"other id":      {"locations", "a", `{"id": "b", "name": "x", "region": "overseas"}`, ErrBadInput},
		"bad region":    {"locations", "a", `{"name": "x", "region": "earth"}`, ErrInvalid},
		"bad id":        {"locations", "A_B", `{"name": "x", "region": "overseas"}`, ErrInvalid},
		"long timeout":  {"locations", "a", `{"name": "x", "region": "overseas", "probe": {"timeout": "2m"}}`, ErrInvalid},
		"number":        {"locations", "a", `{"name": "x", "region": "overseas", "probe": {"timeout": 30}}`, ErrBadInput},
		"short max age": {"heartbeats", "h", `{"max_age": "10s", "severity": "warning"}`, ErrInvalid},
		"severity":      {"heartbeats", "h", `{"max_age": "1h", "severity": "page"}`, ErrInvalid},
		"date":          {"resources", "r", `{"kind": "vps", "expires": "27/11/2026"}`, ErrInvalid},
		"set prefix":    {"targets", "us-web", `[{"targets": ["a"], "labels": {"module": "m", "service": "s"}}]`, ErrInvalid},
		"no labels":     {"targets", "all-web", `[{"targets": ["a"], "labels": {"service": "s"}}]`, ErrInvalid},
		"no targets":    {"targets", "all-web", `[{"targets": [], "labels": {"module": "m", "service": "s"}}]`, ErrInvalid},
		"set path":      {"targets", "../all-x", `[]`, ErrBadInput},
		"not a list":    {"targets", "all-x", `{"targets": ["a"]}`, ErrBadInput},
		"kind":          {"robots", "r", `{}`, ErrBadInput},
		"empty":         {"locations", "a", ``, ErrBadInput},
		"null":          {"locations", "a", `null`, ErrBadInput},
	} {
		if _, err := e.s.Put(ctx, api, c.kind, c.key, json.RawMessage(c.value), ""); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if e.s.Version() != 3 {
		t.Errorf("refused changes make no version: %d", e.s.Version())
	}

	// a target set by its file name; who probes it follows the name
	ch, err = e.s.Put(ctx, api, "targets", "cdn-pro-us2-local.yaml", json.RawMessage(`[{"targets": ["https://local.example/"], "labels": {"module": "http_head", "service": "local"}}]`), "")
	if err != nil || ch.Items[0].Key != "cdn-pro-us2-local" {
		t.Fatalf("target set: %+v %v", ch, err)
	}
	v, err := e.s.Get("targets", "cdn-pro-us2-local")
	if err != nil || len(v.Targets) != 1 || v.Targets[0].ProbedBy != "cdn-pro-us2" || v.Locations != nil {
		t.Errorf("get: %+v %v", v, err)
	}
	ids := []string{"cdn-pre-us1", "cdn-pre-cn1", "cdn-pro-us2"}
	if gs := e.s.Targets().For("cdn-pro-us2", "overseas", ids); len(gs) != 4 || gs[2].Labels["service"] != "local" {
		t.Errorf("the new location probes its set, in the order of the file names: %+v", gs)
	}
	// a location cannot go while a set is named after it
	if _, err := e.s.Delete(ctx, api, "locations", "cdn-pro-us2", ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "cdn-pro-us2-local.yaml") {
		t.Errorf("delete a location with its set: %v", err)
	}
	if _, err := e.s.Delete(ctx, api, "targets", "cdn-pro-us2-local", ""); err != nil {
		t.Fatal(err)
	}
	ch, err = e.s.Delete(ctx, Actor{Name: "cli:alex", Via: "cli"}, "locations", "cdn-pro-us2", "")
	if err != nil || ch.Items[0].Op != "removed" || ch.Items[0].After != nil {
		t.Fatalf("delete: %+v %v", ch, err)
	}
	if got := e.bot.last(); got != "cli:alex removed location cdn-pro-us2 via the command line (version 6)" {
		t.Errorf("echo: %q", got)
	}
	if _, err := e.s.Delete(ctx, api, "locations", "cdn-pro-us2", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
	if _, err := e.s.Get("locations", "cdn-pro-us2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get a deleted item: %v", err)
	}
	if _, err := e.s.Get("", "x"); !errors.Is(err, ErrBadInput) {
		t.Errorf("a key needs its kind: %v", err)
	}
}

func TestModules(t *testing.T) {
	e := imported(t)
	ch, err := e.s.Put(ctx, api, "modules", "git_repo", json.RawMessage(`{"prober": "git", "timeout": "15s", "git": {"username": "probe", "password_file": "git-password"}}`), "")
	if err != nil || ch.Items[0].Kind != KindModule || ch.Items[0].Op != "added" {
		t.Fatalf("put a module: %+v %v", ch, err)
	}
	text := string(e.s.Modules())
	for _, want := range []string{"# how probes check", "# slow sites", "git_repo:\n    prober: git\n    timeout: 15s\n", "password_file: git-password"} {
		if !strings.Contains(text, want) {
			t.Errorf("the module file lacks %q:\n%s", want, text)
		}
	}
	if _, err := probe.ParseModules(e.s.Modules()); err != nil {
		t.Errorf("the module file must stay valid: %v", err)
	}
	v, err := e.s.Get("modules", "http_head")
	if err != nil || len(v.Modules) != 1 || v.Modules["http_head"].(map[string]any)["timeout"] != "20s" {
		t.Errorf("get a module: %+v %v", v, err)
	}
	// a changed module keeps the others
	if _, err := e.s.Put(ctx, api, "module", "http_head", json.RawMessage(`{"prober": "http", "http": {"method": "GET", "valid_status_codes": [200, 204]}}`), ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(e.s.Modules()), "tcp_443") || strings.Contains(string(e.s.Modules()), "HEAD") {
		t.Errorf("changed:\n%s", e.s.Modules())
	}
	for name, value := range map[string]string{
		"unknown field": `{"prober": "http", "http": {"methd": "GET"}}`,
		"prober":        `{"prober": "smoke"}`,
		"regexp":        `{"prober": "http", "http": {"fail_if_body_not_matches_regexp": ["("]}}`,
	} {
		if _, err := e.s.Put(ctx, api, "modules", "bad", json.RawMessage(value), ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.s.Put(ctx, api, "modules", "bad", json.RawMessage(`["http"]`), ""); !errors.Is(err, ErrBadInput) {
		t.Errorf("a module is an object: %v", err)
	}
	if _, err := e.s.Delete(ctx, api, "modules", "git_repo", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(e.s.Modules()), "git_repo") {
		t.Error("deleted module still there")
	}
	if _, err := e.s.Delete(ctx, api, "modules", "git_repo", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete a missing module: %v", err)
	}
}

func TestModulesWithAnchors(t *testing.T) {
	e := newEnv(t)
	text := "modules:\n  base: &base\n    prober: http\n    timeout: 5s\n  head:\n    <<: *base\n    http: {method: HEAD}\n"
	if _, err := e.s.Import(ctx, api, Files{ModulesFile: text}, "", false); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.s.Get("modules", "head"); v.Modules["head"].(map[string]any)["prober"] != "http" {
		t.Errorf("a merged module shows what it merges: %+v", v)
	}
	if _, err := e.s.Delete(ctx, api, "modules", "base", ""); err != nil {
		t.Fatalf("removing an anchored module: %v", err)
	}
	ms, err := probe.ParseModules(e.s.Modules())
	if err != nil || ms.Modules["head"] == nil || ms.Modules["head"].Prober != "http" || ms.Modules["head"].HTTP.Method != "HEAD" {
		t.Fatalf("the module that merged it keeps what it had: %v\n%s", err, e.s.Modules())
	}
}

func TestHistoryAndRevert(t *testing.T) {
	e := imported(t)
	put := func(kind, key, value string) int64 {
		t.Helper()
		ch, err := e.s.Put(ctx, api, kind, key, json.RawMessage(value), "")
		if err != nil {
			t.Fatal(err)
		}
		return ch.Version
	}
	v2 := put("heartbeats", "nas-backup", `{"max_age": "48h", "severity": "info"}`)
	v3 := put("resources", "example.org", `{"kind": "domain", "expires": "2027-06-01"}`)
	v4 := put("modules", "tcp_443", `{"prober": "tcp", "timeout": "3s"}`)

	all, err := e.s.History(HistoryFilter{})
	if err != nil || len(all) != 4 || all[0].Version != v4 || all[3].Version != 1 || all[0].Items[0].Before != nil {
		t.Fatalf("history, newest first, without values: %+v %v", all, err)
	}
	hb, _ := e.s.History(HistoryFilter{Kind: "heartbeats", Key: "nas-backup"})
	if len(hb) != 2 || hb[0].Version != v2 || hb[1].Version != 1 {
		t.Errorf("history of one item: %+v", hb)
	}
	if page, _ := e.s.History(HistoryFilter{Before: v3, Limit: 1}); len(page) != 1 || page[0].Version != v2 {
		t.Errorf("paging: %+v", page)
	}
	if _, err := e.s.History(HistoryFilter{Key: "x"}); !errors.Is(err, ErrBadInput) {
		t.Errorf("a key needs its kind: %v", err)
	}
	ch, err := e.s.Change(v2)
	if err != nil || !strings.Contains(string(ch.Items[0].Before), `"26h"`) || !strings.Contains(string(ch.Items[0].After), `"48h"`) {
		t.Errorf("a change has its values: %+v %v", ch, err)
	}

	r, err := e.s.Revert(ctx, api, v2, "too lax")
	if err != nil || r.Action != "revert" || r.Reverts != v2 {
		t.Fatalf("revert: %+v %v", r, err)
	}
	if h := e.s.Catalog().Heartbeats[0]; h.MaxAge.D().Hours() != 26 || h.Severity != "warning" {
		t.Errorf("reverted: %+v", h)
	}
	if got := e.bot.last(); got != "claude-code reverted configuration change 2 via API: 0 added, 1 changed, 0 removed (version 5)\nNote: too lax" {
		t.Errorf("echo: %q", got)
	}
	// reverting an addition removes it; a module goes back into the file
	if _, err := e.s.Revert(ctx, api, v3, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Item("resources", "example.org"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revert of an addition: %v", err)
	}
	if _, err := e.s.Revert(ctx, api, v4, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(e.s.Modules()), "tls: true") {
		t.Errorf("module reverted:\n%s", e.s.Modules())
	}
	// an item changed again cannot be reverted past the later change
	v8 := put("heartbeats", "nas-backup", `{"max_age": "30h", "severity": "warning"}`)
	if _, err := e.s.Revert(ctx, api, v2, ""); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "changed again in version 8") {
		t.Errorf("a conflicting revert: %v", err)
	}
	// reverting the revert of a revert is a change like any other; and the import itself can be reverted
	if _, err := e.s.Revert(ctx, api, v8, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Revert(ctx, api, 1, "start over"); err != nil {
		t.Fatalf("revert the import: %v", err)
	}
	if s := e.s.Summary(); s != "0 probe locations, 0 heartbeats, 0 resources, 0 probe targets in 0 sets, 0 modules" {
		t.Errorf("after reverting the import: %s %q", s, e.s.Modules())
	}
	if _, err := e.s.Revert(ctx, api, 99, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown version: %v", err)
	}
}

func TestImportOnSignal(t *testing.T) {
	e := imported(t)
	f := testFiles()
	f["targets/all-more.yaml"] = "- targets: [https://more.example/]\n  labels: {module: http_head, service: more}\n"
	ch, err := e.s.ImportOnSignal(ctx, f)
	if err != nil || ch.Via != "signal" || len(ch.Items) != 1 {
		t.Fatalf("SIGHUP after an import: %+v %v", ch, err)
	}
	if !strings.HasPrefix(e.bot.last(), "Beacon imported its configuration files on SIGHUP: 1 added") {
		t.Errorf("echo: %q", e.bot.last())
	}
	if _, err := e.s.Put(ctx, api, "targets", "all-agent", json.RawMessage(`[{"targets": ["https://a.example/"], "labels": {"module": "http_head", "service": "a"}}]`), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ImportOnSignal(ctx, f); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "config export") {
		t.Errorf("SIGHUP after a change through Beacon: %v", err)
	}
	if _, err := e.s.Item("targets", "all-agent"); err != nil {
		t.Error("the change through Beacon is kept")
	}
}

func TestOpenKeepsServingWhatNoLongerPasses(t *testing.T) {
	e := newEnv(t)
	bad, _ := json.Marshal(map[string]string{"id": "a", "name": "x", "region": "earth"})
	good, _ := json.Marshal(map[string]string{"id": "b", "name": "y", "region": "mainland"})
	if _, err := e.st.CommitConfig(store.ConfigChange{Action: "put"}, []store.ConfigWrite{{Key: "location/a", Value: bad}, {Key: "location/b", Value: good}}); err != nil {
		t.Fatal(err)
	}
	s := Open(Deps{Store: e.st})
	if err := s.Check(); err == nil || !strings.Contains(err.Error(), "earth") {
		t.Errorf("Check: %v", err)
	}
	if _, ok := s.Catalog().Location("b"); !ok {
		t.Error("the rest is served")
	}
	if _, err := s.Put(ctx, api, "heartbeats", "h", json.RawMessage(`{"max_age": "1h", "severity": "info"}`), ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("changes wait until it is put right: %v", err)
	}
	if _, err := s.Put(ctx, api, "locations", "a", json.RawMessage(`{"name": "x", "region": "overseas"}`), ""); err != nil || s.Check() != nil {
		t.Errorf("putting it right: %v %v", err, s.Check())
	}
}

func TestEchoes(t *testing.T) {
	e := newEnv(t)
	e.s.d.Lang = i18n.ZH
	if _, err := e.s.Import(ctx, Actor{Name: "beacon", Via: "start"}, testFiles(), "", false); err != nil {
		t.Fatal(err)
	}
	zh := func(format string, args ...any) string { return i18n.T(i18n.ZH, format, args...) }
	want := zh("Beacon imported its configuration files at start: %s (version %d)", zh("%d added, %d changed, %d removed", 12, 0, 0), 1)
	if got := e.bot.last(); got != want || strings.Contains(got, "imported") {
		t.Errorf("zh: %q", got)
	}
	if _, err := e.s.Put(ctx, Actor{Name: "codex", Via: "mcp"}, "targets", "all-web", json.RawMessage(`[{"targets": ["https://linkos.org/"], "labels": {"module": "http_head", "service": "linkos.org"}}]`), ""); err != nil {
		t.Fatal(err)
	}
	want = zh("%s changed %s %s via %s (version %d)", "codex", zh("target set"), "all-web", "MCP", 2)
	if got := e.bot.last(); got != want || strings.Contains(got, "target set") || !strings.HasPrefix(got, "codex ") {
		t.Errorf("zh: %q", got)
	}
}
