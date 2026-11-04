// Package oncall is the reference harness that runs an agent program when Beacon wakes it. It listens
// for Beacon's webhook (docs/guide/on-call.md, "The webhook"), checks the signature, refuses what it has seen
// or what is stale, answers at once, and starts the program the operator named, with the payload on its
// standard input and the alert in its environment. It decides nothing: the program does, and Beacon
// records what the program then does. Standard library only, so that it builds and runs anywhere.
package oncall

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// MaxBody is the largest payload accepted; Beacon's are a few kilobytes.
	MaxBody = 1 << 20
	// DefaultQueue is how many runs may wait for a free slot; beyond that Beacon is told to try later.
	DefaultQueue = 32
	// seenFor is how long a delivery's id is remembered.
	seenFor = 24 * time.Hour
	// maxAge is how old a delivery may be. Beacon retries within about four and a half minutes of the
	// event, and takes up a call interrupted by a restart when it is less than ten minutes old; so
	// nothing genuine arrives later than this, and anything older is a replay.
	maxAge = 15 * time.Minute
	// logTime names the log files, in UTC.
	logTime = "20060102T150405Z"
)

// killGrace is the time a run gets between the termination signal and the kill. Tests shorten it.
var killGrace = 5 * time.Second

// Options configure the harness. Secret, Run and LogDir are required.
type Options struct {
	Secret      []byte        // the hook's secret, as in Beacon's secret_file
	Run         string        // the program for firing events, run through /bin/sh -c
	RunResolved string        // the program for resolved events; empty: they are acknowledged and nothing runs
	Concurrency int           // runs at once (2 by default)
	Queue       int           // runs waiting for a slot (DefaultQueue by default)
	Timeout     time.Duration // per run, after which its process group is killed (30 minutes by default)
	LogDir      string        // where each run's output goes, as <time>-<delivery>.log
	Keep        time.Duration // logs older than this are removed (30 days by default; 0 keeps them all)
	Log         *slog.Logger  // the harness's own log (stderr by default)
}

// Payload is what Beacon sends (bridge.HookPayload), as far as the harness reads it. The program gets
// the whole body on its standard input.
type Payload struct {
	Delivery string    `json:"delivery"`
	Event    string    `json:"event"`
	At       time.Time `json:"at"`
	Hook     string    `json:"hook"`
	Alert    struct {
		ID       string   `json:"id"`
		Status   string   `json:"status"`
		Severity string   `json:"severity"`
		Title    string   `json:"title"`
		Service  string   `json:"service"`
		Services []string `json:"services"`
	} `json:"alert"`
	Links struct {
		Alert    string `json:"alert"`
		Evidence string `json:"evidence"`
		Timeline string `json:"timeline"`
		MCP      string `json:"mcp"`
		Runbook  string `json:"runbook"`
	} `json:"links"`
}

// Server takes Beacon's deliveries and runs the program for them.
type Server struct {
	o   Options
	log *slog.Logger
	now func() time.Time

	mu      sync.Mutex
	seen    map[string]time.Time // delivery id: when it was taken
	closed  bool                 // no more deliveries are taken
	abandon bool                 // queued runs are dropped (the shutdown deadline passed)
	running map[*exec.Cmd]struct{}

	queue       chan *job
	outstanding int            // accepted runs not yet finished; capped at Concurrency + Queue
	wg          sync.WaitGroup // the workers
}

type job struct {
	p       Payload
	body    []byte
	command string
}

