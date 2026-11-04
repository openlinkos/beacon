package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/probe"
	"github.com/openlinkos/beacon/internal/targets"
	"github.com/openlinkos/beacon/internal/version"
)

// Probing on demand. Beside its cycle the agent keeps one request hanging on the gateway's
// GET /api/v1/agent/requests, which answers at once when an agent at the centre asks for a target this
// location has, and with an empty list when the wait passes. What it is handed it probes at once with
// its usual modules and settings, posts what it found to POST /api/v1/agent/requests/{id}, and pushes
// the samples exactly as its cycle does, so VictoriaMetrics and the rule engine see the fresh result too.
// A gateway without the routes answers 404: the agent says so once and asks again in an hour.

var (
	pollWait    = 25 * time.Second // how long the gateway may hold a poll
	pollMissing = time.Hour        // a gateway without the route is asked again after this
	pollRetry   = time.Second      // before the first cycle has fetched the modules
	pollBackoff = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute}
	pollAtOnce  = 4 // requests served at once
)

// ready reports whether the first cycle has fetched the modules, without which nothing can be probed.
func (a *Agent) ready() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.mods != nil
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// poll long-polls for requests until ctx ends and serves each in its own goroutine, a few at once.
func (a *Agent) poll(ctx context.Context) {
	fails, missingLogged := 0, false
	sem := make(chan struct{}, pollAtOnce)
	var wg sync.WaitGroup
	defer wg.Wait()
	for ctx.Err() == nil {
		if !a.ready() {
			sleep(ctx, pollRetry)
			continue
		}
		reqs, err := a.fetchRequests(ctx)
		var he *httpError
		switch {
		case ctx.Err() != nil:
			return
		case errors.As(err, &he) && he.code == http.StatusNotFound:
			if !missingLogged {
				a.cfg.Log.Info("the gateway has no probing on demand; asking again in an hour")
				missingLogged = true
			}
			sleep(ctx, pollMissing)
		case err != nil:
			fails++
			if fails == 1 || fails%20 == 0 {
				a.cfg.Log.Warn("cannot poll the gateway for requests", "err", err, "fails", fails)
			}
			sleep(ctx, pollBackoff[min(fails, len(pollBackoff))-1])
		default:
			fails, missingLogged = 0, false
			for _, r := range reqs {
				wg.Add(1)
				sem <- struct{}{}
				go func() {
					defer func() { <-sem; wg.Done() }()
					a.serve(ctx, r)
				}()
			}
		}
	}
}

// fetchRequests asks the gateway for pending requests, waiting on it for up to pollWait (less when the
// HTTP client's timeout is shorter).
func (a *Agent) fetchRequests(ctx context.Context) ([]targets.Request, error) {
	wait := pollWait
	if t := a.cfg.HTTP.Timeout; t > 0 && t-5*time.Second < wait {
		wait = max(t-5*time.Second, time.Second)
	}
	ctx, cancel := context.WithTimeout(ctx, wait+15*time.Second)
	defer cancel()
	b, err := a.get(ctx, "/api/v1/agent/requests?wait="+wait.String())
	if err != nil {
		return nil, err
	}
	var reqs []targets.Request
	if err := json.Unmarshal(b, &reqs); err != nil {
		return nil, fmt.Errorf("requests: %w", err)
	}
	return reqs, nil
}

// serve probes a request's targets, posts the results and pushes the samples.
func (a *Agent) serve(ctx context.Context, req targets.Request) {
	at := a.cfg.Now()
	jobs, results, attempts := a.probeGroups(ctx, req.Groups)
	ans := targets.Answer{Results: make([]targets.TargetResult, 0, len(jobs))}
	for i, j := range jobs {
		ans.Results = append(ans.Results, toResult(j, results[i], attempts[i]))
	}
	if err := a.postAnswer(ctx, req.ID, ans); err != nil && ctx.Err() == nil {
		a.cfg.Log.Warn("cannot post the results of a request", "request", req.ID, "err", err)
	}
	var b bytes.Buffer
	a.writeResults(&b, jobs, results, attempts, at)
	if err := a.push(ctx, b.Bytes()); err != nil && ctx.Err() == nil {
		a.cfg.Log.Warn("push of requested probes", "request", req.ID, "err", err)
	}
	a.cfg.Log.Info("probed on request", "request", req.ID, "targets", len(jobs))
}

// toResult is the wire form of a probe's result: the fields the evidence shows, taken from the samples.
func toResult(j job, r probe.Result, attempts int) targets.TargetResult {
	out := targets.TargetResult{Target: j.target, Module: j.labels["module"], Success: r.Success,
		DurationSeconds: math.Round(r.Duration.Seconds()*1000) / 1000, Attempts: attempts}
	if !r.Success {
		out.Reason = r.Reason
	}
	for _, s := range r.Samples {
		switch s.Name {
		case "probe_http_status_code":
			out.HTTPStatus = int(s.Value)
		case "probe_ssl_earliest_cert_expiry":
			out.CertExpiry = int64(s.Value)
		}
	}
	return out
}

func (a *Agent) postAnswer(ctx context.Context, id string, ans targets.Answer) error {
	body, err := json.Marshal(ans)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.Gateway+"/api/v1/agent/requests/"+url.PathEscape(id), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "beacon-probe/"+version.Version)
	resp, err := a.cfg.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return &httpError{path: "/api/v1/agent/requests/" + id, code: resp.StatusCode, status: resp.Status, body: strings.TrimSpace(string(b))}
	}
	return nil
}
