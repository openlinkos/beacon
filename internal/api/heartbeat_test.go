package api

import (
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/store"
)

// A heartbeat names its machine through the API: refused while the machine is not enrolled, taken once
// it is, and read back with it.
func TestConfigHeartbeatMachine(t *testing.T) {
	e, c := setupConfig(t)
	write := e.Token(t, "claude-code", "config:write")
	body := `{"max_age": "5m", "severity": "critical", "description": "The drill timer", "machine": "develop-cn"}`
	code, out := c.do("PUT", "/api/v1/config/heartbeats/drill-develop-cn", write, body)
	if msg, _ := out["error"].(string); code != 422 || out["code"] != "config.invalid" || !strings.Contains(msg, `machine "develop-cn" is not enrolled on the SSH channel`) {
		t.Fatalf("a machine not enrolled: %d %v", code, out)
	}
	if err := e.Store.PutMachine(store.Machine{Name: "develop-cn", Address: "develop-cn.example.com:22", User: "beacon"}); err != nil {
		t.Fatal(err)
	}
	if code, out := c.do("PUT", "/api/v1/config/heartbeats/drill-develop-cn", write, body); code != 200 || out["version"] != float64(1) || out["warnings"] != nil {
		t.Fatalf("an enrolled machine: %d %v", code, out)
	}
	if code, body := c.raw("GET", "/api/v1/config/heartbeats/drill-develop-cn", write); code != 200 || !strings.Contains(body, `"machine": "develop-cn"`) {
		t.Errorf("read back: %d %s", code, body)
	}
	// the machine goes; the change says so, and the heartbeat can still be changed
	if err := e.Store.DeleteMachine("develop-cn"); err != nil {
		t.Fatal(err)
	}
	code, out = c.do("PUT", "/api/v1/config/heartbeats/drill-develop-cn", write, strings.Replace(body, "5m", "10m", 1))
	if ws, _ := out["warnings"].([]any); code != 200 || len(ws) != 1 || !strings.Contains(ws[0].(string), "not enrolled") {
		t.Errorf("a machine gone since: %d %v", code, out)
	}
}
