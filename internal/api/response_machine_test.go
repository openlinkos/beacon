package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

// TestResponseMachineFollowsWhatItAnswersFor: a designation is the machine with the service and probes it
// answers for. A put that keeps the machine where it was in the file but gives its group another
// service's labels moves the machine to that service, and takes what any change of a response machine
// takes; moving a group unchanged within its file changes no designation. Every change of a response
// machine takes access to all machines.
func TestResponseMachineFollowsWhatItAnswersFor(t *testing.T) {
	e, c := setupConfig(t)
	configWriter := e.Token(t, "config-writer", "config:write")
	writer := e.Token(t, "operator", "config:write", "machines:write", "machines:ssh")
	named := limited(t, e.Store, "named-operator", store.NamedAccess("app-1"), "config:write", "machines:write", "machines:ssh")
	if err := e.Store.PutMachine(store.Machine{Name: "app-1", Address: "app-1.example.com:22"}); err != nil {
		t.Fatal(err)
	}
	m, _ := e.Store.Machine("app-1")
	binding := `,"machine":{"name":"app-1","registration":"` + m.Registration() + `"}`
	group := func(service, url, extra string) string {
		return `{"targets":["` + url + `"],"labels":{"module":"http_2xx","service":"` + service + `","severity":"critical","failure_domain":"` + service + `"}` + extra + `}`
	}
	front, back := "https://front.example.com/", "https://back.example.com/"
	config := "/api/v1/config"
	path := config + "/targets/all-web"
	if code, out := c.do("PUT", config+"/modules/http_2xx", configWriter, `{"prober":"http"}`); code != 200 {
		t.Fatalf("module: %d %v", code, out)
	}
	if code, out := c.do("PUT", path, writer, "["+group("front", front, binding)+","+group("back", back, "")+"]"); code != 200 {
		t.Fatalf("designating app-1 for front: %d %v", code, out)
	}
	// designated is the service app-1 answers for
	designated := func() string {
		t.Helper()
		_, result := c.raw("GET", path, configWriter)
		var groups []targets.Group
		if err := json.Unmarshal([]byte(result), &groups); err != nil {
			t.Fatalf("%v: %s", err, result)
		}
		for _, g := range groups {
			if g.Machine != nil && g.Machine.Name == "app-1" {
				return g.Labels["service"]
			}
		}
		return ""
	}

	// the machine stays first in the file, its group now back's: app-1 answers for back
	swapped := "[" + group("back", back, binding) + "," + group("front", front, "") + "]"
	if code, out := c.do("PUT", path, configWriter, swapped); code != 403 || out["code"] != "auth.scope" {
		t.Errorf("config:write alone moves app-1 to another service: %d %v", code, out)
	}
	if code, out := c.do("PUT", path, named, swapped); code != 403 || out["code"] != "auth.scope" || !strings.Contains(out["error"].(string), "all machines") {
		t.Errorf("a credential that names its machines moves a response machine: %d %v", code, out)
	}
	if d := designated(); d != "front" {
		t.Errorf("a refused change moved the designation: %s", d)
	}
	// moved within the file, unchanged: no designation changes
	if code, out := c.do("PUT", path, configWriter, "["+group("back", back, "")+","+group("front", front, binding)+"]"); code != 200 {
		t.Errorf("config:write moves a bound group within its file: %d %v", code, out)
	}
	// the dedicated route takes access to all machines too
	_, cfg := c.do("GET", config+"/targets", configWriter, "")
	version := strconv.Itoa(int(cfg["version"].(float64)))
	if code, out := c.do("PUT", path+"/groups/1/machine", named, `{"clear":true,"if_version":`+version+`}`); code != 403 || out["code"] != "auth.scope" {
		t.Errorf("a credential that names its machines clears a response machine: %d %v", code, out)
	}
	if d := designated(); d != "front" {
		t.Errorf("a refused clear changed the designation: %s", d)
	}
	if code, out := c.do("PUT", path, writer, swapped); code != 200 {
		t.Errorf("a credential with all machines moves the designation: %d %v", code, out)
	}
	if d := designated(); d != "back" {
		t.Errorf("the designation did not move: %s", d)
	}
}
