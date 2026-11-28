package oauth_test

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// TestConsentMachines: on the consent page, only these with nothing ticked is asked again, never taken
// for all; a machine must be one in the list; critical machines are offered to an owner alone, unticked,
// and ticking one takes a confirmation; the sign-in reaches a critical machine for 30 days at most.
func TestConsentMachines(t *testing.T) {
	b := start(t)
	st := b.env.Store
	for _, m := range []store.Machine{{Name: "box", Address: "box.example.com:22", User: "beacon"}, {Name: "ca", Address: "ca.example.com:22", User: "beacon", Critical: true}} {
		if err := st.PutMachine(m); err != nil {
			t.Fatal(err)
		}
	}
	redirect := "http://127.0.0.1:41000/callback"
	_, reg := register(t, b, `{"redirect_uris": ["`+redirect+`"], "client_name": "Fixture", "grant_types": ["authorization_code", "refresh_token"]}`)
	client := reg["client_id"].(string)
	verifier := strings.Repeat("v", 50)
	q := authorizeQuery(client, redirect, "s", challenge(verifier), "")
	form := func(br *browser) (url.Values, string) {
		t.Helper()
		_, page, _ := br.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
		f := url.Values{}
		for _, m := range hiddenRe.FindAllStringSubmatch(page, -1) {
			f.Set(m[1], html.UnescapeString(m[2]))
		}
		f.Set("decision", "allow")
		f.Set("duration", "1")
		f.Set("preset", "oncall")
		return f, page
	}
	asked := func(br *browser, f url.Values, what, want string) {
		t.Helper()
		code, body, hd := br.do(http.MethodPost, "/oauth/authorize", f)
		if code != http.StatusBadRequest || hd.Get("Location") != "" || !strings.Contains(body, html.EscapeString(want)) {
			t.Errorf("%s: %d %q", what, code, hd.Get("Location"))
		}
	}

	// bob, on call: no critical machine is offered, and none may be ticked
	bob := b.browser()
	bob.signIn("bob")
	f, page := form(bob)
	if strings.Contains(page, `name="critical"`) || strings.Contains(page, `value="ca"`) {
		t.Errorf("a critical machine is offered to someone who is not an owner")
	}
	f.Set("agent", "bob-client")
	only := url.Values{}
	for k, v := range f {
		only[k] = v
	}
	only.Set("machines", "some")
	asked(bob, only, "only these with nothing ticked", "Tick at least one machine")
	only.Set("machine", "elsewhere")
	asked(bob, only, "a machine that is not enrolled", "Choose machines from the list.")
	only.Set("machine", "ca")
	asked(bob, only, "a critical machine in the list", "Choose machines from the list.")
	only.Del("machine")
	only.Set("critical", "ca")
	only.Set("critical_confirm", "yes")
	only.Set("machine", "box")
	asked(bob, only, "a critical machine ticked by someone who is not an owner", "Only an owner")
	if _, ok := st.Agent("bob-client"); ok {
		t.Fatal("a refused consent made an agent")
	}
	f.Del("machines")
	asked(bob, f, "no machine choice", "Choose which machines it reaches.")

	// alice, an owner: the critical machine is offered apart and unticked, and takes the confirmation
	alice := b.browser()
	alice.signIn("alice")
	f, page = form(alice)
	if !strings.Contains(page, `name="critical" value="ca">`) || strings.Contains(page, `name="critical" value="ca" checked`) {
		t.Errorf("the owner's page does not offer the critical machine apart and unticked")
	}
	f.Set("agent", "alice-client")
	f.Set("machines", "some")
	f.Set("machine", "box")
	f.Set("critical", "ca")
	asked(alice, f, "a critical machine without the confirmation", "Confirm that this client may reach the critical machines")
	f.Set("critical_confirm", "yes")
	code, _, hd := alice.do(http.MethodPost, "/oauth/authorize", f)
	if code != http.StatusSeeOther || !strings.HasPrefix(hd.Get("Location"), redirect) {
		t.Fatalf("an owner allows a critical machine: %d %q", code, hd.Get("Location"))
	}
	to, _ := url.Parse(hd.Get("Location"))
	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {to.Query().Get("code")}, "redirect_uri": {redirect}, "client_id": {client}, "code_verifier": {verifier}}
	_, tok := token(t, b, exchange)
	p, ok := st.Authenticate(tok["access_token"].(string))
	if !ok || !p.Reaches("box") || !p.Reaches("ca") || p.IsAll() {
		t.Fatalf("the sign-in: %+v %v", p, ok)
	}
	if until := p.Until("ca"); until.IsZero() || until.After(time.Now().Add(store.CriticalGrantTTL+time.Minute)) {
		t.Errorf("the critical machine's access ends %v", until)
	}
}

