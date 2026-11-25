package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/web/link"
)

func TestWorkspacePageAccess(t *testing.T) {
	e := newEnv(t, i18n.EN)
	const root = "/console/api/v1/page"
	owner := e.owner()
	reader := e.invite(owner, "workspace-page-reader", "viewer")
	if a := reader.get(root + "/people"); a.code != http.StatusForbidden {
		t.Fatalf("viewer people page: %d", a.code)
	}
	owner.headers["Sec-Fetch-Site"] = "cross-site"
	if a := owner.get(root + "/people"); a.code != http.StatusForbidden {
		t.Fatalf("cross-site people page: %d", a.code)
	}
	delete(owner.headers, "Sec-Fetch-Site")
	if a := owner.get(root + "/people"); a.code != http.StatusOK ||
		!strings.Contains(a.header.Get("Content-Type"), "application/json") ||
		!strings.Contains(a.header.Get("Cache-Control"), "no-store") ||
		strings.Contains(a.body, owner.cookies[sessionCookie].Value) {
		t.Fatalf("owner people page: %d", a.code)
	}
	if a := owner.post(root+"/people/invite", url.Values{"csrf": {"wrong"}, "role": {"viewer"}}); a.code != http.StatusForbidden {
		t.Fatalf("unguarded invitation: %d", a.code)
	}
	signedSession := strings.Replace(e.link(link.SessionPath(sessionID)), "/console/", root+"/", 1)
	if a := e.browser().get(signedSession); a.code != http.StatusOK ||
		!strings.Contains(a.header.Get("Content-Type"), "application/json") ||
		!strings.Contains(a.body, link.RecordingPath(sessionID)+"?s=") {
		t.Fatalf("signed replay page: %d", a.code)
	}
}

func TestWorkspaceSessionAndIncidentMutations(t *testing.T) {
	e := newEnv(t, i18n.EN)
	const root = "/console/api/v1"
	if a := e.browser().get(root + "/session"); a.code != http.StatusUnauthorized {
		t.Fatalf("anonymous workspace session status %d", a.code)
	}

	owner := e.owner()
	duty := e.invite(owner, "workspace-duty", "oncall")
	reader := e.invite(owner, "workspace-reader", "viewer")
	me := duty.get(root + "/session")
	var session struct{ CSRF string }
	if me.code != http.StatusOK || !strings.Contains(me.header.Get("Cache-Control"), "no-store") ||
		json.Unmarshal([]byte(me.body), &session) != nil || session.CSRF != duty.csrf ||
		strings.Contains(me.body, duty.cookies[sessionCookie].Value) {
		t.Fatal("workspace session was not limited to the same-origin account and its CSRF token")
	}

	path := root + "/alerts/" + e.id
	load := func() struct {
		Episode, Ownership string
		Actions            workspaceActions
	} {
		t.Helper()
		page := duty.get(path)
		var body struct {
			Episode, Ownership string
			Actions            workspaceActions
		}
		if page.code != http.StatusOK || json.Unmarshal([]byte(page.body), &body) != nil || body.Episode == "" {
			t.Fatalf("invalid workspace incident status %d", page.code)
		}
		return body
	}

	post := func(b *browser, endpoint, csrf, site string, input workspaceMutation) int {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Sec-Fetch-Site", site)
		req.AddCookie(b.cookies[sessionCookie])
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, req)
		return rec.Code
	}

	before := load()
	if !before.Actions.CanAck || before.Actions.AckVersion == "" {
		t.Fatal("on-call person could not take over this incident")
	}
	input := workspaceMutation{Episode: before.Episode, AckVersion: before.Actions.AckVersion}
	for _, tc := range []struct {
		b          *browser
		csrf, site string
	}{
		{duty, "", "same-origin"},
		{duty, session.CSRF, "cross-site"},
		{reader, reader.csrf, "same-origin"},
	} {
		if code := post(tc.b, path+"/ack", tc.csrf, tc.site, input); code != http.StatusForbidden {
			t.Errorf("unguarded incident action returned %d", code)
		}
	}

	if code := post(duty, path+"/ack", session.CSRF, "same-origin", input); code != http.StatusNoContent {
		t.Fatalf("takeover status %d", code)
	}
	if after := load(); !strings.Contains(after.Ownership, "workspace-duty") || after.Actions.CanAck {
		t.Fatal("the refreshed incident did not show the new owner")
	}
	if code := post(duty, path+"/ack", session.CSRF, "same-origin", input); code != http.StatusConflict {
		t.Errorf("stale takeover status %d, want 409", code)
	}
	input.Text = "Verified handover from the workspace."
	if code := post(duty, path+"/notes", session.CSRF, "same-origin", input); code != http.StatusNoContent {
		t.Fatalf("note status %d", code)
	}
	if page := duty.get(path + "/episodes/" + before.Episode); !strings.Contains(page.body, input.Text) {
		t.Fatal("the note did not appear on the exact incident")
	}
}

func TestWorkspaceAssetsAndDeepLinks(t *testing.T) {
	files := fstest.MapFS{
		"static/workspace/index.html":    &fstest.MapFile{Data: []byte("<title>Beacon</title>")},
		"static/workspace/assets/app.js": &fstest.MapFile{Data: []byte("void 0")},
	}
	mux := http.NewServeMux()
	registerWorkspaceFiles(mux, files)
	for _, tc := range []struct {
		path  string
		code  int
		cache string
	}{
		{"/workspace/", 200, "no-cache"},
		{"/workspace/alerts/example/episodes/id", 200, "no-cache"},
		{"/workspace/assets/app.js", 200, "immutable"},
		{"/workspace/assets/missing.js", 404, "no-store"},
		{"/workspace/assets/..%2Findex.html", 404, "no-store"},
		{"/workspace/unknown", 404, ""},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.code || !strings.Contains(rec.Header().Get("Cache-Control"), tc.cache) {
			t.Errorf("%s: code %d, cache %q", tc.path, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
}