// New checks the options, creates the log directory and starts the workers.
func New(o Options) (*Server, error) {
	if len(o.Secret) == 0 {
		return nil, errors.New("a secret is required")
	}
	if strings.TrimSpace(o.Run) == "" {
		return nil, errors.New("a program to run is required")
	}
	if o.LogDir == "" {
		return nil, errors.New("a log directory is required")
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 2
	}
	if o.Queue <= 0 {
		o.Queue = DefaultQueue
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Minute
	}
	if o.Keep < 0 {
		o.Keep = 0
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if err := os.MkdirAll(o.LogDir, 0o750); err != nil {
		return nil, fmt.Errorf("log directory: %w", err)
	}
	// The channel holds every accepted run, both those running and those waiting. It is as deep as the
	// concurrency plus the queue, so a send that outstanding has already allowed cannot block on a
	// worker that has not reached its receive; outstanding is what refuses a delivery.
	s := &Server{o: o, log: o.Log, now: time.Now, seen: map[string]time.Time{}, running: map[*exec.Cmd]struct{}{},
		queue: make(chan *job, o.Concurrency+o.Queue)}
	s.prune()
	for range o.Concurrency {
		s.wg.Add(1)
		go s.worker()
	}
	return s, nil
}

// Genuine reports whether signature is Beacon's X-Beacon-Signature for body under secret: "sha256=" and
// the hex HMAC-SHA256, compared in constant time.
func Genuine(secret, body []byte, signature string) bool {
	hexMAC, ok := strings.CutPrefix(signature, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexMAC)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// ServeHTTP answers GET /healthz and takes a delivery on any POST.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		_, _ = io.WriteString(w, "ok\n")
	case r.Method == http.MethodPost:
		s.hook(w, r)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		http.NotFound(w, r)
	default:
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// hook checks one delivery and queues its run. The answer is sent before anything runs: 202 when the
// run is queued, was already taken or has nothing to run; 503 when there is no room, so that Beacon
// tries again; 401 and 400 when trying again would not help.
func (s *Server) hook(w http.ResponseWriter, r *http.Request) {
	from := r.RemoteAddr
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.log.Warn("refused", "reason", "body too large", "from", from)
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		s.log.Warn("refused", "reason", "body not read", "from", from, "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sig := r.Header.Get("X-Beacon-Signature")
	if !Genuine(s.o.Secret, body, sig) {
		reason := "bad signature"
		if sig == "" {
			reason = "no signature"
		}
		s.log.Warn("refused", "reason", reason, "from", from, "bytes", len(body))
		http.Error(w, reason, http.StatusUnauthorized)
		return
	}
	// From here on the body is Beacon's. The headers are not signed, so the body decides and the
	// headers may only agree with it.
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		s.log.Warn("refused", "reason", "not JSON", "from", from)
		http.Error(w, "not JSON", http.StatusBadRequest)
		return
	}
	if reason := s.check(p, r.Header); reason != "" {
		s.log.Warn("refused", "reason", reason, "from", from, "delivery", p.Delivery, "event", p.Event)
		status := http.StatusBadRequest
		if reason == "stale delivery" {
			status = http.StatusUnauthorized
		}
		http.Error(w, reason, status)
		return
	}
	command := s.o.Run
	if p.Event == "resolved" {
		command = s.o.RunResolved
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.log.Warn("not taken", "reason", "shutting down", "delivery", p.Delivery)
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	if at, dup := s.seen[p.Delivery]; dup && s.now().Sub(at) <= seenFor {
		s.log.Info("delivery seen before", "delivery", p.Delivery, "event", p.Event)
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "seen\n")
		return
	}
	if command == "" {
		s.remember(p.Delivery)
		s.log.Info("nothing to run", "delivery", p.Delivery, "event", p.Event, "alert", p.Alert.ID)
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "nothing to run\n")
		return
	}
	// Room is Concurrency runs plus Queue waiting. A worker that has not yet received is not a full
	// queue: the count, not a rendezvous with a worker, decides. The send does not block while mu is
	// held; the buffer is that deep, so the default is only a disagreement between the two.
	if s.outstanding >= s.o.Concurrency+s.o.Queue {
		s.log.Warn("not taken", "reason", "queue full", "delivery", p.Delivery, "event", p.Event, "alert", p.Alert.ID)
		http.Error(w, "queue full, try later", http.StatusServiceUnavailable)
		return
	}
	select {
	case s.queue <- &job{p: p, body: body, command: command}:
		s.outstanding++
	default:
		s.log.Warn("not taken", "reason", "queue full", "delivery", p.Delivery, "event", p.Event, "alert", p.Alert.ID)
		http.Error(w, "queue full, try later", http.StatusServiceUnavailable)
		return
	}
	s.remember(p.Delivery)
	s.log.Info("run queued", "delivery", p.Delivery, "event", p.Event, "hook", p.Hook, "alert", p.Alert.ID)
	w.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(w, "queued\n")
}

// check validates a genuine payload against what the harness needs and against the headers; it
// returns the reason to refuse, or "".
func (s *Server) check(p Payload, h http.Header) string {
	switch {
	case p.Delivery == "":
		return "no delivery"
	case len(p.Delivery) > 64 || strings.ContainsFunc(p.Delivery, func(r rune) bool { return !plain(r) }):
		return "bad delivery"
	case p.Event != "firing" && p.Event != "resolved":
		return "unknown event"
	case p.At.IsZero():
		return "no at"
	case h.Get("X-Beacon-Delivery") != "" && h.Get("X-Beacon-Delivery") != p.Delivery:
		return "delivery header differs from the body"
	case h.Get("X-Beacon-Event") != "" && h.Get("X-Beacon-Event") != p.Event:
		return "event header differs from the body"
	case s.now().Sub(p.At) > maxAge:
		return "stale delivery"
	}
	return ""
}

// plain says whether r may be in a delivery id, which names a file: letters, digits, dash, underscore, dot.
func plain(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_' || r == '.'
}

// remember marks a delivery as taken and forgets the ones older than seenFor. Called with mu held.
func (s *Server) remember(delivery string) {
	now := s.now()
	for id, at := range s.seen {
		if now.Sub(at) > seenFor {
			delete(s.seen, id)
		}
	}
	s.seen[delivery] = now
}

func (s *Server) worker() {
	defer s.wg.Done()
	for j := range s.queue {
		s.mu.Lock()
		abandon := s.abandon
		s.mu.Unlock()
		if abandon {
			s.log.Warn("run abandoned", "reason", "shutting down", "delivery", j.p.Delivery, "alert", j.p.Alert.ID)
		} else {
			s.run(j)
		}
		s.mu.Lock()
		s.outstanding--
		s.mu.Unlock()
	}
}

// run starts the program for a delivery and waits for it, up to the timeout.
func (s *Server) run(j *job) {
	s.prune()
	start := s.now()
	name := filepath.Join(s.o.LogDir, start.UTC().Format(logTime)+"-"+j.p.Delivery+".log")
	out, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640) //nolint:gosec // our own log directory
	if err != nil {
		s.log.Error("run not started", "reason", "cannot open the log", "delivery", j.p.Delivery, "err", err)
		return
	}
	defer func() { _ = out.Close() }()

	// Not CommandContext: on the timeout the whole process group must go, not the one process it would kill.
	cmd := exec.Command("/bin/sh", "-c", j.command) //nolint:gosec,noctx // the operator names the program; the timeout is handled below
	cmd.Stdin = bytes.NewReader(j.body)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = append(os.Environ(), Environment(j.p)...)
	setGroup(cmd)
	if err := cmd.Start(); err != nil {
		s.log.Error("run not started", "delivery", j.p.Delivery, "alert", j.p.Alert.ID, "err", err)
		fmt.Fprintf(out, "--- beacon-oncall: not started: %v\n", err)
		return
	}
	s.mu.Lock()
	s.running[cmd] = struct{}{}
	s.mu.Unlock()
	s.log.Info("run started", "delivery", j.p.Delivery, "event", j.p.Event, "hook", j.p.Hook, "alert", j.p.Alert.ID,
		"pid", cmd.Process.Pid, "log", name)

	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	timedOut := false
	timer := time.NewTimer(s.o.Timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		timedOut = true
		terminate(cmd)
		select {
		case <-done:
		case <-time.After(killGrace):
			kill(cmd)
			<-done
		}
	}
	// The program has exited, but a child of its own may be in the group still (a shell that ignored
	// the termination signal, a background job): the run is over, and so is its group.
	kill(cmd)
	s.mu.Lock()
	delete(s.running, cmd)
	s.mu.Unlock()

	took := s.now().Sub(start).Round(time.Millisecond)
	code := -1
	how := "no exit status"
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
		how = fmt.Sprintf("exit %d", code)
		if code < 0 {
			how = cmd.ProcessState.String()
		}
	}
	var exited *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exited) {
		s.log.Warn("run wait failed", "delivery", j.p.Delivery, "err", waitErr)
	}
	if timedOut {
		how += fmt.Sprintf(", killed after the %s timeout", s.o.Timeout)
	}
	fmt.Fprintf(out, "\n--- beacon-oncall: %s after %s\n", how, took)
	s.log.Info("run ended", "delivery", j.p.Delivery, "event", j.p.Event, "alert", j.p.Alert.ID, "exit", code,
		"timed_out", timedOut, "duration", took, "log", name)
}

