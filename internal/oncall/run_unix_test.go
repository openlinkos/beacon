//go:build !windows

package oncall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests run real programs through /bin/sh and so need a Unix host.

// script writes a shell script into dir and returns the command that runs it.
func script(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within %s", what, d)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// alive reports whether a process still runs (a zombie does not count).
func alive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true // not Linux: trust the signal
	}
	if i := bytes.LastIndexByte(b, ')'); i >= 0 && i+2 < len(b) {
		return b[i+2] != 'Z'
	}
	return true
}

func pidFrom(t *testing.T, p string) int {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func fire(t *testing.T, s *Server, delivery string, want int) {
	t.Helper()
	body := payload(delivery, "firing", time.Now())
	if rec := post(s, body, sign(testSecret, body), map[string]string{"X-Beacon-Delivery": delivery, "X-Beacon-Event": "firing"}); rec.Code != want {
		t.Fatalf("%s: %d %s, want %d", delivery, rec.Code, strings.TrimSpace(rec.Body.String()), want)
	}
}

func TestRunGetsPayloadAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, `env | grep '^BEACON_' | sort > "`+dir+`/env"
cat > "`+dir+`/stdin"
echo "hello from the agent"
exit 3
`)
	s, logs := newServer(t, Options{Run: run})
	body := payload("9f3c2a1b7e4d5c60", "firing", time.Now())
	if rec := post(s, body, sign(testSecret, body), nil); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "stdin")); !bytes.Equal(got, body) {
		t.Errorf("stdin = %s", got)
	}
	var p Payload
	_ = json.Unmarshal(body, &p)
	want := Environment(p)
	slices.Sort(want)
	if got, _ := os.ReadFile(filepath.Join(dir, "env")); strings.TrimSpace(string(got)) != strings.Join(want, "\n") {
		t.Errorf("env:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	matches, _ := filepath.Glob(filepath.Join(s.o.LogDir, "*-9f3c2a1b7e4d5c60.log"))
	if len(matches) != 1 {
		t.Fatalf("logs: %v", matches)
	}
	out, _ := os.ReadFile(matches[0])
	if !strings.Contains(string(out), "hello from the agent") || !strings.Contains(string(out), "--- beacon-oncall: exit 3 after") {
		t.Errorf("log:\n%s", out)
	}
	l := logs.String()
	if !strings.Contains(l, `msg="run started" delivery=9f3c2a1b7e4d5c60 event=firing hook=claude-oncall alert=0cdfbada62dd`) ||
		!strings.Contains(l, `msg="run ended" delivery=9f3c2a1b7e4d5c60 event=firing alert=0cdfbada62dd exit=3 timed_out=false duration=`) {
		t.Errorf("log lines:\n%s", l)
	}
}

// release lets the run for delivery leave the slot it is holding.
func release(t *testing.T, dir, delivery string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "release-"+delivery), []byte("go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrencyAndQueue(t *testing.T) {
	dir := t.TempDir()
	// Each run holds its slot until the test releases it, so nothing depends on a sleep ending
	// first. d1 and d2 are sent before either has started: both must be accepted, because a free
	// slot is room even when no worker has reached its receive yet.
	run := script(t, dir, `echo x >> "`+dir+`/$BEACON_DELIVERY.ran"
touch "`+dir+`/$BEACON_DELIVERY.start"
while [ ! -f "`+dir+`/release-$BEACON_DELIVERY" ]; do sleep 0.02; done
touch "`+dir+`/$BEACON_DELIVERY.end"
`)
	s, _ := newServer(t, Options{Run: run, Concurrency: 2, Queue: 1})
	fire(t, s, "d1", http.StatusAccepted)
	fire(t, s, "d2", http.StatusAccepted)
	waitFor(t, "d1 and d2 running", 5*time.Second, func() bool {
		return exists(filepath.Join(dir, "d1.start")) && exists(filepath.Join(dir, "d2.start"))
	})
	fire(t, s, "d3", http.StatusAccepted)           // queued
	fire(t, s, "d4", http.StatusServiceUnavailable) // no room: Beacon will try again
	fire(t, s, "d3", http.StatusAccepted)           // a repeat of a queued one is simply seen
	// d1 and d2 have already shown that a run touches its files at once, so a third run would have
	// done the same inside this window. It must still be waiting.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if exists(filepath.Join(dir, "d3.start")) || exists(filepath.Join(dir, "d3.ran")) {
			t.Fatal("d3 started while d1 and d2 still held their slots")
		}
		time.Sleep(10 * time.Millisecond)
	}
	release(t, dir, "d1")
	waitFor(t, "d3 running", 5*time.Second, func() bool { return exists(filepath.Join(dir, "d3.start")) })
	if exists(filepath.Join(dir, "d2.end")) {
		t.Error("d2 ended before it was released")
	}
	waitFor(t, "d1 ended", 5*time.Second, func() bool { return exists(filepath.Join(dir, "d1.end")) })
	release(t, dir, "d2")
	release(t, dir, "d3")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	starts, _ := filepath.Glob(filepath.Join(dir, "*.start"))
	if len(starts) != 3 || exists(filepath.Join(dir, "d4.start")) {
		t.Errorf("runs: %v", starts)
	}
	for _, id := range []string{"d1", "d2", "d3"} {
		if b, err := os.ReadFile(filepath.Join(dir, id+".ran")); err != nil || string(b) != "x\n" {
			t.Errorf("%s ran %q, want one run", id, b)
		}
	}
	if exists(filepath.Join(dir, "d4.ran")) {
		t.Error("d4 ran")
	}
	// Once closed, the harness takes nothing more, and says so in a way Beacon retries.
	fire(t, s, "d5", http.StatusServiceUnavailable)
}

