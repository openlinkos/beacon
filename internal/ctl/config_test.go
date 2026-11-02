package ctl

import (
	"context"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/store"
)

func TestConfig(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := configdb.Open(configdb.Deps{Store: st})
	srv := httptest.NewServer(ConfigHandler(cfg))
	defer srv.Close()
	c := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
	})
	ctx := context.Background()
	files := configdb.Files{
		"catalog/locations.yaml": "locations: [{id: edge-1, name: United States - Los Angeles, region: overseas}]\n",
		"targets/all-web.yaml":   "- targets: [https://linkos.org/]\n  labels: {module: http_head, service: linkos.org}\n",
		configdb.ModulesFile:     "modules:\n  http_head: {prober: http, timeout: 20s}\n",
	}

	dry, err := c.ImportConfig(ctx, ImportRequest{Files: files, DryRun: true})
	if err != nil || dry.Version != 0 || len(dry.Items) != 3 || cfg.Version() != 0 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	ch, err := c.ImportConfig(ctx, ImportRequest{Files: files, Note: "from the ops repository"})
	if err != nil || ch.Version != 1 || ch.Via != "cli" || !strings.HasPrefix(ch.Actor, "cli:") || ch.Note != "from the ops repository" {
		t.Fatalf("import: %+v %v", ch, err)
	}
	bad := map[string]string{"catalog/locations.yaml": "locations: [{id: edge-1, name: x, region: earth}]\n"}
	if _, err := c.ImportConfig(ctx, ImportRequest{Files: bad}); err == nil || !strings.Contains(err.Error(), "region must be overseas or mainland") {
		t.Errorf("an import that fails the checks says why: %v", err)
	}
	out, err := c.ExportConfig(ctx)
	if err != nil || out.Version != 1 || out.Files[configdb.ModulesFile] != files[configdb.ModulesFile] || len(out.Files) != 3 {
		t.Fatalf("export: %+v %v", out, err)
	}
	if again, err := c.ImportConfig(ctx, ImportRequest{Files: out.Files}); err != nil || !again.Unchanged {
		t.Errorf("an export imports to nothing new: %+v %v", again, err)
	}
	if state, err := c.CheckConfig(ctx); err != nil || state.Version != 1 || state.Problem != "" || !strings.HasPrefix(state.Summary, "1 probe locations") {
		t.Errorf("check: %+v %v", state, err)
	}
	r, err := c.RevertConfig(ctx, RevertRequest{Version: 1})
	if err != nil || r.Reverts != 1 || cfg.Summary() != "0 probe locations, 0 heartbeats, 0 resources, 0 probe targets in 0 sets, 0 modules" {
		t.Fatalf("revert: %+v %v", r, err)
	}
	if _, err := c.RevertConfig(ctx, RevertRequest{Version: 9}); err == nil || !strings.Contains(err.Error(), "no change made version 9") {
		t.Errorf("revert an unknown version: %v", err)
	}
	h, err := c.ConfigHistory(ctx, 10)
	if err != nil || len(h) != 2 || h[0].Action != "revert" || h[1].Items[0].After != nil {
		t.Errorf("history: %+v %v", h, err)
	}
	if n := len(st.AuditLog(10)); n != 2 {
		t.Errorf("audit entries: %d", n)
	}
}

func TestConfigWarnings(t *testing.T) {
	// a heartbeat naming a machine that is not enrolled is imported, and both the import and the check
	// say so
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := httptest.NewServer(ConfigHandler(configdb.Open(configdb.Deps{Store: st})))
	defer srv.Close()
	c := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
	})
	ctx := context.Background()
	files := configdb.Files{"catalog/heartbeats.yaml": "heartbeats: [{name: nas-backup, max_age: 26h, severity: warning, machine: nas-1}]\n"}
	ch, err := c.ImportConfig(ctx, ImportRequest{Files: files})
	if err != nil || len(ch.Warnings) != 1 || !strings.Contains(ch.Warnings[0], `machine "nas-1", which is not enrolled`) {
		t.Fatalf("import: %+v %v", ch, err)
	}
	if state, err := c.CheckConfig(ctx); err != nil || state.Problem != "" || len(state.Warnings) != 1 {
		t.Errorf("check: %+v %v", state, err)
	}
	if err := st.PutMachine(store.Machine{Name: "nas-1", Address: "nas-1.example.com:22", User: "beacon"}); err != nil {
		t.Fatal(err)
	}
	if state, err := c.CheckConfig(ctx); err != nil || len(state.Warnings) != 0 {
		t.Errorf("check once the machine is enrolled: %+v %v", state, err)
	}
}
