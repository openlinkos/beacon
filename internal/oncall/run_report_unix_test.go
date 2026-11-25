//go:build !windows

package oncall

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunReportsExecutedAndUnstartedJobs(t *testing.T) {
	for _, tc := range []struct {
		name, command, terminal string
		removeLogDir            bool
	}{
		{"succeeded", "exit 0", `{"status":"succeeded","exit_code":0}`, false},
		{"failed", "exit 23", `{"status":"failed","exit_code":23}`, false},
		{"signal", "kill -TERM $$", `{"status":"failed"}`, false},
		{"log unavailable", "exit 0", `{"status":"not_started"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan string, 4)
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/alerts/0cdfbada62dd/agent-runs/d1" || r.Header.Get("Authorization") != "Bearer "+fixtureReportToken {
					t.Error("report route or bearer did not come from the harness configuration")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				got <- string(body)
				w.WriteHeader(http.StatusOK)
			}))
			defer receiver.Close()
			dir := t.TempDir()
			logDir := filepath.Join(dir, "logs")
			agent := script(t, dir, `env | grep -F 'lbt_fixture_only' && exit 99
echo 'private command output'
`+tc.command)
			// Replace the launcher shell so Go observes the process signal directly. A shell waiting for
			// a child can translate that child's signal into a conventional numeric exit status.
			command := "exec '" + strings.ReplaceAll(agent, "'", "'\\''") + "'"
			s, _ := newServer(t, Options{Run: command, LogDir: logDir, ReportURL: receiver.URL, ReportToken: fixtureReportToken})
			if tc.removeLogDir {
				if err := os.Remove(logDir); err != nil {
					t.Fatal(err)
				}
			}
			fire(t, s, "d1", http.StatusAccepted)
			fire(t, s, "d1", http.StatusAccepted) // accepted duplicates must not start a process or report again
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := s.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			close(got)
			counts := make(map[string]int)
			for body := range got {
				counts[body]++
			}
			wantRunning := 1
			if tc.removeLogDir {
				wantRunning = 0
			}
			if counts[tc.terminal] != 1 || counts[`{"status":"running"}`] != wantRunning || len(counts) != 1+wantRunning {
				t.Errorf("unexpected reports: %v", counts)
			}
		})
	}
}

func TestBlockedRunningReportDoesNotDelayTimeoutOrTerminalReport(t *testing.T) {
	dir := t.TempDir()
	const timeout = time.Second
	started := make(chan struct{}, reportAttempts)
	terminal := make(chan runReport, 1)
	releaseRunning := make(chan struct{})
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got runReport
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got.Status == runRunning {
			started <- struct{}{}
			<-releaseRunning
		} else {
			terminal <- got
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	defer close(releaseRunning)
	agent := script(t, dir, `sleep 300 &
echo $$ > "`+dir+`/shell.pid"
echo $! > "`+dir+`/child.pid"
wait
`)
	// Source the fixture in Beacon's shell so the one-second deadline tests the report path
	// without depending on the cost of launching another executable from a temporary directory.
	s, _ := newServer(t, Options{Run: `. "` + agent + `"`, Timeout: timeout, ReportURL: receiver.URL, ReportToken: fixtureReportToken})
	fire(t, s, "d1", http.StatusAccepted)
	select {
	case <-started:
	case <-time.After(4 * time.Second):
		t.Fatal("running report never arrived")
	}
	select {
	case got := <-terminal:
		if got.Status != runTimedOut || got.ExitCode != nil {
			t.Errorf("unexpected terminal report: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout and final report waited on the blocked running report")
	}
	shell, child := pidFrom(t, filepath.Join(dir, "shell.pid")), pidFrom(t, filepath.Join(dir, "child.pid"))
	waitFor(t, "expired process group to exit", 3*time.Second, func() bool { return !alive(shell) && !alive(child) })
}

func TestReportSkipsAnIntentionallyUnconfiguredRecovery(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a deliberately skipped recovery produced a report")
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	s, _ := newServer(t, Options{ReportURL: receiver.URL, ReportToken: fixtureReportToken})
	body := payload("recovery", "resolved", time.Now())
	for _, expected := range []string{"nothing to run", "seen"} {
		got := post(s, body, sign(testSecret, body), nil)
		if got.Code != http.StatusAccepted || strings.TrimSpace(got.Body.String()) != expected {
			t.Fatalf("recovery acceptance: %d, %q", got.Code, got.Body.String())
		}
	}
	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownReportsAcceptedWorkItCouldNotStart(t *testing.T) {
	type received struct {
		delivery, status string
	}
	observed := make(chan received, 4)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got runReport
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		observed <- received{filepath.Base(r.URL.Path), got.Status}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	s, _ := newServer(t, Options{Run: "sleep 300", Concurrency: 1, Queue: 1, ReportURL: receiver.URL, ReportToken: fixtureReportToken})
	fire(t, s, "d1", http.StatusAccepted)
	select {
	case got := <-observed:
		if got != (received{"d1", runRunning}) {
			t.Fatalf("first report = %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first run never started")
	}
	fire(t, s, "d2", http.StatusAccepted)
	fire(t, s, "rejected", http.StatusServiceUnavailable)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown: %v", err)
	}
	close(observed)
	got := make(map[received]int)
	for report := range observed {
		got[report]++
	}
	if len(got) != 2 || got[received{"d1", runFailed}] != 1 || got[received{"d2", runNotStarted}] != 1 {
		t.Errorf("terminal reports: %v", got)
	}
}
