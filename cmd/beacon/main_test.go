package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/store"
)

// TestMain makes password hashing cheap for the tests: the parameters are recorded in each hash.
func TestMain(m *testing.M) {
	store.PasswordParams = store.Argon2Params{Time: 1, Memory: 64, Threads: 1}
	os.Exit(m.Run())
}

func TestCheckConfig(t *testing.T) {
	d := t.TempDir()
	tok := filepath.Join(d, "tok")
	if err := os.WriteFile(tok, []byte("0123456789abcdef0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	cat := filepath.Join(d, "catalog")
	_ = os.Mkdir(cat, 0o700)
	_ = os.WriteFile(filepath.Join(cat, "locations.yaml"), []byte("locations: [{id: us-los-angeles, name: United States - Los Angeles, region: overseas}]\n"), 0o600)
	hook := filepath.Join(d, "hook")
	_ = os.WriteFile(hook, []byte("https://open.feishu.cn/open-apis/bot/v2/hook/x"), 0o600)
	cfg := filepath.Join(d, "config.yaml")
	body := "catalog_dir: " + filepath.ToSlash(cat) + "\nrules: {token_file: " + filepath.ToSlash(tok) + "}\nvictoriametrics: {url: http://vm:8428}\n" +
		"feishu: {webhook_url_file: " + filepath.ToSlash(hook) + "}\n" +
		"escalation: {critical: {channel: webhook}, warning: {channel: webhook}, info: {channel: webhook}}\n"
	_ = os.WriteFile(cfg, []byte(body), 0o600)

	var out, errb bytes.Buffer
	if code := run([]string{"check-config", "-config", cfg}, &out, &errb); code != 0 {
		t.Fatalf("check-config: %d %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "1 probe locations") {
		t.Errorf("output: %s", out.String())
	}
	_ = os.WriteFile(filepath.Join(cat, "bad.yaml"), []byte("locations: [{id: BAD}]\n"), 0o600)
	errb.Reset()
	if code := run([]string{"check-config", "-config", cfg}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "BAD") {
		t.Errorf("a bad catalogue: %d %s", code, errb.String())
	}
}

func TestCheckConfigDatabase(t *testing.T) {
	d := t.TempDir()
	tok := filepath.Join(d, "tok")
	_ = os.WriteFile(tok, []byte("0123456789abcdef0123456789"), 0o600)
	data := filepath.Join(d, "data")
	cfg := filepath.Join(d, "config.yaml")
	body := "data_dir: " + filepath.ToSlash(data) + "\ncatalog_dir: " + filepath.ToSlash(filepath.Join(d, "none")) +
		"\ngateway: {targets_dir: " + filepath.ToSlash(filepath.Join(d, "none")) + "}\nrules: {token_file: " + filepath.ToSlash(tok) + "}\nvictoriametrics: {url: http://vm:8428}\n"
	_ = os.WriteFile(cfg, []byte(body), 0o600)
	check := func() (int, string, string) {
		var out, errb bytes.Buffer
		code := run([]string{"check-config", "-config", cfg}, &out, &errb)
		return code, out.String(), errb.String()
	}
	if code, out, errs := check(); code != 0 || !strings.Contains(out, "files: none") || !strings.Contains(out, "database: none yet") {
		t.Fatalf("no files, no database: %d %s %s", code, out, errs)
	}

	st, err := store.Open(filepath.Join(data, "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	cdb := configdb.Open(configdb.Deps{Store: st})
	_, err = cdb.Import(context.Background(), configdb.Actor{Name: "test", Via: "cli"},
		configdb.Files{"catalog/c.yaml": "locations: [{id: edge-1, name: United States - Los Angeles, region: overseas}]\n"}, "", false)
	_ = st.Close()
	if err != nil {
		t.Fatal(err)
	}
	if code, out, errs := check(); code != 0 || !strings.Contains(out, "database: version 1: 1 probe locations") {
		t.Fatalf("a database: %d %s %s", code, out, errs)
	}

	st, err = store.Open(filepath.Join(data, "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CommitConfig(store.ConfigChange{Action: "put"}, []store.ConfigWrite{{Key: "heartbeat/nas", Value: []byte(`{"name":"nas","max_age":"10s","severity":"warning"}`)}})
	_ = st.Close()
	if err != nil {
		t.Fatal(err)
	}
	code, out, errs := check()
	if code != 1 || !strings.Contains(errs, "max_age must be at least 1m") {
		t.Errorf("a database that fails its checks: %d out=%q errs=%q", code, out, errs)
	}
}

func TestOpenConfigImportsOnce(t *testing.T) {
	d := t.TempDir()
	cfg := &config.Config{CatalogDir: filepath.Join(d, "catalog")}
	cfg.Gateway.TargetsDir = filepath.Join(d, "targets")
	cfg.Gateway.ModulesFile = filepath.Join(d, "targets", "modules.yml")
	for _, p := range []string{cfg.CatalogDir, cfg.Gateway.TargetsDir} {
		_ = os.Mkdir(p, 0o700)
	}
	st, err := store.Open(filepath.Join(d, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	// empty directories, as in the image, import nothing
	cdb, err := openConfig(ctx, cfg, st, nil, log)
	if err != nil || cdb.Version() != 0 {
		t.Fatalf("empty directories: %v %v", cdb.Version(), err)
	}
	_ = os.WriteFile(filepath.Join(cfg.CatalogDir, "c.yaml"), []byte("locations: [{id: BAD}]\n"), 0o600)
	if _, err := openConfig(ctx, cfg, st, nil, log); err == nil || !strings.Contains(err.Error(), "BAD") {
		t.Errorf("files that fail their checks stop the start: %v", err)
	}
	_ = os.WriteFile(filepath.Join(cfg.CatalogDir, "c.yaml"), []byte("locations: [{id: edge-1, name: United States - Los Angeles, region: overseas}]\n"), 0o600)
	_ = os.WriteFile(cfg.Gateway.ModulesFile, []byte("modules: {http_head: {prober: http}}\n"), 0o600)
	cdb, err = openConfig(ctx, cfg, st, nil, log)
	if err != nil || cdb.Version() != 1 || len(cdb.Catalog().Locations) != 1 || cdb.Modules() == nil {
		t.Fatalf("the first start imports the files: %v %v", cdb.Version(), err)
	}
	if h, _ := cdb.History(configdb.HistoryFilter{}); h[0].Via != "start" || h[0].Actor != "beacon" {
		t.Errorf("history: %+v", h[0])
	}
	// from then on the database is the source: changed files are not read at start
	_ = os.WriteFile(filepath.Join(cfg.CatalogDir, "c.yaml"), []byte("locations: []\n"), 0o600)
	cdb, err = openConfig(ctx, cfg, st, nil, log)
	if err != nil || cdb.Version() != 1 || len(cdb.Catalog().Locations) != 1 {
		t.Errorf("a configured database is not replaced by the files: %v %v", cdb.Version(), err)
	}
}

func TestConfigCommand(t *testing.T) {
	var out, errb bytes.Buffer
	for _, args := range [][]string{{"config"}, {"config", "import"}, {"config", "export"}, {"config", "revert", "x"}, {"config", "sideways"}, {"config", "history", "extra"}} {
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
	errb.Reset()
	if code := run([]string{"config", "import", t.TempDir()}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "holds no configuration") {
		t.Errorf("an empty directory: %d %s", code, errb.String())
	}
	var w bytes.Buffer
	printChange(&w, store.ConfigChange{Version: 3, Action: "import", Items: []store.ConfigItem{{Kind: "location", Key: "edge-1", Op: "added"}, {Kind: "module", Key: "tcp", Op: "removed"}}}, false)
	if w.String() != "version 3: import of 2 items\n  + location edge-1\n  - module tcp\n" {
		t.Errorf("printChange: %q", w.String())
	}
}

func TestUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if run(nil, &out, &errb) != 2 || run([]string{"bogus"}, &out, &errb) != 2 {
		t.Error("usage errors exit 2")
	}
	if run([]string{"version"}, &out, &errb) != 0 || !strings.Contains(out.String(), "beacon") {
		t.Error("version")
	}
	if run([]string{"drill", "sideways"}, &out, &errb) != 2 || run([]string{"token", "create"}, &out, &errb) != 2 {
		t.Error("bad subcommands exit 2")
	}
}