// TestConsentReusedAgentMachines: a reused agent's sign-in reaches a critical machine for as long as the
// agent does, when that is less than 30 days, rather than being refused; and a reused agent that reaches
// no machine that is not critical is allowed with its critical machines ticked, or with none.
func TestConsentReusedAgentMachines(t *testing.T) {
	b := start(t)
	st := b.env.Store
	ctx := context.Background()
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	for _, m := range []store.Machine{{Name: "box", Address: "box.example.com:22", User: "beacon"}, {Name: "ca", Address: "ca.example.com:22", User: "beacon", Critical: true}} {
		if err := st.PutMachine(m); err != nil {
			t.Fatal(err)
		}
	}
	soon := time.Now().Add(7 * 24 * time.Hour).UTC().Truncate(time.Second)
	ends := map[string]time.Time{"ca": soon}
	if _, err := b.env.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "wide", Owner: "alice", Preset: "oncall",
		Machines: store.MachineAccess{Access: store.AccessAll, Machines: []string{"ca"}, MachineUntil: ends}}); err != nil {
		t.Fatal(err)
	}
	redirect := "http://127.0.0.1:41000/callback"
	_, reg := register(t, b, `{"redirect_uris": ["`+redirect+`"], "client_name": "Fixture", "grant_types": ["authorization_code", "refresh_token"]}`)
	client := reg["client_id"].(string)
	if _, err := b.env.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "ca-only", Owner: "alice", Preset: "oncall",
		Machines: store.MachineAccess{Access: store.AccessNamed, Machines: []string{"ca"}, MachineUntil: ends}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateAgent("ca-only", func(a *store.Agent) error { a.Client = client; return nil }); err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("v", 50)
	q := authorizeQuery(client, redirect, "s", challenge(verifier), "")
	alice := b.browser()
	alice.signIn("alice")
	allow := func(what string, f url.Values) store.Principal {
		t.Helper()
		code, body, hd := alice.do(http.MethodPost, "/oauth/authorize", f)
		if code != http.StatusSeeOther || !strings.HasPrefix(hd.Get("Location"), redirect) {
			t.Fatalf("%s: %d %q %s", what, code, hd.Get("Location"), body)
		}
		to, _ := url.Parse(hd.Get("Location"))
		_, tok := token(t, b, url.Values{"grant_type": {"authorization_code"}, "code": {to.Query().Get("code")}, "redirect_uri": {redirect}, "client_id": {client}, "code_verifier": {verifier}})
		p, ok := st.Authenticate(tok["access_token"].(string))
		if !ok {
			t.Fatalf("%s: the sign-in does not work", what)
		}
		return p
	}

	_, page, _ := alice.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	form := func() url.Values {
		f := url.Values{}
		for _, m := range hiddenRe.FindAllStringSubmatch(page, -1) {
			f.Set(m[1], html.UnescapeString(m[2]))
		}
		f.Set("decision", "allow")
		f.Set("duration", "1")
		f.Set("preset", "oncall")
		f.Set("critical", "ca")
		f.Set("critical_confirm", "yes")
		return f
	}

	// an agent reaching all machines and a critical one for a week: the sign-in reaches it for that week
	f := form()
	f.Set("agent", "wide")
	f.Set("machines", "all")
	p := allow("a critical machine ending within 30 days", f)
	if !p.Reaches("ca") || !p.Reaches("box") || p.Until("ca").After(soon) || p.Until("ca").IsZero() {
		t.Errorf("a sign-in of an agent whose critical access ends soon: %s", p.String())
	}

	// the agent made for this client reaches only a critical machine: the page says so and is answered
	if !strings.Contains(page, `name="machines" value="none"`) || !strings.Contains(page, "reaches no machine that is not critical") || !strings.Contains(page, `name="critical" value="ca">`) {
		t.Fatalf("the consent page for an agent with only a critical machine: %s", page)
	}
	f = form()
	f.Set("agent", "ca-only")
	p = allow("the critical machine ticked", f)
	if !p.Reaches("ca") || p.Reaches("box") || p.Until("ca").After(soon) || p.Until("ca").IsZero() {
		t.Errorf("a sign-in with the critical machine: %s", p.String())
	}
	f.Del("critical")
	f.Del("critical_confirm")
	if p := allow("no machine ticked", f); !p.NoMachines() || p.Has("machines:ssh") {
		t.Errorf("a sign-in with no machine reaches %s", p.String())
	}
	for _, g := range st.Grants("wide") {
		if !g.Until("ca").Equal(soon) {
			t.Errorf("the grant's end for the critical machine: %v, the agent's %v", g.Until("ca"), soon)
		}
	}
}
