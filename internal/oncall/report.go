package oncall

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/openlinkos/beacon/internal/version"
)

const (
	runRunning    = "running"
	runSucceeded  = "succeeded"
	runFailed     = "failed"
	runTimedOut   = "timed_out"
	runNotStarted = "not_started"

	reportAttempts = 3
	reportBudget   = 5 * time.Second
)

type runReport struct {
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

func executionResult(timedOut bool, code int) runReport {
	switch {
	case timedOut:
		return runReport{Status: runTimedOut}
	case code == 0:
		return runReport{Status: runSucceeded, ExitCode: &code}
	case code > 0:
		return runReport{Status: runFailed, ExitCode: &code}
	default:
		return runReport{Status: runFailed}
	}
}

type reportTask struct {
	alert, delivery string
	report          runReport
}

// runReporter holds two in-memory lanes. A blocked running report cannot stop a terminal report.
type runReporter struct {
	base   *url.URL
	token  string
	log    *slog.Logger
	client *http.Client

	running  chan reportTask
	terminal chan reportTask
	wg       sync.WaitGroup
	cancel   context.CancelFunc
}

func newRunReporter(o Options) (*runReporter, error) {
	if o.ReportURL == "" && o.ReportToken == "" {
		return nil, nil
	}
	if o.ReportURL == "" || o.ReportToken == "" {
		return nil, errors.New("the report URL and report token must be set together")
	}
	base, err := reportBase(o.ReportURL)
	if err != nil {
		return nil, err
	}
	if !validReportToken(o.ReportToken) {
		return nil, errors.New("the report token file must contain one bearer token")
	}
	if hmac.Equal(o.Secret, []byte(o.ReportToken)) {
		return nil, errors.New("the report token must be separate from the hook secret")
	}
	return &runReporter{
		base: base, token: o.ReportToken, log: o.Log,
		client: &http.Client{
			Timeout: 1500 * time.Millisecond,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		running: make(chan reportTask, o.Concurrency+o.Queue), terminal: make(chan reportTask, o.Concurrency+o.Queue),
	}, nil
}

func reportBase(raw string) (*url.URL, error) {
	invalid := errors.New("the report URL must be an absolute HTTPS base URL or an HTTP loopback base URL, without credentials, a query or a fragment")
	base, err := url.Parse(raw)
	if err != nil || base.Hostname() == "" || base.User != nil || base.RawPath != "" || strings.HasSuffix(base.Host, ":") ||
		strings.ContainsAny(raw, "?#\\") || strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return nil, invalid
	}
	if port := base.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return nil, invalid
		}
	}
	host := base.Hostname()
	addr, ipErr := netip.ParseAddr(host)
	if strings.HasPrefix(base.Host, "[") {
		if ipErr != nil || !addr.Is6() {
			return nil, invalid
		}
	} else if strings.ContainsAny(host, ":[]") {
		return nil, invalid
	}
	if base.Scheme == "https" {
		return base, nil
	}
	if ipErr != nil || base.Scheme != "http" || !addr.Unmap().IsLoopback() {
		return nil, invalid
	}
	return base, nil
}

// A bearer token uses the RFC 6750 alphabet with optional trailing padding.
func validReportToken(token string) bool {
	core := strings.TrimRight(token, "=")
	return len(token) <= 4096 && core != "" && !strings.ContainsFunc(core, func(r rune) bool {
		return !plain(r) && !strings.ContainsRune("~+/", r)
	})
}

func (r *runReporter) start(concurrency int, finished func()) {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Go(func() {
		for task := range r.running {
			r.deliver(ctx, task)
		}
	})
	for range concurrency {
		r.wg.Go(func() {
			for task := range r.terminal {
				r.deliver(ctx, task)
				finished()
			}
		})
	}
}

func (r *runReporter) close() {
	close(r.running)
	close(r.terminal)
	r.wg.Wait()
	r.cancel()
}

func validReportID(id string) bool {
	return id != "" && id != "." && id != ".." && len(id) <= 64 && !strings.ContainsFunc(id, func(c rune) bool { return !plain(c) })
}

func (r *runReporter) queueRunning(p Payload) {
	if !validReportID(p.Alert.ID) || !validReportID(p.Delivery) {
		return // queueTerminal logs the invalid identifiers once
	}
	task := reportTask{alert: p.Alert.ID, delivery: p.Delivery, report: runReport{Status: runRunning}}
	select {
	case r.running <- task:
	default:
		r.log.Warn("run report queue full", "alert", task.alert, "delivery", task.delivery, "status", runRunning)
	}
}

func (r *runReporter) queueTerminal(p Payload, report runReport) bool {
	if !validReportID(p.Alert.ID) || !validReportID(p.Delivery) {
		r.log.Warn("run report skipped: invalid identifiers", "status", report.Status)
		return false
	}
	// Admission reserves a slot until the terminal report is attempted. The channel is as deep as
	// the admission limit, so the execution worker can always enqueue the final state.
	r.terminal <- reportTask{alert: p.Alert.ID, delivery: p.Delivery, report: report}
	return true
}

func (r *runReporter) deliver(ctx context.Context, task reportTask) {
	if err := r.send(ctx, task); err != nil {
		r.log.Warn("run report failed", "alert", task.alert, "delivery", task.delivery, "status", task.report.Status, "err", err)
	}
}

func (r *runReporter) send(parent context.Context, task reportTask) error {
	ctx, cancel := context.WithTimeout(parent, reportBudget)
	defer cancel()
	target := r.base.JoinPath("api", "v1", "alerts", task.alert, "agent-runs", task.delivery).String()
	body, _ := json.Marshal(task.report)
	var err error
	for attempt := range reportAttempts {
		if ctx.Err() != nil {
			return errors.New("report canceled")
		}
		var retry bool
		var delay time.Duration
		retry, delay, err = r.request(ctx, target, body)
		if err == nil || !retry || attempt+1 == reportAttempts {
			break
		}
		delay = max(delay, time.Duration(attempt+1)*100*time.Millisecond)
		until, _ := ctx.Deadline()
		if time.Until(until) <= delay {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return errors.New("report canceled")
		}
	}
	return err
}

func (r *runReporter) request(ctx context.Context, target string, body []byte) (retry bool, delay time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(body))
	if err != nil {
		return false, 0, errors.New("invalid report request")
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := r.client.Do(req)
	if err != nil {
		return true, 0, errors.New("report request failed")
	}
	_ = resp.Body.Close()
	status := resp.StatusCode
	if status/100 == 2 {
		return false, 0, nil
	}
	retry = status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status/100 == 5
	if retry {
		delay = reportRetryAfter(resp.Header.Get("Retry-After"))
	}
	return retry, delay, fmt.Errorf("report HTTP %d", status)
}

func reportRetryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseUint(value, 10, 31); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(time.Until(date), 0)
	}
	return 0
}