func TestTimeoutKillsTheProcessGroup(t *testing.T) {
	old := killGrace
	killGrace = 300 * time.Millisecond
	t.Cleanup(func() { killGrace = old })
	dir := t.TempDir()
	// The program ignores the termination signal, and so does the child it leaves behind.
	run := script(t, dir, `trap '' TERM
sleep 300 &
echo $! > "`+dir+`/child.pid"
echo $$ > "`+dir+`/shell.pid"
wait
`)
	s, logs := newServer(t, Options{Run: run, Timeout: 500 * time.Millisecond})
	fire(t, s, "d1", http.StatusAccepted)
	waitFor(t, "child started", 3*time.Second, func() bool { return exists(filepath.Join(dir, "shell.pid")) && exists(filepath.Join(dir, "child.pid")) })
	shell, child := pidFrom(t, filepath.Join(dir, "shell.pid")), pidFrom(t, filepath.Join(dir, "child.pid"))
	if !alive(shell) || !alive(child) {
		t.Fatal("the program is not running")
	}
	began := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("the run ended only after %s", took)
	}
	waitFor(t, "shell and child gone", 3*time.Second, func() bool { return !alive(shell) && !alive(child) })
	if l := logs.String(); !strings.Contains(l, "timed_out=true") || !strings.Contains(l, "exit=-1") {
		t.Errorf("log lines:\n%s", l)
	}
	matches, _ := filepath.Glob(filepath.Join(s.o.LogDir, "*-d1.log"))
	if len(matches) != 1 {
		t.Fatalf("logs: %v", matches)
	}
	if out, _ := os.ReadFile(matches[0]); !strings.Contains(string(out), "killed after the 500ms timeout") {
		t.Errorf("log:\n%s", out)
	}
}

func TestRepeatedDeliveryRunsOnce(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, `echo run >> "`+dir+`/runs"`)
	s, _ := newServer(t, Options{Run: run})
	fire(t, s, "d1", http.StatusAccepted)
	fire(t, s, "d1", http.StatusAccepted)
	fire(t, s, "d1", http.StatusAccepted)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "runs")); string(b) != "run\n" {
		t.Errorf("runs = %q, want one", b)
	}
}

func TestResolvedEvents(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, `echo "$BEACON_EVENT" >> "`+dir+`/events"`)
	resolved := func(s *Server, delivery string) *httptest.ResponseRecorder {
		body := payload(delivery, "resolved", time.Now())
		return post(s, body, sign(testSecret, body), nil)
	}

	// Without -run-resolved, a resolved event is acknowledged and nothing runs.
	s, _ := newServer(t, Options{Run: run})
	if rec := resolved(s, "r1"); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "nothing to run") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = s.Shutdown(ctx)
	if exists(filepath.Join(dir, "events")) {
		t.Fatal("something ran for a resolved event without -run-resolved")
	}

	// With it, the program runs and knows the event.
	s, _ = newServer(t, Options{Run: run, RunResolved: run})
	if rec := resolved(s, "r2"); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "queued") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	fire(t, s, "f1", http.StatusAccepted)
	_ = s.Shutdown(ctx)
	b, _ := os.ReadFile(filepath.Join(dir, "events"))
	got := strings.Fields(string(b))
	slices.Sort(got)
	if strings.Join(got, " ") != "firing resolved" {
		t.Errorf("events = %q", b)
	}
}

func TestShutdownWaitsThenKills(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, `sleep 0.5; touch "`+dir+`/done"`)
	s, _ := newServer(t, Options{Run: run})
	fire(t, s, "d1", http.StatusAccepted)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(dir, "done")) {
		t.Error("Shutdown returned before the program finished")
	}

	// When the deadline passes, what still runs is killed and what still waits is dropped.
	dir = t.TempDir()
	run = script(t, dir, `touch "`+dir+`/$BEACON_DELIVERY.start"; echo $$ > "`+dir+`/$BEACON_DELIVERY.pid"; sleep 300`)
	s, logs := newServer(t, Options{Run: run, Concurrency: 1})
	fire(t, s, "d1", http.StatusAccepted)
	fire(t, s, "d2", http.StatusAccepted)
	waitFor(t, "d1 running", 3*time.Second, func() bool { return exists(filepath.Join(dir, "d1.pid")) })
	pid := pidFrom(t, filepath.Join(dir, "d1.pid"))
	sctx, scancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer scancel()
	if err := s.Shutdown(sctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown = %v, want the deadline", err)
	}
	waitFor(t, "d1 killed", 3*time.Second, func() bool { return !alive(pid) })
	if exists(filepath.Join(dir, "d2.start")) {
		t.Error("d2 ran after the deadline")
	}
	if !strings.Contains(logs.String(), `msg="run abandoned"`) {
		t.Errorf("log lines:\n%s", logs.String())
	}
}
