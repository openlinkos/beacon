//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/store"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestSIGHUPImportsTheFiles(t *testing.T) {
	// Once anything listens for SIGHUP, the signal no longer ends the process.
	hold := make(chan os.Signal, 16)
	signal.Notify(hold, syscall.SIGHUP)
	defer signal.Stop(hold)

	d := t.TempDir()
	cfg := &config.Config{CatalogDir: filepath.Join(d, "catalog")}
	cfg.Gateway.TargetsDir = filepath.Join(d, "targets")
	cfg.Gateway.ModulesFile = filepath.Join(d, "targets", "modules.yml")
	_ = os.Mkdir(cfg.CatalogDir, 0o700)
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(cfg.CatalogDir, "c.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("locations: [{id: edge-1, name: United States - Los Angeles, region: overseas}]\n")
	st, err := store.Open(filepath.Join(d, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var logs syncBuffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cdb, err := openConfig(ctx, cfg, st, nil, log)
	if err != nil || cdb.Version() != 1 {
		t.Fatalf("start: %v %v", cdb.Version(), err)
	}
	go reloadOnHUP(ctx, cfg, cdb, log)

	// hang up until want is in the log, as reloadOnHUP may not be listening yet
	hangUp := func(want string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if strings.Contains(logs.String(), want) {
				return
			}
			_ = syscall.Kill(os.Getpid(), syscall.SIGHUP)
		}
		t.Fatalf("no %q in the log:\n%s", want, logs.String())
	}
	write("locations: [{id: edge-1, name: United States - Los Angeles, region: overseas}, {id: edge-2, name: Mainland China - Shanghai, region: mainland}]\n")
	hangUp("SIGHUP: imported the configuration files")
	if cdb.Version() != 2 || len(cdb.Catalog().Locations) != 2 {
		t.Fatalf("after SIGHUP: %d %+v", cdb.Version(), cdb.Catalog())
	}

	// a change through Beacon is not undone by the files
	if _, err := cdb.Put(ctx, configdb.Actor{Name: "claude-code", Via: "mcp"}, "locations", "edge-3",
		json.RawMessage(`{"name": "Japan - Tokyo", "region": "overseas"}`), ""); err != nil {
		t.Fatal(err)
	}
	hangUp("SIGHUP: the configuration files were not imported")
	if _, ok := cdb.Catalog().Location("edge-3"); !ok || cdb.Version() != 3 {
		t.Errorf("the change through Beacon is kept: %d", cdb.Version())
	}
	if !strings.Contains(logs.String(), "config export") {
		t.Errorf("the log says how to go on:\n%s", logs.String())
	}
}

// TestServeAndConfigCommands runs the service on files, then the config commands against it over the
// admin socket, as an operator would.
func TestServeAndConfigCommands(t *testing.T) {
	hold := make(chan os.Signal, 16)
	signal.Notify(hold, syscall.SIGHUP, syscall.SIGTERM)
	defer signal.Stop(hold)

	d := t.TempDir()
	put := func(path, body string) {
		t.Helper()
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	etc, data := filepath.Join(d, "etc"), filepath.Join(d, "data")
	put(filepath.Join(etc, "catalog", "locations.yaml"), "# where probes are\nlocations:\n  - {id: edge-1, name: United States - Los Angeles, region: overseas}\n")
	put(filepath.Join(etc, "targets", "all-web.yaml"), "- targets: [https://linkos.org/]\n  labels: {module: http_head, service: linkos.org}\n")
	put(filepath.Join(etc, "targets", "modules.yml"), "modules:\n  http_head: {prober: http, timeout: 20s}  # as the Blackbox Exporter\n")
	put(filepath.Join(d, "rules-token"), "0123456789abcdef0123456789")
	cfg := filepath.Join(d, "config.yaml")
	put(cfg, "listen: 127.0.0.1:0\ndata_dir: "+data+"\ncatalog_dir: "+filepath.Join(etc, "catalog")+"\nlanguage: en\n"+
		"gateway: {listen: 127.0.0.1:0, targets_dir: "+filepath.Join(etc, "targets")+"}\n"+
		"rules: {token_file: "+filepath.Join(d, "rules-token")+"}\nvictoriametrics: {url: http://127.0.0.1:1}\n")
	t.Setenv("LINKOS_BEACON_DATA_DIR", data)

	var logs syncBuffer
	done := make(chan int, 1)
	go func() { done <- run([]string{"serve", "-config", cfg}, &logs, &logs) }()
	cli := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := run(args, &out, &errb)
		return code, out.String() + errb.String()
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if code, _ := cli("config", "history"); code == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the service did not answer on its socket:\n%s", logs.String())
		}
	}
	if !strings.Contains(logs.String(), "imported the configuration files into the database") {
		t.Errorf("the first start imports the files:\n%s", logs.String())
	}
	if code, out := cli("config", "history"); code != 0 || !strings.Contains(out, "linkos-beacon  start") || !strings.Contains(out, "import") {
		t.Errorf("history: %d %s", code, out)
	}

	exp := filepath.Join(d, "export")
	if code, out := cli("config", "export", exp); code != 0 || !strings.Contains(out, "exported version 1") {
		t.Fatalf("export: %d %s", code, out)
	}
	if code, out := cli("config", "import", exp, "-dry-run"); code != 0 || !strings.Contains(out, "nothing to change") {
		t.Errorf("an export imports to nothing new: %d %s", code, out)
	}
	put(filepath.Join(exp, "targets", "overseas-nodes.yaml"), "- targets: ['203.0.113.2:443']\n  labels: {module: http_head, service: node}\n")
	if code, out := cli("config", "import", exp, "-dry-run"); code != 0 || !strings.Contains(out, "would change 1 items") || !strings.Contains(out, "+ targets overseas-nodes") {
		t.Errorf("dry run: %d %s", code, out)
	}
	if code, out := cli("config", "import", exp, "-note", "more nodes"); code != 0 || !strings.Contains(out, "version 2: import of 1 items") {
		t.Fatalf("import: %d %s", code, out)
	}
	put(filepath.Join(exp, "catalog", "locations.yaml"), "locations: [{id: edge-1, name: x, region: earth}]\n")
	if code, out := cli("config", "import", exp); code != 1 || !strings.Contains(out, "region must be overseas or mainland") {
		t.Errorf("an import that fails its checks: %d %s", code, out)
	}
	if code, out := cli("config", "revert", "2", "-note", "not yet"); code != 0 || !strings.Contains(out, "version 3: revert of 1 items") || !strings.Contains(out, "- targets overseas-nodes") {
		t.Errorf("revert: %d %s", code, out)
	}
	if code, out := cli("check-config", "-config", cfg); code != 0 || !strings.Contains(out, "database: version 3 in the running service: 1 probe locations") {
		t.Errorf("check-config: %d %s", code, out)
	}

	// the last change was not an import, so SIGHUP does not bring the files back over it
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(logs.String(), "SIGHUP: the configuration files were not imported"); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no SIGHUP in the log:\n%s", logs.String())
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGHUP)
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("serve ended with %d:\n%s", code, logs.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop on SIGTERM")
	}
}
