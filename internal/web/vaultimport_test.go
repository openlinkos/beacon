package web

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/secrets/importer"
)

// The console's import wizard: the page and its doors, and the whole walk — upload, dry-run,
// confirm — against a real vault, with a made-up Chrome export. The upload is parsed in memory
// (nothing writes the file anywhere) and the batch waits under a one-time token.

// upload sends the wizard's multipart form: the CSRF secret, the source, the collection and the
// export file itself.
func (b *browser) upload(path, source, collection, name, content string) answer {
	b.e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf", b.csrf)
	_ = mw.WriteField("source", source)
	if collection != "" {
		_ = mw.WriteField("collection", collection)
	}
	w, _ := mw.CreateFormFile("file", name)
	_, _ = w.Write([]byte(content))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range b.headers {
		req.Header.Set(k, v)
	}
	for _, c := range b.cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	rec := httptest.NewRecorder()
	b.e.mux.ServeHTTP(rec, req)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	out, _ := io.ReadAll(res.Body)
	return answer{code: res.StatusCode, body: string(out), header: res.Header}
}

func TestVaultImportWizard(t *testing.T) {
	e := newEnv(t, i18n.EN)
	svc, err := secrets.Open(context.Background(),
		secrets.SealConfig{Mode: secrets.SealFile, Dir: filepath.Join(t.TempDir(), "secrets")},
		secrets.Deps{Store: e.st, Bot: e.bot, Lang: i18n.EN, CheckPassword: func(string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Vault = svc

	// the page needs the admin scope: a token without it is refused at the door
	reader := e.signedIn("reader", "status:read")
	if a := reader.get("/console/secrets/import"); a.code != http.StatusForbidden {
		t.Fatalf("the wizard is the admin's: %d", a.code)
	}
	// an account that signs in (the owner the setup link makes) gets the upload form
	owner := e.owner()
	a := owner.get("/console/secrets/import")
	if a.code != http.StatusOK || !strings.Contains(a.body, "Choose the export file") && !strings.Contains(a.body, `type="file"`) {
		t.Fatalf("the upload form: %d %s", a.code, a.body)
	}
	const csv = "name,url,username,password,note\nPortal,https://www.example.com,fixture-import-web-user@example.com,fixture-import-web-value-3,hello\n"
	// the dry-run: the report, without a value in it
	a = owner.upload("/console/secrets/import/dry-run", "chrome", "Chrome Imports", "chrome.csv", csv)
	if a.code != http.StatusOK {
		t.Fatalf("the dry-run answers: %d %s", a.code, a.body)
	}
	if !strings.Contains(a.body, "Chrome Imports") || !strings.Contains(a.body, "1") || !strings.Contains(a.body, `name="token"`) {
		t.Fatalf("the report and its token: %s", a.body)
	}
	if strings.Contains(a.body, "fixture-import-web-value-3") {
		t.Fatal("the report shows no value")
	}
	m := tokenRe.FindStringSubmatch(a.body)
	if m == nil {
		t.Fatal("the one-time token is in the page")
	}
	token := m[1]
	// nothing was written yet
	if len(svc.Collections()) != 0 {
		t.Fatalf("a dry-run writes nothing: %+v", svc.Collections())
	}
	// the confirmation imports it
	form := url.Values{"csrf": {owner.csrf}, "token": {token}, "on_conflict": {"new_version"}, "reason": {"the migration"}}
	a = owner.post("/console/secrets/import/commit", form)
	if a.code != http.StatusOK || !strings.Contains(a.body, "1 created") {
		t.Fatalf("the import runs: %d %s", a.code, a.body)
	}
	if !strings.Contains(a.body, "Delete the export file") || !strings.Contains(a.body, "Give the agents their entitlements") {
		t.Fatalf("the follow-ups are said: %s", a.body)
	}
	if got := svc.Items("Chrome Imports", ""); len(got) != 1 || got[0].Source != "chrome" {
		t.Fatalf("the item is in the vault: %+v", got)
	}
	// the token is one-time: a second commit starts over
	if back := owner.post("/console/secrets/import/commit", form); back.code != http.StatusSeeOther || !strings.Contains(back.header.Get("Location"), "problem=") {
		t.Fatalf("a spent token is refused: %d %s", back.code, back.body)
	}
	if len(svc.Items("Chrome Imports", "")) != 1 {
		t.Fatal("the second commit wrote nothing")
	}
}

// tokenRe reads the one-time token out of the report's confirmation form.
var tokenRe = regexp.MustCompile(`<input type="hidden" name="token" value="([^"]+)"`)

// TestImportSlotIsOneTimeAndExpires: the batch waits in memory under a token that answers once and
// not after ten minutes.
func TestImportSlotIsOneTimeAndExpires(t *testing.T) {
	s := &Server{}
	batch := importer.Batch{Source: importer.SourceCSV, Collections: []importer.CollectionImport{{Name: "Sheet"}}}
	token := s.putImport(batch)
	if token == "" {
		t.Fatal("the token is made")
	}
	if again, ok := s.takeImport(token); !ok || again.Source != batch.Source {
		t.Fatalf("the batch comes back: %+v %v", again, ok)
	}
	if _, ok := s.takeImport(token); ok {
		t.Fatal("the token answers once")
	}
	// an old slot is refused and dropped
	old := s.putImport(batch)
	s.importMu.Lock()
	slot := s.imports[old]
	slot.at = time.Now().Add(-11 * time.Minute)
	s.imports[old] = slot
	s.importMu.Unlock()
	if _, ok := s.takeImport(old); ok {
		t.Fatal("a slot older than ten minutes is refused")
	}
}
