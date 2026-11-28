package oauth_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestEndedSignInRetriesDoNotLockOut checks that once a person's new password ends their agents' OAuth
// sign-ins, the client retrying its refresh token at the token endpoint and its access token on the API
// is refused without its retries counting as guesses, so the person's own address is not locked out;
// while an unknown refresh token still counts.
func TestEndedSignInRetriesDoNotLockOut(t *testing.T) {
	b := start(t)
	redirect := "http://127.0.0.1:41000/callback"
	_, reg := register(t, b, `{"redirect_uris": ["`+redirect+`"], "client_name": "Retrying client"}`)
	client := reg["client_id"].(string)
	br := b.browser()
	br.signIn("bob")
	verifier := strings.Repeat("v", 50)
	q := authorizeQuery(client, redirect, "s", challenge(verifier), "oncall")
	to, _ := br.consent("/oauth/authorize?"+q.Encode(), url.Values{"agent": {"retrying-client"}})
	code, tok := token(t, b, url.Values{"grant_type": {"authorization_code"}, "code": {to.Query().Get("code")}, "redirect_uri": {redirect}, "client_id": {client}, "code_verifier": {verifier}})
	if code != http.StatusOK {
		t.Fatalf("exchange: %d %v", code, tok)
	}
	refresh, access := tok["refresh_token"].(string), tok["access_token"].(string)

	if err := b.env.Store.SetPassword("bob", "a new password for bob", ""); err != nil { // ends the sign-in
		t.Fatal(err)
	}
	here := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	for i := range 30 {
		if code, out := token(t, b, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {client}}); code != http.StatusBadRequest || out["error"] != "invalid_grant" {
			t.Fatalf("refresh %d of the ended sign-in: %d %v", i, code, out)
		}
		req, _ := http.NewRequest(http.MethodGet, b.url+"/api/v1/token", nil)
		req.Header.Set("Authorization", "Bearer "+access)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("access %d of the ended sign-in: %d", i, res.StatusCode)
		}
	}
	if b.oauth.Auth.Blocked(here) {
		t.Fatal("the ended sign-in's retries locked its address out")
	}

	for range 20 {
		token(t, b, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"lbr_unknown"}, "client_id": {client}})
	}
	if !b.oauth.Auth.Blocked(here) {
		t.Error("unknown refresh tokens no longer count as guesses")
	}
}
