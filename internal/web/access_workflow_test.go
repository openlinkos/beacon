package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/config"
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

func TestPersonalMachineConnection(t *testing.T) {
	e := newEnv(t, i18n.EN)
	owner := e.owner()
	reader := e.invite(owner, "reader", "viewer")
	const one = "/console/machines/edge-1"
	const link = one + "#ssh-connect"
	const command = "ssh -F ~/.ssh/beacon-personal.conf beacon-edge-1"
	if a := reader.get(one); strings.Contains(a.body, `id="ssh-connect"`) {
		t.Error("a viewer with no machine access was invited to connect")
	}
	if a := owner.get("/console/machines"); !strings.Contains(a.body, link) || strings.Contains(a.body, "/console/machines/edge-2#ssh-connect") {
		t.Error("fleet should offer SSH for the reachable machine, while the disconnected NAT machine stays listed")
	}
	if a := owner.get(one); !strings.Contains(a.body, `/console/account#ssh-keys`) {
		t.Error("an account without a key should be directed to register one")
	}
	if a := owner.post("/console/account/keys", url.Values{"key": {lockPubKey(t)}}); !strings.Contains(a.header.Get("Location"), "done=key_added") {
		t.Fatalf("add personal SSH key: %d %s", a.code, a.header.Get("Location"))
	}
	if a := owner.get(one); !strings.Contains(a.body, "ssh.public_address") || strings.Contains(a.body, command) {
		t.Error("without an SSH endpoint, the page should explain what the owner needs to configure")
	}
	e.srv.Bridge = bridge.New(bridge.Deps{Store: e.st, Config: &config.Config{Location: time.UTC}, SSHConfig: func(identity, machine string) string {
		if identity != "user:alice" || machine != "edge-1" {
			t.Errorf("SSH snippet requested for %q and %q", identity, machine)
		}
		return "# ~/.ssh/beacon-personal.conf\nHost beacon-edge-1\n  HostName beacon.example.com"
	}})
	if a := owner.get(one); !strings.Contains(a.body, command) || !strings.Contains(a.body, "HostName beacon.example.com") || !strings.Contains(a.body, "records the session as alice") {
		t.Error("an eligible account should see the command, client setup and audit identity")
	}
	personal, _ := e.st.Session(sessionID)
	personal.Agent, personal.Principal, personal.Via = "alice", "user:alice", "ssh"
	if err := e.st.PutSession(personal); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{one, "/console/sessions?agent=alice"} {
		page := owner.get(path)
		if page.code != http.StatusOK || !strings.Contains(page.body, `<th scope="col">Who</th>`) || !strings.Contains(page.body, `>Person · SSH</span>`) {
			t.Errorf("%s did not present alice as a person in the session table", path)
		}
	}
	if a := owner.post("/console/machines/pause", nil); !strings.Contains(a.header.Get("Location"), "done=paused") {
		t.Fatalf("pause SSH access: %d %s", a.code, a.header.Get("Location"))
	}
	if a := owner.get(one); strings.Contains(a.body, command) || !strings.Contains(a.body, "SSH access is paused") {
		t.Error("during a pause the detail should explain the state and withhold the connection command")
	}
	if a := owner.get("/console/machines"); strings.Contains(a.body, link) {
		t.Error("during a pause the fleet should withhold the connection action")
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
