package api

import (
	"strconv"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/store"
)

// TestTargetSetChangesHoldResponseMachines: a put of a whole target set, a delete and a revert that
// change a service's response machine take what the dedicated route takes: machines:write and access to
// the machine designated before and after, at its current registration.
func TestTargetSetChangesHoldResponseMachines(t *testing.T) {
	e, c := setupConfig(t)
	configWriter := e.Token(t, "config-writer", "config:write")
	writer := e.Token(t, "operator", "config:write", "machines:write", "machines:ssh")
	if err := e.Store.PutMachine(store.Machine{Name: "app-1", Address: "app-1.example.com:22"}); err != nil {
		t.Fatal(err)
	}
	m, _ := e.Store.Machine("app-1")
	config := "/api/v1/config"
	path := config + "/targets/all-web"
	plain := `[{"targets":["https://first.example.com/"],"labels":{"module":"http_2xx","service":"front","severity":"critical","failure_domain":"front"}}]`
	bound := `[{"targets":["https://first.example.com/"],"labels":{"module":"http_2xx","service":"front","severity":"critical","failure_domain":"front"},` +
		`"machine":{"name":"app-1","registration":"` + m.Registration() + `"}}]`
	if code, out := c.do("PUT", config+"/modules/http_2xx", configWriter, `{"prober":"http"}`); code != 200 {
		t.Fatalf("module: %d %v", code, out)
	}
	if code, out := c.do("PUT", path, configWriter, plain); code != 200 {
		t.Fatalf("target set: %d %v", code, out)
	}
	// config:write alone designates no machine through a put
	if code, out := c.do("PUT", path, configWriter, bound); code != 403 || out["code"] != "auth.scope" {
		t.Errorf("a put designates a machine with config:write alone: %d %v", code, out)
	}
	code, out := c.do("PUT", path, writer, bound)
	if code != 200 {
		t.Fatalf("a put by someone who may: %d %v", code, out)
	}
	set := strconv.Itoa(int(out["version"].(float64)))
	// nor clears one, by a put, a delete or a revert
	if code, out := c.do("PUT", path, configWriter, plain); code != 403 || out["code"] != "auth.scope" {
		t.Errorf("a put clears a machine with config:write alone: %d %v", code, out)
	}
	if code, out := c.do("DELETE", path, configWriter, ""); code != 403 || out["code"] != "auth.scope" {
		t.Errorf("a delete clears a machine with config:write alone: %d %v", code, out)
	}
	if code, out := c.do("POST", config+"/history/"+set+"/revert", configWriter, ""); code != 403 || out["code"] != "auth.scope" {
		t.Errorf("a revert clears a machine with config:write alone: %d %v", code, out)
	}
	if code, result := c.raw("GET", path, configWriter); code != 200 || !strings.Contains(result, `"app-1"`) {
		t.Fatalf("a refused change changed the designation: %d %s", code, result)
	}
	// a designation of a registration that is not the machine's current one is refused
	stale := strings.Replace(bound, m.Registration(), "0-2000-01-01T00:00:00Z", 1)
	if code, out := c.do("PUT", path, writer, stale); code != 409 || out["code"] != "config.conflict" {
		t.Errorf("a stale registration: %d %v", code, out)
	}
	if code, out := c.do("POST", config+"/history/"+set+"/revert", writer, ""); code != 200 {
		t.Errorf("a revert by someone who may: %d %v", code, out)
	}
}
