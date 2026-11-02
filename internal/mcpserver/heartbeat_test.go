package mcpserver

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
	"github.com/openlinkos/beacon/internal/vm"
)

// lastSuccessVM answers every instant query with one heartbeat's last success, and has no probes.
type lastSuccessVM struct{ at time.Time }

func (v lastSuccessVM) Query(context.Context, string) ([]vm.Sample, error) { return nil, nil }

func (v lastSuccessVM) QueryAt(context.Context, string, time.Time) ([]vm.Sample, error) {
	return []vm.Sample{{Labels: map[string]string{"heartbeat": "drill-1"}, Value: float64(v.at.Unix())}}, nil
}

func (v lastSuccessVM) QueryRange(context.Context, string, time.Time, time.Time, time.Duration) ([]vm.Series, error) {
	return nil, nil
}

// get_evidence and get_timeline answer for a heartbeat's alert in their output schemas: the heartbeat,
// the sessions before the alert with their phase and a shell's commands, and the timeline's new events.
func TestHeartbeatEvidenceTools(t *testing.T) {
	start := time.Now().Add(-5 * time.Minute).UTC().Truncate(time.Second)
	zero := 0
	e := testutil.NewWith(t, func(d *bridge.Deps) {
		cat := &catalog.Catalog{Heartbeats: []catalog.Heartbeat{{Name: "drill-1", MaxAge: config.Duration(5 * time.Minute), Severity: "critical", Machine: "develop-1"}}}
		d.Catalog = func() *catalog.Catalog { return cat }
		d.VM = lastSuccessVM{at: start.Add(-6 * time.Minute)}
		d.ShellCommands = func(string) []bridge.ShellCommand {
			return []bridge.ShellCommand{{Command: "systemctl stop drill.timer", Exit: &zero, Started: start.Add(-3 * time.Minute)}}
		}
	})
	if err := e.Store.PutMachine(store.Machine{Name: "develop-1", Address: "develop-1.example.com:22", User: "beacon"}); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"alertname": "HeartbeatStale", "heartbeat": "drill-1", "service": "drill-1", "severity": "critical"}
	if err := e.Bridge.HandleWebhook(context.Background(), alertmanager.Webhook{Version: "4", GroupKey: "drill", Status: "firing", GroupLabels: labels,
		CommonLabels: labels, Alerts: []alertmanager.Alert{{Status: "firing", Fingerprint: "d", Labels: labels, StartsAt: start}}}); err != nil {
		t.Fatal(err)
	}
	id := start.Add(-4*time.Minute).Format("20060102-150405") + "-abcdef-1"
	if err := e.Store.PutSession(store.SessionRecord{ID: id, Agent: "claude-code", Via: "ssh", Machine: "develop-1", Kind: "shell", Commands: 1,
		Started: start.Add(-4 * time.Minute), Ended: start.Add(-2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default()))
	defer srv.Close()
	read := connect(t, srv.URL, e.Token(t, "reader", "status:read"))
	res, text := call(t, read, "get_evidence", map[string]any{"id": bridge.GroupID("drill")})
	for _, want := range []string{`"heartbeats":[{`, `"name":"drill-1"`, `"enrolled":true`, `"machine":"develop-1"`, `"phase":"before"`,
		`"shell_commands":[{`, `"command":"systemctl stop drill.timer"`} {
		if res.IsError || !strings.Contains(text, want) {
			t.Errorf("get_evidence lacks %s: %s", want, text)
		}
	}
	res, text = call(t, read, "get_timeline", map[string]any{"id": bridge.GroupID("drill")})
	for _, want := range []string{`"kind":"heartbeat"`, `"kind":"command"`, `"phase":"before"`, `"heartbeats":[`} {
		if res.IsError || !strings.Contains(text, want) {
			t.Errorf("get_timeline lacks %s: %s", want, text)
		}
	}
}

// put_config takes a heartbeat's machine, once the machine is enrolled.
func TestPutHeartbeatMachine(t *testing.T) {
	e := testutil.New(t)
	cfg := configdb.Open(configdb.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN})
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default(), WithConfig(cfg)))
	defer srv.Close()
	w := connect(t, srv.URL, e.Token(t, "codex", "config:write"))
	in := map[string]any{"kind": "heartbeats", "key": "nas-backup", "value": map[string]any{"max_age": "26h", "severity": "warning", "machine": "nas-1"}}
	if res, text := call(t, w, "put_config", in); !res.IsError || !strings.Contains(text, `machine "nas-1" is not enrolled on the SSH channel`) {
		t.Errorf("a machine not enrolled: %s", text)
	}
	if err := e.Store.PutMachine(store.Machine{Name: "nas-1", Address: "nas-1.example.com:22", User: "beacon"}); err != nil {
		t.Fatal(err)
	}
	if res, text := call(t, w, "put_config", in); res.IsError || !strings.Contains(text, `"machine":"nas-1"`) {
		t.Fatalf("put_config: %s", text)
	}
	if h, ok := cfg.Catalog().Heartbeat("nas-backup"); !ok || h.Machine != "nas-1" {
		t.Errorf("in force: %+v", h)
	}
}