// prune removes the logs older than Keep.
func (s *Server) prune() {
	if s.o.Keep <= 0 {
		return
	}
	entries, err := os.ReadDir(s.o.LogDir)
	if err != nil {
		return
	}
	cutoff := s.now().Add(-s.o.Keep)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.o.LogDir, e.Name())); err == nil {
			s.log.Info("old log removed", "log", e.Name())
		}
	}
}

// Shutdown takes no more deliveries, lets the queued and running programs finish, and when ctx ends
// first, drops what is still queued and kills what still runs.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.queue)
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}
	s.mu.Lock()
	s.abandon = true
	for cmd := range s.running {
		kill(cmd)
	}
	s.mu.Unlock()
	<-done
	return ctx.Err()
}

// Environment is what the program learns about the alert, beside the payload on its standard input.
// Every variable is present, empty when Beacon sent nothing for it. Control characters are removed
// from the values; Beacon has already cleaned the text, this only makes sure.
func Environment(p Payload) []string {
	service := p.Alert.Service
	if service == "" {
		service = strings.Join(p.Alert.Services, ",")
	}
	pairs := [][2]string{
		{"BEACON_EVENT", p.Event},
		{"BEACON_DELIVERY", p.Delivery},
		{"BEACON_HOOK", p.Hook},
		{"BEACON_ALERT_ID", p.Alert.ID},
		{"BEACON_ALERT_TITLE", p.Alert.Title},
		{"BEACON_ALERT_SERVICE", service},
		{"BEACON_ALERT_SEVERITY", p.Alert.Severity},
		{"BEACON_ALERT_STATUS", p.Alert.Status},
		{"BEACON_ALERT_URL", p.Links.Alert},
		{"BEACON_EVIDENCE_URL", p.Links.Evidence},
		{"BEACON_TIMELINE_URL", p.Links.Timeline},
		{"BEACON_MCP_URL", p.Links.MCP},
		{"BEACON_RUNBOOK_URL", p.Links.Runbook},
	}
	env := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		env = append(env, kv[0]+"="+printable(kv[1]))
	}
	return env
}

// printable drops control characters and cuts the value to a sane length for an environment variable.
func printable(s string) string {
	const limit = 2000
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if b.Len()+len(string(r)) > limit {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
