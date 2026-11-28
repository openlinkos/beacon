package oauth_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestRefreshPassesLockout checks that during a lockout of the client's address, a refresh token Beacon
// issued to a client there still refreshes, and the access token it gets is still taken; while an unknown
// refresh token, or a code, gets the lockout unread.
func TestRefreshPassesLockout(t *testing.T) {
	b := start(t)
	redirect := "http://127.0.0.1:41000/callback"
	_, reg := register(t, b, `{"redirect_uris": ["`+redirect+`"], "client_name": "Locked client"}`)
	client := reg["client_id"].(string)
	br := b.browser()
	br.signIn("bob")
	verifier := strings.Repeat("v", 50)
	q := authorizeQuery(client, redirect, "s", challenge(verifier), "oncall")
	to, _ := br.consent("/oauth/authorize?"+q.Encode(), url.Values{"agent": {"locked-client"}})
	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {to.Query().Get("code")}, "redirect_uri": {redirect}, "client_id": {client}, "code_verifier": {verifier}}
	code, tok := token(t, b, exchange)
	if code != http.StatusOK {
		t.Fatalf("exchange: %d %v", code, tok)
	}
	refresh := tok["refresh_token"].(string)

	// someone at the same address keeps failing
	locked := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	for range 20 {
		b.oauth.Auth.Fail(locked)
	}
	if !b.oauth.Auth.Blocked(locked) {
		t.Fatal("the address is not locked out")
	}

	if code, out := token(t, b, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"lbr_unknown"}, "client_id": {client}}); code != http.StatusTooManyRequests || out["error"] != "too_many_requests" {
		t.Errorf("an unknown refresh token during the lockout: %d %v", code, out)
	}
	to, _ = br.consent("/oauth/authorize?"+q.Encode(), url.Values{"agent": {"locked-client"}})
	exchange.Set("code", to.Query().Get("code"))
	if code, out := token(t, b, exchange); code != http.StatusTooManyRequests {
		t.Errorf("a code during the lockout: %d %v", code, out)
	}

	code, next := token(t, b, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {client}})
	if code != http.StatusOK {
		t.Fatalf("the client's refresh token during the lockout: %d %v", code, next)
	}
	// and again with the one it got, then the new access token on the REST API
	code, next = token(t, b, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {next["refresh_token"].(string)}, "client_id": {client}})
	if code != http.StatusOK {
		t.Fatalf("the next refresh during the lockout: %d %v", code, next)
	}
	req, _ := http.NewRequest(http.MethodGet, b.url+"/api/v1/token", nil)
	req.Header.Set("Authorization", "Bearer "+next["access_token"].(string))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("the refreshed access token during the lockout: %d", res.StatusCode)
	}
	// a refresh token already replaced is still refused, and ends the grant
	if code, out := token(t, b, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {client}}); code != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Errorf("a replaced refresh token during the lockout: %d %v", code, out)
	}
}
