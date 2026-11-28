package oauth_test

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

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
