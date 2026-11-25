package web

import (
	"bytes"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/web/link"
)

func TestSessionDetailsRespectTheCallersScope(t *testing.T) {
	e := newEnv(t, i18n.EN)
	apiServer := api.Server{Auth: e.srv.Auth, Bridge: e.b, SSH: e.srv.SSH}
	apiServer.Register(e.mux)
	rec, ok := e.st.Session(sessionID)
	if !ok {
		t.Fatal("fixture session is missing")
	}
	const fixture = "fixture command marker"
	rec.Command = fixture
	if err := e.st.PutSession(rec); err != nil {
		t.Fatal(err)
	}
	metadata := e.token("metadata", "status:read")
	detail := e.token("session-detail", "status:read", "sessions:read")
	for _, path := range []string{"/api/v1/sessions", "/api/v1/alerts/" + e.id + "/evidence", "/api/v1/alerts/" + e.id + "/timeline"} {
		code, payload, _ := e.get(path, metadata)
		if code != http.StatusOK || !strings.Contains(payload, sessionID) || strings.Contains(payload, fixture) || strings.Contains(payload, link.SessionPath(sessionID)+"?s=") {
			t.Errorf("%s did not keep command text and signed playback within the session scope: %d", path, code)
		}
		code, payload, _ = e.get(path, detail)
		if code != http.StatusOK || !strings.Contains(payload, fixture) || !strings.Contains(payload, link.SessionPath(sessionID)+"?s=") {
			t.Errorf("%s did not retain session text and playback for an authorized credential: %d", path, code)
		}
	}
}

func TestOwnerRevokesOutstandingSessionLinks(t *testing.T) {
	e := newEnv(t, i18n.EN)
	legacy := e.signer
	var err error
	e.signer, err = link.NewRevocableSigner(bytes.Repeat([]byte{1}, link.KeySize), time.Hour,
		func() time.Time { return e.now }, e.st.PageLinkGeneration)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Pages.Signer = e.signer
	owner := e.owner()
	reader := e.invite(owner, "reader", "viewer")
	path := link.SessionPath(sessionID)
	management := shareLinksPath("sessions", sessionID)
	if a := e.browser().get(management); a.code != http.StatusSeeOther {
		t.Fatalf("signed-out management: %d", a.code)
	}
	if a := reader.get(management); a.code != http.StatusForbidden {
		t.Fatalf("read-only share management: %d", a.code)
	}
	if a := reader.post(management, url.Values{"intent": {"revoke"}}); a.code != http.StatusForbidden {
		t.Fatalf("read-only revocation: %d", a.code)
	}
	if a := owner.get(path); a.code != http.StatusOK || len(hrefs(a.body, management)) == 0 {
		t.Fatalf("owner's session has no link management: %d", a.code)
	}
	if a := owner.get(management); a.code != http.StatusOK || strings.Contains(a.body, `id="issued-share-url"`) {
		t.Fatalf("a management read issued a link: %d", a.code)
	}
	issue := func() string {
		t.Helper()
		a := owner.post(management, url.Values{"intent": {"create"}})
		pattern := regexp.MustCompile(`https://beacon\.example\.com/console/sessions/` + sessionID + `\?s=[A-Za-z0-9._-]+`)
		found := pattern.FindString(html.UnescapeString(a.body))
		if a.code != http.StatusOK || a.header.Get("Cache-Control") != "private, no-store" || found == "" {
			t.Fatalf("issue share: %d", a.code)
		}
		return strings.TrimPrefix(found, publicURL)
	}
	shared := issue()
	anon := e.browser()
	a := anon.get(shared)
	recordings := hrefs(a.body, link.RecordingPath(sessionID)+"?s=")
	if a.code != http.StatusOK || len(recordings) == 0 || len(hrefs(a.body, management)) != 0 {
		t.Fatalf("anonymous replay or scope: %d", a.code)
	}
	old := "/sessions/" + sessionID
	old += "?" + legacy.Query(old)
	redirect := anon.get(old)
	if redirect.code != http.StatusPermanentRedirect || !strings.Contains(redirect.header.Get("Location"), "?s=") ||
		anon.get(redirect.header.Get("Location")).code != http.StatusOK {
		t.Fatalf("legacy link stopped too early: %d", redirect.code)
	}
	other := e.link(link.AlertPath(e.id))
	if a := owner.post(management, url.Values{"intent": {"revoke"}}); a.code != http.StatusSeeOther || a.header.Get("Location") != management+"?done=shared_links_revoked" {
		t.Fatalf("revocation: %d", a.code)
	}
	for _, removed := range []string{shared, recordings[0], redirect.header.Get("Location")} {
		if a := anon.get(removed); a.code != http.StatusNotFound {
			t.Fatalf("revoked link still opens: %d", a.code)
		}
	}
	if a := anon.get(old); a.code != http.StatusPermanentRedirect || a.header.Get("Location") != path {
		t.Fatalf("old path re-issued a revoked credential: %d", a.code)
	}
	if a := anon.get(other); a.code != http.StatusOK {
		t.Fatalf("revocation affected another resource: %d", a.code)
	}
	if a := owner.get(link.RecordingPath(sessionID)); a.code != http.StatusOK {
		t.Fatalf("revocation affected authenticated access: %d", a.code)
	}
	if recreated := issue(); recreated == shared || anon.get(recreated).code != http.StatusOK {
		t.Fatal("a share created after revocation should work with a new URL")
	}
	if !e.bot.told("revoked share links", "session/"+sessionID) || e.bot.told(shared) {
		t.Fatal("the chat should show the revocation without the bearer URL")
	}
}
