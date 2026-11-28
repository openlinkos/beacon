package mcpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
	"github.com/openlinkos/beacon/internal/web/link"
)

func TestSessionAccessKeepsCredentialMachineCeilings(t *testing.T) {
	e := testutil.New(t)
	now := time.Now().UTC()
	dir := t.TempDir()
	signer, err := link.NewSigner(make([]byte, link.KeySize), time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	pages := &link.Pages{Signer: signer, PublicURL: "https://beacon.example.com"}
	gate, err := sshgate.New(sshgate.Deps{Store: e.Store, Dir: dir, Pages: pages})
	if err != nil {
		t.Fatal(err)
	}
	const allowed = "20261125-010001-deadbe-1"
	const other = "20261125-010002-deadbe-1"
	const oldRegistration = "20261125-010003-deadbe-1"
	const missing = "20261125-010004-deadbe-1"
	const recording = `{"version":3,"term":{"cols":80,"rows":24}}` + "\n"
	for _, name := range []string{"edge-one", "edge-two"} {
		if err := e.Store.PutMachine(store.Machine{Name: name, Address: name + ".example.com:22", User: "beacon", Added: now.Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ id, machine string }{{allowed, "edge-one"}, {other, "edge-two"}, {oldRegistration, "edge-one"}} {
		m, _ := e.Store.Machine(row.machine)
		r := store.SessionRecord{ID: row.id, Machine: m.Name, MachineRegistration: m.Registration(), Kind: "shell",
			Started: now.Add(-time.Minute), Ended: now, Command: "printf fixture", Recording: row.id}
		if row.id == oldRegistration {
			r.MachineRegistration = "formerly-enrolled"
		}
		if err := e.Store.PutSession(r); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, row.id+".cast"), []byte(recording), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, row.id+".json"), []byte(`{"stdout":"fixture output","page":"https://beacon.example.com/old?s=previous-share"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	static := e.Token(t, "scoped-reader", "status:read", "sessions:read")
	metadata := e.Token(t, "scoped-metadata", "status:read")
	for _, name := range []string{"scoped-reader", "scoped-metadata"} {
		if _, err := e.Store.UpdateToken(name, func(tok *store.Token) error {
			tok.MachineAccess = store.NamedAccess("edge-one")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	code, _, err := e.Store.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.Join(code, "owner", "", "fixture owner password"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.CreateAgent(store.Agent{Name: "operator", Owner: "owner", Preset: "operator", Machines: []string{"edge-one", "edge-two"}}); err != nil {
		t.Fatal(err)
	}
	oauth, err := e.Store.CreateGrant(store.Grant{Agent: "operator", Client: "fixture-client", Machines: []string{"edge-one"}, Expires: now.Add(time.Hour)}, true)
	if err != nil {
		t.Fatal(err)
	}

	auth := &api.Auth{Store: e.Store}
	mux := http.NewServeMux()
	(&api.Server{Bridge: e.Bridge, Auth: auth, SSH: gate, Log: slog.Default()}).Register(mux)
	mux.Handle("/mcp", Handler(e.Bridge, SSH{Gate: gate}, auth, slog.Default()))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	const path = "/api/v1/sessions"
	get := func(t *testing.T, token, path string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, string(body)
	}
	checkList := func(t *testing.T, body string, detail bool) {
		t.Helper()
		var out sessionsOut
		if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Sessions) != 1 || out.Sessions[0].ID != allowed {
			t.Fatalf("list took its limit before applying the machine ceiling: %s", body)
		}
		row := out.Sessions[0]
		if detail {
			assertSessionSignInPage(t, row.Page, allowed)
		} else if row.Command != "" || row.Page != "" || row.Recording != "" {
			t.Fatalf("a metadata-only credential saw replay details: %s", body)
		}
	}
	for _, tc := range []struct{ name, secret string }{{"static token", static}, {"OAuth grant", oauth.Access}} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := get(t, tc.secret, path+"?limit=1")
			if status != http.StatusOK {
				t.Fatalf("REST list: %d %s", status, body)
			}
			checkList(t, body, true)
			c := connect(t, srv.URL+"/mcp", tc.secret)
			mcpList, _ := call(t, c, "list_sessions", map[string]any{"limit": 1})
			wire, err := json.Marshal(mcpList.StructuredContent)
			if err != nil || mcpList.IsError {
				t.Fatalf("MCP list: %v, %s", err, wire)
			}
			checkList(t, string(wire), true)
			status, body = get(t, tc.secret, path+"/"+allowed)
			var restTranscript sshgate.Transcript
			if err := json.Unmarshal([]byte(body), &restTranscript); status != http.StatusOK || err != nil || restTranscript.Stdout != "fixture output" ||
				restTranscript.RecordingAvailable == nil || !*restTranscript.RecordingAvailable {
				t.Fatalf("REST detail: %d %s", status, body)
			}
			assertSessionSignInPage(t, restTranscript.Page, allowed)
			mcpDetail, mcpText := call(t, c, "get_session", map[string]any{"id": allowed})
			wire, err = json.Marshal(mcpDetail.StructuredContent)
			var tr sshgate.Transcript
			if err != nil || json.Unmarshal(wire, &tr) != nil || mcpDetail.IsError || tr.Stdout != "fixture output" ||
				tr.RecordingAvailable == nil || !*tr.RecordingAvailable {
				t.Fatalf("MCP detail: %s", mcpText)
			}
			assertSessionSignInPage(t, tr.Page, allowed)
			if status, body := get(t, tc.secret, path+"/"+allowed+"/recording"); status != http.StatusOK || body != recording {
				t.Fatalf("REST recording: %d %s", status, body)
			}
			for _, suffix := range []string{"", "/recording"} {
				_, absent := get(t, tc.secret, path+"/"+missing+suffix)
				for _, id := range []string{other, oldRegistration} {
					status, body := get(t, tc.secret, path+"/"+id+suffix)
					if status != http.StatusNotFound || !strings.Contains(body, `"session.not_found"`) || strings.ReplaceAll(body, id, missing) != absent {
						t.Errorf("REST inaccessible %s differs from a missing session: %d %s", suffix, status, body)
					}
				}
			}
			_, absent := call(t, c, "get_session", map[string]any{"id": missing})
			if result, body := call(t, c, "get_session", map[string]any{"id": other}); !result.IsError || strings.ReplaceAll(body, other, missing) != absent {
				t.Errorf("MCP inaccessible session differs from a missing session: %s", body)
			}
		})
	}
	status, body := get(t, metadata, path+"?limit=1")
	if status != http.StatusOK {
		t.Fatalf("REST metadata: %d %s", status, body)
	}
	checkList(t, body, false)
	if status, _ := get(t, metadata, path+"/"+allowed); status != http.StatusForbidden {
		t.Errorf("a metadata-only credential read a session transcript (HTTP %d)", status)
	}
	if _, err := e.Store.UpdateAgent("operator", func(a *store.Agent) error {
		a.MachineAccess = store.NamedAccess("edge-two")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	status, body = get(t, oauth.Access, path)
	var none sessionsOut
	if err := json.Unmarshal([]byte(body), &none); status != http.StatusOK || err != nil || len(none.Sessions) != 0 {
		t.Fatalf("a disjoint OAuth grant gained REST session metadata: %d %s", status, body)
	}
	c := connect(t, srv.URL+"/mcp", oauth.Access)
	if list, body := call(t, c, "list_sessions", nil); list.IsError || !strings.Contains(body, `"sessions":[]`) {
		t.Fatalf("a disjoint OAuth grant gained MCP session metadata: %s", body)
	}
	if res, body := call(t, c, "whoami", nil); res.IsError || !strings.Contains(body, `"machine_scope":"none"`) {
		t.Fatal("whoami should distinguish a disjoint grant from all machines")
	}
	status, body = get(t, oauth.Access, "/api/v1/token")
	var scope struct {
		MachineScope string `json:"machine_scope"`
	}
	if err := json.Unmarshal([]byte(body), &scope); status != http.StatusOK || err != nil || scope.MachineScope != "none" {
		t.Fatal("the REST credential view should distinguish a disjoint grant from all machines")
	}

	// A valid capture can coexist with a corrupt transcript. Lookups still use
	// the original credential's effective machine scope.
	saved, _ := e.Store.Session(allowed)
	saved.RecordingState, saved.TranscriptState = store.EvidenceComplete, store.EvidenceComplete
	if err := e.Store.PutSession(saved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, allowed+".json"), []byte(`{"stdout":"discard partial data", "commands":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c = connect(t, srv.URL+"/mcp", static)
	result, body := call(t, c, "get_session", map[string]any{"id": allowed})
	if result.IsError || !strings.Contains(body, `"transcript_state":"unavailable"`) || !strings.Contains(body, `"recording_available":true`) ||
		strings.Contains(body, "discard partial data") {
		t.Fatalf("MCP unreadable transcript: %s", body)
	}
	if err := os.Remove(filepath.Join(dir, allowed+".cast")); err != nil {
		t.Fatal(err)
	}
	status, body = get(t, static, path+"/"+allowed)
	var restEvidence sshgate.Transcript
	if err := json.Unmarshal([]byte(body), &restEvidence); status != http.StatusOK || err != nil ||
		restEvidence.RecordingAvailable == nil || *restEvidence.RecordingAvailable || restEvidence.RecordingState != store.EvidenceComplete {
		t.Fatalf("REST missing replay does not change the capture result: %d %s", status, body)
	}
	if status, body = get(t, static, path+"/"+allowed+"/recording"); status != http.StatusNotFound ||
		!strings.Contains(body, `"session.recording_unavailable"`) {
		t.Fatalf("REST authorised missing recording: %d %s", status, body)
	}

}

func assertSessionSignInPage(t *testing.T, address, id string) {
	t.Helper()
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host != "beacon.example.com" || u.Path != "/console/sign-in" ||
		u.Query().Get("next") != "/console/sessions/"+id || u.Query().Has("s") {
		t.Fatalf("expected the sign-in address for the permitted session: %q", address)
	}
}
