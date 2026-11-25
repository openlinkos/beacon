package mcpserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestFileSearchRESTAndMCPUseCredentialMachineCeilings(t *testing.T) {
	e := testutil.New(t)
	address, host, calls := searchSSHFixture(t, false)
	gateway, err := sshgate.New(sshgate.Deps{Store: e.Store, Dir: t.TempDir(), CA: searchTestSigner(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"allowed", "outside"} {
		if err := e.Store.PutMachine(store.Machine{Name: name, Address: address, User: "beacon",
			HostKey: string(ssh.MarshalAuthorizedKey(host))}); err != nil {
			t.Fatal(err)
		}
	}
	static := e.Token(t, "scoped-search", "machines:ssh")
	if _, err := e.Store.UpdateToken("scoped-search", func(tok *store.Token) error {
		tok.Machines = []string{"allowed"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, _, err := e.Store.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.Join(code, "operator", "", "fixture owner password"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.CreateAgent(store.Agent{Name: "searcher", Owner: "operator", Preset: "operator",
		Machines: []string{"allowed", "outside"}}); err != nil {
		t.Fatal(err)
	}
	oauth, err := e.Store.CreateGrant(store.Grant{Agent: "searcher", Client: "fixture", Machines: []string{"allowed"},
		Expires: time.Now().Add(time.Hour)}, true)
	if err != nil {
		t.Fatal(err)
	}
	reader := e.Token(t, "reader", "status:read")

	auth := &api.Auth{Store: e.Store}
	mux := http.NewServeMux()
	(&api.Server{Bridge: e.Bridge, SSH: gateway, Auth: auth, Log: slog.Default()}).Register(mux)
	mux.Handle("/mcp", Handler(e.Bridge, SSH{Gate: gateway}, auth, slog.Default()))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	const body = `{"directory":"/project","query":"needle"}`
	post := func(t *testing.T, cred, machine string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/v1/machines/"+machine+"/search", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if cred != "" {
			req.Header.Set("Authorization", "Bearer "+cred)
		}
		req.Header.Set("Content-Type", "application/json")
		r, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Body.Close() }()
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		return r.StatusCode, string(data)
	}
	check := func(t *testing.T, data, via, actor string) {
		t.Helper()
		var result sshgate.SearchResult
		if err := json.Unmarshal([]byte(data), &result); err != nil || len(result.Matches) != 1 ||
			result.Matches[0].Path != "/project/fixture.txt" || result.Matches[0].Line != 7 ||
			result.Machine != "allowed" || result.Session == "" {
			t.Fatalf("structured search result: %s (%v)", data, err)
		}
		r, ok := e.Store.Session(result.Session)
		if !ok || r.Machine != "allowed" || r.Via != via || r.Agent != actor || r.TranscriptState != store.EvidenceComplete {
			t.Errorf("authorised search evidence: %+v", r)
		}
	}
	if code, _ := post(t, "", "allowed"); code != http.StatusUnauthorized {
		t.Fatalf("anonymous search returned %d", code)
	}
	if code, _ := post(t, reader, "allowed"); code != http.StatusForbidden {
		t.Fatalf("status-only search returned %d", code)
	}
	for _, tc := range []struct{ name, cred, actor string }{
		{"static token", static, "scoped-search"}, {"OAuth grant", oauth.Access, "operator / searcher"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, out := post(t, tc.cred, "outside"); code != http.StatusForbidden || !strings.Contains(out, "machine.forbidden") {
				t.Fatalf("REST escaped the machine ceiling: %d %s", code, out)
			}
			code, out := post(t, tc.cred, "allowed")
			if code != http.StatusOK {
				t.Fatalf("authorised REST search: %d %s", code, out)
			}
			check(t, out, "api", tc.actor)

			con := connect(t, srv.URL+"/mcp", tc.cred)
			if r, out := call(t, con, "search_files", map[string]any{"machine": "outside", "directory": "/project", "query": "needle"}); r == nil || !r.IsError || !strings.Contains(out, "may not reach") {
				t.Fatalf("MCP escaped the machine ceiling: %s", out)
			}
			r, out := call(t, con, "search_files", map[string]any{"machine": "allowed", "directory": "/project", "query": "needle"})
			if r == nil || r.IsError {
				t.Fatalf("authorised MCP search: %s", out)
			}
			data, err := json.Marshal(r.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			check(t, string(data), "mcp", tc.actor)
		})
	}
	if _, err := e.Store.UpdateToken("scoped-search", func(token *store.Token) error {
		token.Machines = []string{"outside"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if code, out := post(t, static, "allowed"); code != http.StatusForbidden || !strings.Contains(out, "machine.forbidden") {
		t.Fatalf("REST reused a stale token machine selection: %d %s", code, out)
	}
	if _, err := e.Store.UpdateAgent("searcher", func(agent *store.Agent) error {
		agent.Machines = []string{"outside"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	con := connect(t, srv.URL+"/mcp", oauth.Access)
	if r, out := call(t, con, "search_files", map[string]any{"machine": "allowed", "directory": "/project", "query": "needle"}); r == nil || !r.IsError {
		t.Fatalf("MCP reused a stale OAuth machine selection: %s", out)
	}
	if calls.Load() != 4 {
		t.Fatalf("an unauthorised search reached the SSH target: %d observed calls", calls.Load())
	}
}

func TestFileSearchKeepsObservedMatchesAtDeadline(t *testing.T) {
	e := testutil.New(t)
	address, host, _ := searchSSHFixture(t, true)
	gateway, err := sshgate.New(sshgate.Deps{Store: e.Store, Dir: t.TempDir(), CA: searchTestSigner(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Store.PutMachine(store.Machine{Name: "fixture", Address: address, User: "beacon",
		HostKey: string(ssh.MarshalAuthorizedKey(host))}); err != nil {
		t.Fatal(err)
	}
	_ = e.Token(t, "runner", "machines:ssh")
	who := sshgate.Actor{Name: "runner", Key: "token:runner", Via: "mcp"}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result, err := gateway.SearchFiles(ctx, who, "fixture", sshgate.SearchInput{Directory: "/project", Query: "needle"})
	if err != nil || !result.TimedOut || !result.Limited || result.ScannedFiles != nil ||
		len(result.Matches) != 1 || result.Matches[0].Path != "/project/fixture.txt" {
		t.Fatalf("timed-out search lost observed results or reported completeness: %+v, %v", result, err)
	}
	record, ok := e.Store.Session(result.Session)
	if !ok || record.Ended.IsZero() || record.Exit != nil || record.TranscriptState != store.EvidenceComplete {
		t.Fatalf("the stopped search has no accurate session evidence: %+v", record)
	}
}

func searchTestSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func searchSSHFixture(t *testing.T, holdOpen bool) (string, ssh.PublicKey, *atomic.Int32) {
	t.Helper()
	host := searchTestSigner(t)
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(host)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var calls atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, channels, requests, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(requests)
				for nc := range channels {
					ch, reqs, err := nc.Accept()
					if err != nil {
						return
					}
					request, ok := <-reqs
					if !ok || request.Type != "exec" {
						_ = ch.Close()
						return
					}
					calls.Add(1)
					_ = request.Reply(true, nil)
					_, _ = io.WriteString(ch, "{\"type\":\"match\",\"path\":\"fixture.txt\",\"line\":7,\"text\":\"needle\",\"truncated\":false}\n")
					if holdOpen {
						ssh.DiscardRequests(reqs)
						return
					}
					_, _ = io.WriteString(ch, "{\"type\":\"summary\",\"scanned_files\":1,\"limited\":false}\n")
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
					_ = ch.Close()
				}
			}()
		}
	}()
	return ln.Addr().String(), host.PublicKey(), &calls
}
