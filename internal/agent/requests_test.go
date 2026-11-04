package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/targets"
)

// lockedBuffer takes log lines from several goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// quick shortens the poller's waits for a test and restores them after.
func quick(t *testing.T) {
	t.Helper()
	retry, missing, backoff := pollRetry, pollMissing, pollBackoff
	pollRetry, pollMissing, pollBackoff = 10*time.Millisecond, 60*time.Millisecond, []time.Duration{20 * time.Millisecond, 40 * time.Millisecond}
	t.Cleanup(func() { pollRetry, pollMissing, pollBackoff = retry, missing, backoff })
}

// start runs the agent (its cycle once an hour) until the test ends, with its log in a buffer.
func start(t *testing.T, a *Agent) (*lockedBuffer, func()) {
	t.Helper()
	logs := &lockedBuffer{}
	a.cfg.Log = slog.New(slog.NewTextHandler(logs, nil))
	a.cfg.Interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	stop := func() { cancel(); <-done }
	t.Cleanup(stop)
	return logs, stop
}

func (g *gateway) polled() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.polls
}

func (g *gateway) answer(id string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.answers[id]
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServesRequestsAndPostsResults(t *testing.T) {
	quick(t)
	a, gw, target := newAgent(t)
	gw.requests = make(chan string, 1)
	logs, stop := start(t, a)
	const id = "0123456789abcdef"
	gw.requests <- `[{"id":"` + id + `","groups":[{"targets":["` + target + `/broken"],"labels":{"module":"http_2xx","service":"web","severity":"critical"}}]}]`
	waitFor(t, "the answer", func() bool { return gw.answer(id) != "" })
	var ans targets.Answer
	if err := json.Unmarshal([]byte(gw.answer(id)), &ans); err != nil || len(ans.Results) != 1 {
		t.Fatalf("answer %s: %v", gw.answer(id), err)
	}
	r := ans.Results[0]
	if r.Target != target+"/broken" || r.Module != "http_2xx" || r.Success || r.Reason != "http_status" || r.HTTPStatus != 502 || r.Attempts != 1 || r.DurationSeconds < 0 {
		t.Fatalf("result %+v", r)
	}
	// the samples are pushed as the cycle's, with the same names and labels, besides the cycle's own push
	var pushes []string
	waitFor(t, "two pushes", func() bool { pushes = append(pushes, gw.take()...); return len(pushes) >= 2 })
	var onDemand string
	for _, p := range pushes {
		if !strings.Contains(p, "beacon_probe_targets") { // the cycle's push has the agent's own series
			onDemand = p
		}
	}
	for _, want := range []string{
		`probe_success{instance="` + target + `/broken",job="probe",module="http_2xx",service="web",severity="critical"} 0 `,
		`probe_failure_info{instance="` + target + `/broken",job="probe",module="http_2xx",service="web",severity="critical",reason="http_status"} 1 `,
		`probe_http_status_code{instance="` + target + `/broken",job="probe",module="http_2xx",service="web",severity="critical"} 502 `,
		`beacon_probe_attempts{instance="` + target + `/broken",job="probe",module="http_2xx",service="web",severity="critical"} 1 `,
	} {
		if !strings.Contains(onDemand, want) {
			t.Errorf("missing %q in\n%s", want, onDemand)
		}
	}
	if strings.Contains(onDemand, target+"/ok") || strings.Contains(onDemand, "node_load1") {
		t.Errorf("only the requested target, no host metrics:\n%s", onDemand)
	}
	stop()
	if !strings.Contains(logs.String(), "probed on request") || strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("log:\n%s", logs.String())
	}
}

// Against a gateway without the route (404) the agent says so once and asks again after pollMissing.
func TestOldGatewayIsAskedAgainLater(t *testing.T) {
	quick(t)
	a, gw, _ := newAgent(t)
	logs, stop := start(t, a)
	time.Sleep(250 * time.Millisecond)
	stop()
	if n := gw.polled(); n < 2 || n > 8 {
		t.Errorf("polls in 250ms with a 60ms pause: %d", n)
	}
	if n := strings.Count(logs.String(), "no probing on demand"); n != 1 {
		t.Errorf("said %d times:\n%s", n, logs.String())
	}
}

// Other errors back off.
func TestPollBacksOffOnErrors(t *testing.T) {
	quick(t)
	a, gw, _ := newAgent(t)
	gw.requestsStatus = 500
	logs, stop := start(t, a)
	time.Sleep(200 * time.Millisecond)
	stop()
	if n := gw.polled(); n < 2 || n > 12 {
		t.Errorf("polls in 200ms with 20ms then 40ms backoff: %d", n)
	}
	if n := strings.Count(logs.String(), "cannot poll the gateway"); n != 1 {
		t.Errorf("warned %d times:\n%s", n, logs.String())
	}
}
