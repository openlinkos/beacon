package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

func TestMachinesSearch(t *testing.T) {
	e := newEnv(t, i18n.EN)
	b := e.owner()
	p := b.get("/console/machines?q=EDGE-1")
	if p.code != http.StatusOK || !strings.Contains(p.body, `href="/console/machines/edge-1"`) || strings.Contains(p.body, `href="/console/machines/edge-2"`) {
		t.Fatal("the filtered fleet must show only the matching machine")
	}
	if !strings.Contains(p.body, `<details class="bc-machine-add">`) || !strings.Contains(p.body, `action="/console/machines/pause"`) {
		t.Error("registration should be folded and the global pause action available")
	}
	p = b.get("/console/machines?q=no-such-machine")
	if p.code != http.StatusOK || !strings.Contains(p.body, "Nothing matches.") {
		t.Error("an empty search should offer a way to clear the filter")
	}
}

func TestAgentMachineChoice(t *testing.T) {
	e := newEnv(t, i18n.EN)
	b := e.owner()
	path := "/console/agents/access-bot/settings"
	create := url.Values{
		"name": {"access-bot"}, "preset": {"oncall"}, "expires": {"90d"},
		"machine_access_form": {"1"}, "machine_access": {"selected"}, "machine": {"edge-1"},
	}
	if a := b.post("/console/agents", create); a.code != http.StatusCreated {
		t.Fatalf("create limited agent: %d %s", a.code, a.header.Get("Location"))
	}
	for _, c := range []struct {
		modes, machines []string
		problem         string
	}{
		{nil, nil, "machine_access"},
		{[]string{"all", "selected"}, []string{"edge-1"}, "machine_access"},
		{[]string{"selected"}, nil, "machine_selection"},
		{[]string{"selected"}, []string{"missing"}, "machine_unknown"},
	} {
		a := b.post(path, url.Values{"machine_access_form": {"1"}, "machine_access": c.modes, "machine": c.machines})
		if a.code != http.StatusSeeOther || !strings.Contains(a.header.Get("Location"), "error="+c.problem) {
			t.Errorf("%q: expected %s, got %d %s", c.modes, c.problem, a.code, a.header.Get("Location"))
		}
		if ag := lockAgent(t, e, "access-bot"); !slices.Equal(ag.Machines, []string{"edge-1"}) {
			t.Errorf("invalid machine choice changed access to %v", ag.Machines)
		}
	}
	if err := e.st.DeleteMachine("edge-1"); err != nil {
		t.Fatal(err)
	}
	p := b.get("/console/agents/access-bot")
	if !strings.Contains(p.body, `name="machine" value="edge-1" checked`) || !strings.Contains(p.body, "Not registered") {
		t.Error("the editor should expose the already-saved machine restriction after removal")
	}
	if a := b.post(path, url.Values{"machine_access": {"selected"}, "machine": {"edge-1"}}); !strings.Contains(a.header.Get("Location"), "done=saved") {
		t.Errorf("saving an unchanged restriction: %s", a.header.Get("Location"))
	}
	if a := b.post(path, url.Values{"machine_access": {"all"}}); !strings.Contains(a.header.Get("Location"), "done=saved") {
		t.Errorf("explicit all-machines grant: %s", a.header.Get("Location"))
	}
	if ag := lockAgent(t, e, "access-bot"); len(ag.Machines) != 0 {
		t.Errorf("all-machines grant still restricted: %v", ag.Machines)
	}
}

func TestReservedAgentScopeInConsole(t *testing.T) {
	e := newEnv(t, i18n.EN)
	p := lockSetup(t, e)
	const name = "old-bot"
	_, err := e.st.CreateAgent(store.Agent{Name: name, Owner: "alice", Preset: store.PresetCustom, Scopes: []string{"status:read", reservedAgentScope}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/console/agents/" + name
	if a := p.alice.get(path); strings.Contains(a.body, `value="`+reservedAgentScope+`"`) {
		t.Error("the editor offered a reserved permission")
	}
	if a := p.alice.post(path+"/settings", url.Values{"preset": {store.PresetCustom}, "scopes": {"status:read"}, "machine_access": {"all"}}); !strings.Contains(a.header.Get("Location"), "done=saved") {
		t.Errorf("save existing custom grant: %s", a.header.Get("Location"))
	}
	if ag := lockAgent(t, e, name); !slices.Contains(ag.Scopes, reservedAgentScope) {
		t.Error("saving silently discarded the old reserved permission")
	}
}
