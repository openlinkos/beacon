//go:build !windows

package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestPeopleCommands runs the service with no account, finds the setup link in its log, makes the first
// owner with it as a browser would, then runs setup, user, agent and token claim over the admin socket.
func TestPeopleCommands(t *testing.T) {
	hold := make(chan os.Signal, 16)
	signal.Notify(hold, syscall.SIGHUP, syscall.SIGTERM)
	defer signal.Stop(hold)

	d := t.TempDir()
	put := func(path, body string) {
		t.Helper()
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	data := filepath.Join(d, "data")
	put(filepath.Join(d, "rules-token"), "0123456789abcdef0123456789")
	put(filepath.Join(d, "catalog", "locations.yaml"), "locations:\n  - {id: edge-1, name: United States - Los Angeles, region: overseas}\n")
	cfg := filepath.Join(d, "config.yaml")
	put(cfg, "listen: "+addr+"\ndata_dir: "+data+"\ncatalog_dir: "+filepath.Join(d, "catalog")+"\nlanguage: en\npublic_url: https://status.example.com\n"+
		"rules: {token_file: "+filepath.Join(d, "rules-token")+"}\nvictoriametrics: {url: http://127.0.0.1:1}\n")
	t.Setenv("BEACON_DATA_DIR", data)

	var logs syncBuffer
	done := make(chan int, 1)
	go func() { done <- run([]string{"serve", "-config", cfg}, &logs, &logs) }()
	cli := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := run(args, &out, &errb)
		return code, out.String(), errb.String()
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if code, _, _ := cli("user", "list"); code == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the service did not answer on its socket:\n%s", logs.String())
		}
	}
	defer func() {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("serve did not stop on SIGTERM")
		}
	}()

	// the log has a setup link, and beacon setup prints another
	if !strings.Contains(logs.String(), "https://status.example.com/console/setup?code=") {
		t.Fatalf("no setup link in the log:\n%s", logs.String())
	}
	code, out, _ := cli("setup")
	if code != 0 || !strings.HasPrefix(out, "https://status.example.com/console/setup?code=") {
		t.Fatalf("setup: %d %s", code, out)
	}
	link, _ := url.Parse(strings.TrimSpace(out))

	// a browser makes the first owner with it
	page, cookie := fetch(t, "http://"+addr+link.RequestURI(), "", nil)
	form := regexp.MustCompile(`name="form" value="([^"]+)"`).FindStringSubmatch(page)
	if form == nil {
		t.Fatalf("setup page: %s", page)
	}
	if _, set := fetch(t, "http://"+addr+"/console/setup", cookie, url.Values{"code": {link.Query().Get("code")}, "form": {form[1]},
		"username": {"alice"}, "password": {"correct horse battery"}, "again": {"correct horse battery"}}); !strings.Contains(set, "__Host-beacon-console=") {
		t.Fatalf("setup did not sign alice in: %q", set)
	}
	if code, _, errOut := cli("setup"); code != 1 || !strings.Contains(errOut, "accounts already") {
		t.Errorf("setup again: %d %s", code, errOut)
	}

	code, out, errOut := cli("user", "invite", "-role", "oncall", "-note", "bob")
	if code != 0 || !strings.HasPrefix(out, "https://status.example.com/console/join?code=") || !strings.Contains(errOut, "works once") {
		t.Errorf("invite: %d %s %s", code, out, errOut)
	}
	if code, out, _ := cli("user", "list"); code != 0 || !strings.Contains(out, "alice") || !strings.Contains(out, "owner") || !strings.Contains(out, "1 invitations not yet used") {
		t.Errorf("user list: %d %s", code, out)
	}
	if code, _, errOut := cli("user", "role", "alice", "viewer"); code != 1 || !strings.Contains(errOut, "without an owner") {
		t.Errorf("demoting the last owner: %d %s", code, errOut)
	}
	if code, out, _ := cli("user", "reset", "alice"); code != 0 || !strings.HasPrefix(out, "https://status.example.com/console/reset?code=") {
		t.Errorf("reset: %d %s", code, out)
	}

	// an agent with its token on stdout, the snippets on stderr
	code, out, errOut = cli("agent", "add", "ci-bot", "-owner", "alice", "-preset", "oncall", "-machines", "edge-1", "-expires", "90d")
	secret := strings.TrimSpace(out)
	if code != 0 || !strings.HasPrefix(secret, "lbt_") || strings.Count(out, "\n") != 1 || !strings.Contains(errOut, "claude mcp add --transport http beacon https://status.example.com/mcp") ||
		!strings.Contains(errOut, "codex mcp add beacon --url https://status.example.com/mcp --bearer-token-env-var BEACON_TOKEN") {
		t.Fatalf("agent add: %d %q %s", code, out, errOut)
	}
	// the token works on the API, as alice's agent
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/api/v1/token", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), `"actor": "alice / ci-bot"`) {
		t.Errorf("whoami: %d %s", res.StatusCode, b)
	}
	if code, out, _ := cli("agent", "list"); code != 0 || !strings.Contains(out, "ci-bot") || !strings.Contains(out, "edge-1") {
		t.Errorf("agent list: %d %s", code, out)
	}

	// a legacy token, claimed
	if code, _, errOut := cli("token", "create", "old-bot", "-scopes", "status:read,sessions:read,secrets:describe", "-expires", "30d"); code != 0 {
		t.Fatalf("token create: %d %s", code, errOut)
	}
	if code, out, _ := cli("token", "claim", "old-bot", "-owner", "alice"); code != 0 || !strings.Contains(out, "alice / old-bot") || !strings.Contains(out, "readonly") {
		t.Errorf("token claim: %d %s", code, out)
	}
	if code, out, _ := cli("agent", "remove", "ci-bot"); code != 0 || !strings.Contains(out, "removed") {
		t.Errorf("agent remove: %d %s", code, out)
	}
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 401 {
		t.Errorf("a removed agent's token: %v %v", res, err)
	} else {
		_ = res.Body.Close()
	}
	if code, out, _ := cli("user", "disable", "alice"); code != 1 || out != "" {
		t.Errorf("disabling the last owner: %d %s", code, out)
	}
}

// fetch sends a GET (form nil) or a form POST with the cookie, and returns the body and the cookie set.
func fetch(t *testing.T, u, cookie string, form url.Values) (string, string) {
	t.Helper()
	method, body := http.MethodGet, io.Reader(nil)
	if form != nil {
		method, body = http.MethodPost, strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	var set []string
	for _, c := range res.Cookies() {
		set = append(set, c.Name+"="+c.Value)
	}
	return string(b), strings.Join(set, "; ")
}
