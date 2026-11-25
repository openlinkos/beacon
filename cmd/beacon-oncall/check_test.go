package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestCheckDrillUsesCurrentPermissionsAndEnrolment(t *testing.T) {
	const signingSecret = "fixture hook signing secret"
	const destination = "http://127.0.0.1:1/private"
	loaded := func(name, service, reporter string, severities ...string) config.Agent {
		return config.Agent{Name: name, Kind: "webhook", Service: service, Severities: severities,
			Reporter: reporter, Secret: signingSecret, URL: destination}
	}
	env := testutil.NewWith(t, func(deps *bridge.Deps) {
		deps.Config.Agents = []config.Agent{
			loaded("reference-drill", "^drill$", "token:drill-reporter", "critical"),
			loaded("wrong-reporter", "^drill$", "token:other-reporter", "critical"),
			loaded("wrong-severity", "^drill$", "token:drill-reporter", "warning"),
			loaded("wrong-service", "^demo$", "token:drill-reporter", "critical"),
			loaded("broad-filter", "drill.*", "token:drill-reporter", "critical"),
			{Name: "no-signature", Kind: "webhook", Service: "drill", Severities: []string{"critical"}, Reporter: "token:drill-reporter"},
		}
	})
	setupCode, _, err := env.Store.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Store.Join(setupCode, "alice", "", "fixture owner password"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Store.CreateAgent(store.Agent{Name: "drill-agent", Owner: "alice", Preset: "oncall", Machines: []string{"edge-1"}}); err != nil {
		t.Fatal(err)
	}
	agent, _, err := env.Store.CreateAgentToken("drill-agent", time.Hour, "alice")
	if err != nil {
		t.Fatal(err)
	}
	reporter := env.Token(t, "drill-reporter", "alerts:report")
	broad := env.Token(t, "broad-reporter", "alerts:report", "status:read")
	dir := t.TempDir()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	g, err := sshgate.New(sshgate.Deps{Store: env.Store, Dir: filepath.Join(dir, "recordings"), HostKey: signer, CA: signer})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&api.Server{Bridge: env.Bridge, SSH: g, Auth: &api.Auth{Store: env.Store}, Log: slog.Default()}).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	agentFile := filepath.Join(dir, "agent")
	reportFile := filepath.Join(dir, "reporter")
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(agentFile, agent)
	write(reportFile, reporter)
	args := []string{"check", "-url", server.URL, "-owner", "alice", "-agent", "drill-agent", "-machine", "edge-1",
		"-agent-token-file", agentFile, "-report-token-file", reportFile}
	expectedReporter := "drill-reporter"
	check := func(code int, want string, extra ...string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		got := run(append(append(args, "-reporter", expectedReporter), extra...), &stdout, &stderr)
		output := stdout.String() + stderr.String()
		if got != code || !strings.Contains(output, want) {
			t.Fatalf("check exit %d, output %q; want %d and %q", got, output, code, want)
		}
		for _, secret := range []string{agent, reporter, broad, signingSecret, destination} {
			if strings.Contains(output, secret) {
				t.Fatal("check disclosed a credential")
			}
		}
	}

	check(1, "is not enrolled")
	if err := env.Store.PutMachine(store.Machine{Name: "edge-1", Address: "edge-1.example.com:22", User: "beacon"}); err != nil {
		t.Fatal(err)
	}
	check(0, "is limited to enrolled machine")
	check(0, "The loaded signed hook", "-hook", "reference-drill", "-chat", "webhook")
	check(1, "did not confirm hook", "-hook", "missing")
	check(1, "did not confirm hook", "-hook", "wrong-reporter")
	check(1, "does not match a firing critical alert", "-hook", "wrong-severity")
	check(1, "does not match a firing critical alert", "-hook", "wrong-service")
	check(1, "limit the first hook", "-hook", "broad-filter")
	check(1, "with a signing secret", "-hook", "no-signature")
	check(1, `is "webhook"; expected "slack"`, "-hook", "reference-drill", "-chat", "slack")
	check(1, "-chat requires -hook", "-chat", "slack")
	check(1, "the hook name uses lower case", "-hook", "../token")
	write(reportFile, agent)
	check(1, "must be separate")
	write(reportFile, broad)
	check(1, "must be the standalone token")
	expectedReporter = "broad-reporter"
	check(1, "must have only the alerts:report scope")
	expectedReporter = "drill-reporter"
	write(reportFile, reporter)
	if _, err := env.Store.UpdateAgent("drill-agent", func(a *store.Agent) error {
		a.Machines = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	check(1, "must be limited to")
}

func TestCheckDrillRefusesRedirectsAndRedactsReplies(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer destination.Close()

	const agent = "private-agent-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/beacon/api/v1/token" || r.Header.Get("Authorization") != "Bearer "+agent {
			t.Errorf("unexpected preflight route %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = w.Write([]byte(agent))
	}))
	defer server.Close()

	dir := t.TempDir()
	for name, value := range map[string]string{"a": agent, "r": "private-report-token"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	args := []string{"check", "-url", server.URL + "/beacon", "-owner", "alice", "-agent", "drill-agent", "-machine", "edge-1",
		"-reporter", "drill-reporter", "-agent-token-file", filepath.Join(dir, "a"), "-report-token-file", filepath.Join(dir, "r")}
	if code := run(args, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "HTTP 307") ||
		strings.Contains(stdout.String()+stderr.String(), agent) || redirected.Load() != 0 {
		t.Fatalf("unexpected redirect result (exit %d, requests sent to redirect: %d)", code, redirected.Load())
	}
}
