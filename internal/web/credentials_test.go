package web

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/store"
)

const fixtureTeamToken = "fixture-team-bearer-token"

func setupTeamCredentialWeb(t *testing.T, lang i18n.Lang) (*env, string, string) {
	t.Helper()
	e := newEnv(t, lang)
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fixtureTeamToken || r.URL.Path != "/ready" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-Reflected-Value", fixtureTeamToken)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fixtureTeamToken))
	}))
	t.Cleanup(up.Close)
	host, _ := url.Parse(up.URL)
	roots := x509.NewCertPool()
	roots.AddCert(up.Certificate())
	anchor := filepath.Join(t.TempDir(), "anchor")
	c, err := secrets.Open(bytes.Repeat([]byte{0x71}, 32), secrets.Deps{
		Store: e.st, AnchorDir: anchor, AllowedHosts: []string{host.Host}, RootCAs: roots,
		Lang: lang, Now: func() time.Time { return e.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Credentials = c
	e.mux = http.NewServeMux()
	e.srv.Register(e.mux)
	(&api.Server{Bridge: e.b, Auth: e.srv.Auth, Credentials: c}).Register(e.mux)
	return e, up.URL + "/ready", anchor
}

func TestConsoleCredentialAnchorPendingAndBootstrapUnavailable(t *testing.T) {
	e, endpoint, anchor := setupTeamCredentialWeb(t, i18n.EN)
	owner := e.owner()
	if err := os.Mkdir(filepath.Join(anchor, "epochs.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	const index = "/console/credentials"
	p := owner.post(index, url.Values{"name": {"Billing API"}, "endpoint": {endpoint}, "value": {fixtureTeamToken}})
	if p.code != http.StatusServiceUnavailable || p.header.Get("Location") != "" ||
		!strings.Contains(p.body, "Credential change is awaiting verification") || strings.Contains(p.body, fixtureTeamToken) {
		t.Fatalf("unanchored mutation showed the wrong result: %d", p.code)
	}
	// Simulate startup refusing an unverified vault. The rest of the console can still serve.
	e.srv.Credentials, e.srv.CredentialsConfigured = nil, true
	e.mux = http.NewServeMux()
	e.srv.Register(e.mux)
	if p := owner.get(index); p.code != http.StatusServiceUnavailable || !strings.Contains(p.body, "The team credential store cannot be verified.") {
		t.Fatalf("missing startup vault did not explain its status: %d", p.code)
	}
	if p := owner.get("/console"); p.code != http.StatusOK {
		t.Fatalf("unavailable credentials impacted monitoring pages: %d", p.code)
	}
}

func TestConsoleTeamCredentialWorkflow(t *testing.T) {
	e, endpoint, _ := setupTeamCredentialWeb(t, i18n.EN)
	owner := e.owner()
	reader := e.invite(owner, "read-only", "viewer")
	if a := reader.get("/console/credentials"); a.code != http.StatusForbidden {
		t.Fatalf("viewer can open credentials: %d", a.code)
	}
	if _, err := e.st.CreateAgent(store.Agent{Name: "duty-agent", Owner: "alice", Preset: store.PresetCustom,
		Scopes: []string{"status:read", secrets.UseScope}, CreatedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	token, _, err := e.st.CreateAgentToken("duty-agent", time.Hour, "alice")
	if err != nil {
		t.Fatal(err)
	}
	const index = "/console/credentials"
	p := owner.get(index)
	if p.code != http.StatusOK || !strings.Contains(p.body, `type="password" name="value"`) {
		t.Fatalf("the vault does not offer a write-only field: %d", p.code)
	}
	if p := owner.do(http.MethodPost, index, url.Values{"value": {fixtureTeamToken}}); p.code != http.StatusForbidden {
		t.Fatalf("creation without CSRF: %d", p.code)
	}
	p = owner.post(index, url.Values{"name": {"Billing API"}, "endpoint": {endpoint}, "value": {fixtureTeamToken}})
	if p.code != http.StatusSeeOther || !strings.HasPrefix(p.header.Get("Location"), index+"/") {
		t.Fatalf("creation did not open the credential: %d %q", p.code, p.header.Get("Location"))
	}
	page := strings.SplitN(p.header.Get("Location"), "?", 2)[0]
	id := strings.TrimPrefix(page, index+"/")
	user := e.browser()
	user.headers["Authorization"] = "Bearer " + token
	apiPath := "/api/v1/team-credentials/" + id + "/use"
	if a := user.do(http.MethodPost, apiPath, nil); a.code != http.StatusForbidden {
		t.Fatalf("agent used a credential before being granted access: %d", a.code)
	}
	if a := owner.get(page); a.code != http.StatusOK || strings.Contains(a.body, fixtureTeamToken) || !strings.Contains(a.body, endpoint) {
		t.Fatalf("saved value escaped to the UI or detail not shown: %d", a.code)
	}
	if a := owner.post(page+"/test", nil); a.code != http.StatusSeeOther || a.header.Get("Location") != page+"?status=200" {
		t.Fatalf("owner's authenticated request failed: %d %q", a.code, a.header.Get("Location"))
	}
	grant := url.Values{"agent": {"duty-agent"}, "version": {"1"}, "hours": {"8"}}
	if a := owner.post(page+"/grant", grant); a.code != http.StatusSeeOther || !strings.HasSuffix(a.header.Get("Location"), "?done=saved") {
		t.Fatalf("grant from the console: %d %q", a.code, a.header.Get("Location"))
	}
	grant.Set("version", "2")
	grant.Set("hours", "1")
	if a := owner.post(page+"/grant", grant); a.code != http.StatusSeeOther || !strings.HasSuffix(a.header.Get("Location"), "?done=saved") {
		t.Fatalf("shortening from the console: %d %q", a.code, a.header.Get("Location"))
	}
	a := user.do(http.MethodPost, apiPath, nil)
	var result secrets.Result
	if err := json.Unmarshal([]byte(a.body), &result); err != nil || a.code != http.StatusOK || result.Status != http.StatusOK || strings.Contains(a.body, fixtureTeamToken) {
		t.Fatalf("agent did not receive the status-only response: %d, status %d, err %v", a.code, result.Status, err)
	}
	if a := owner.post(page+"/revoke", url.Values{"version": {"3"}, "agent": {"duty-agent"}}); a.code != http.StatusSeeOther ||
		!strings.HasSuffix(a.header.Get("Location"), "?done=saved") {
		t.Fatalf("revocation from the console: %d %q", a.code, a.header.Get("Location"))
	}
	if a := user.do(http.MethodPost, apiPath, nil); a.code != http.StatusForbidden {
		t.Fatalf("agent kept access after revocation: %d", a.code)
	}
	a = owner.get("/console/audit?q=" + id)
	if a.code != http.StatusOK || !strings.Contains(a.body, "alice / duty-agent") || !strings.Contains(a.body, "credential.use") ||
		!strings.Contains(a.body, "credential.revoke") || strings.Contains(a.body, fixtureTeamToken) {
		t.Fatalf("audit missing safe provenance: %d", a.code)
	}
}
