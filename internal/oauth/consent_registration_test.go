package oauth_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestConsentPageKeepsRegistration checks that opening a registered client's consent page records the
// sign-in under way, which keeps the registration from making room for others while the person answers.
func TestConsentPageKeepsRegistration(t *testing.T) {
	b := start(t)
	redirect := "http://127.0.0.1:41000/callback"
	_, reg := register(t, b, `{"redirect_uris": ["`+redirect+`"], "client_name": "Slow reader"}`)
	client := reg["client_id"].(string)
	if c, ok := b.env.Store.OAuthClient(client); !ok || !c.Asked.IsZero() {
		t.Fatalf("a new registration: %+v %v", c, ok)
	}
	br := b.browser()
	br.signIn("bob")
	q := authorizeQuery(client, redirect, "s", challenge(strings.Repeat("v", 50)), "oncall")
	if code, page, _ := br.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil); code != http.StatusOK {
		t.Fatalf("consent page: %d %s", code, page)
	}
	if c, ok := b.env.Store.OAuthClient(client); !ok || c.Asked.IsZero() {
		t.Errorf("the consent page was not recorded: %+v %v", c, ok)
	}
}
