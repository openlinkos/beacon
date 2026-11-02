// Package agent is beacon-probe's push mode, the whole probe agent in one process: every interval it
// fetches what to check from the gateway (its targets and the module file, cached on disk for when the
// gateway cannot be reached), probes every target, reads the host's metrics and heartbeats, and pushes
// it all to the gateway in the Prometheus text format with timestamps. When a push fails, the batch
// waits in a queue on disk and goes first next time, so a probe that loses its path to the gateway
// fills the gap when the path comes back. The queue has a size limit; the oldest batches go first.
package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openlinkos/beacon/internal/hostmetrics"
	"github.com/openlinkos/beacon/internal/probe"
	"github.com/openlinkos/beacon/internal/targets"
	"github.com/openlinkos/beacon/internal/version"
)

// Config is the agent's setup.
type Config struct {
	Gateway  string // such as https://metrics.example.com
	Token    string
	StateDir string // cache and queue
	Interval time.Duration
	Parallel int   // probes at once
	MaxQueue int64 // bytes on disk
	Prober   *probe.Prober
	Host     *hostmetrics.Collector
	HTTP     *http.Client
	Log      *slog.Logger
	Now      func() time.Time
}

// Agent runs the cycle, and beside it the poll for requests to probe on demand (requests.go).
type Agent struct {
	cfg Config

	mu       sync.RWMutex // guards mods and settings, which the request poller reads
	mods     *probe.Modules
	groups   []targets.Group // the cycle's alone
	settings targets.Settings

	pushMu     sync.Mutex // one push and queue drain at a time
	pushErrors atomic.Int64
	lastPush   atomic.Int64 // unix seconds of the last successful push
}

// New creates an agent with defaults for what is unset.
func New(cfg Config) *Agent {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.Parallel <= 0 {
		cfg.Parallel = 16
	}
	if cfg.MaxQueue <= 0 {
		cfg.MaxQueue = 64 << 20
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Prober == nil {
		cfg.Prober = &probe.Prober{UserAgent: "beacon-probe/" + version.Version}
	}
	cfg.Gateway = strings.TrimSuffix(cfg.Gateway, "/")
	return &Agent{cfg: cfg}
}

func (a *Agent) queueDir() string { return filepath.Join(a.cfg.StateDir, "queue") }

// Run runs a cycle every interval until ctx ends, starting at once, and polls for requests to probe on
// demand beside it; it returns once both have stopped.
func (a *Agent) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.poll(ctx)
	}()
	defer wg.Wait()
	for {
		start := a.cfg.Now()
		if err := a.Cycle(ctx); err != nil && ctx.Err() == nil {
			a.cfg.Log.Warn("cycle", "err", err)
		}
		wait := a.cfg.Interval - a.cfg.Now().Sub(start)
		select {
		case <-ctx.Done():
			return
		case <-time.After(max(wait, time.Second)):
		}
	}
}

// Cycle fetches, probes, collects and pushes once.
func (a *Agent) Cycle(ctx context.Context) error {
	a.refresh(ctx)
	at := a.cfg.Now()
	var b bytes.Buffer
	a.probeAll(ctx, &b, at)
	a.host(&b, at)
	a.self(&b, at)
	return a.push(ctx, b.Bytes())
}

// ---- What to check

func (a *Agent) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.Gateway+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("User-Agent", "beacon-probe/"+version.Version)
	resp, err := a.cfg.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &httpError{path: path, code: resp.StatusCode, status: resp.Status, body: strings.TrimSpace(string(body))}
	}
	return body, nil
}

type httpError struct {
	path, status, body string
	code               int
}

func (e *httpError) Error() string { return fmt.Sprintf("%s: %s %s", e.path, e.status, e.body) }

// refresh fetches the targets and modules, falling back to the cache on disk, then to what it had.
func (a *Agent) refresh(ctx context.Context) {
	for _, f := range []struct {
		path, cache string
		use         func([]byte) error
		optional    bool // a gateway older than the agent may not have it: then the defaults, quietly
	}{
		{"/api/v1/modules", "modules.yml", func(b []byte) error {
			m, err := probe.ParseModules(b)
			if err == nil {
				a.mu.Lock()
				a.mods = m
				a.mu.Unlock()
			}
			return err
		}, false},
		{"/api/v1/targets", "targets.json", func(b []byte) error {
			var gs []targets.Group
			err := json.Unmarshal(b, &gs)
			if err == nil {
				a.groups = gs
			}
			return err
		}, false},
		{"/api/v1/agent", "agent.json", func(b []byte) error {
			var s targets.Settings
			err := json.Unmarshal(b, &s)
			if err == nil {
				a.mu.Lock()
				a.settings = s
				a.mu.Unlock()
			}
			return err
		}, true},
	} {
		cache := filepath.Join(a.cfg.StateDir, f.cache)
		b, err := a.get(ctx, f.path)
		var he *httpError
		if f.optional && errors.As(err, &he) && he.code == http.StatusNotFound {
			_ = f.use([]byte("{}"))
			continue
		}
		if err == nil {
			if err = f.use(b); err == nil {
				_ = writeAtomic(cache, b)
				continue
			}
		}
		a.cfg.Log.Warn("cannot fetch from the gateway; using the cached copy", "what", f.path, "err", err)
		if (f.cache == "modules.yml" && a.mods == nil) || (f.cache == "targets.json" && a.groups == nil) {
			if b, err := os.ReadFile(cache); err == nil { //nolint:gosec // our own state directory
				_ = f.use(b)
			}
		}
	}
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---- Probing

type job struct {
	labels map[string]string
	target string
}

// probeAll probes the cycle's targets and writes their samples.
func (a *Agent) probeAll(ctx context.Context, w *bytes.Buffer, at time.Time) {
	jobs, results, attempts := a.probeGroups(ctx, a.groups)
	a.writeResults(w, jobs, results, attempts, at)
}

// probeGroups probes every target of the groups, up to Parallel at once, and returns the jobs with each
// one's result and attempts. The cycle and requests to probe on demand share it.
func (a *Agent) probeGroups(ctx context.Context, groups []targets.Group) ([]job, []probe.Result, []int) {
	var jobs []job
	for _, g := range groups {
		for _, t := range g.Targets {
			jobs = append(jobs, job{labels: g.Labels, target: t})
		}
	}
	results := make([]probe.Result, len(jobs))
	attempts := make([]int, len(jobs))
	sem := make(chan struct{}, a.cfg.Parallel)
	var wg sync.WaitGroup
	for i, j := range jobs {
		m := a.module(j.labels["module"])
		if m == nil {
			results[i] = probe.Result{Reason: probe.ReasonConfig, Detail: "no module " + j.labels["module"]}
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			results[i], attempts[i] = a.run(ctx, m, j.target)
		}()
	}
	wg.Wait()
	return jobs, results, attempts
}

// writeResults writes the samples of probed jobs, the same names and labels however the probe was asked
// for, and logs the failures.
func (a *Agent) writeResults(w *bytes.Buffer, jobs []job, results []probe.Result, attempts []int, at time.Time) {
	for i, j := range jobs {
		r := results[i]
		if !r.Success {
			a.cfg.Log.Info("probe failed", "module", j.labels["module"], "target", j.target, "reason", r.Reason, "detail", r.Detail)
		}
		base := maps.Clone(j.labels)
		base["instance"], base["job"] = j.target, "probe"
		ok := 0.0
		if r.Success {
			ok = 1
		}
		writeSample(w, "probe_success", base, "", ok, at)
		writeSample(w, "probe_duration_seconds", base, "", r.Duration.Seconds(), at)
		if attempts[i] > 0 {
			writeSample(w, "beacon_probe_attempts", base, "", float64(attempts[i]), at)
		}
		for _, s := range r.Samples {
			writeSample(w, s.Name, base, s.Labels, s.Value, at)
		}
		if !r.Success {
			writeSample(w, "probe_failure_info", base, `reason="`+r.Reason+`"`, 1, at)
		}
	}
}

// module is the named module as this location runs it: with the location's least timeout from the
// gateway, when that is longer than the module's own. nil when there is no such module.
func (a *Agent) module(name string) *probe.Module {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.mods == nil || a.mods.Modules[name] == nil {
		return nil
	}
	m := a.mods.Modules[name]
	least := time.Duration(a.settings.ProbeTimeoutSeconds * float64(time.Second))
	if least > time.Duration(m.Timeout) {
		c := *m
		c.Timeout = probe.Duration(least)
		m = &c
	}
	return m
}

// retries is the location's retries from the gateway.
func (a *Agent) retries() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.settings.ProbeRetries
}

// run probes target with m, trying again at once, up to the location's retries, while it fails without
// an answer: a dropped connection on a lossy path, not a service that answered wrongly.
func (a *Agent) run(ctx context.Context, m *probe.Module, target string) (probe.Result, int) {
	r := a.cfg.Prober.Run(ctx, m, target)
	n, retries := 1, a.retries()
	for n <= retries && !r.Success && ctx.Err() == nil &&
		(r.Reason == probe.ReasonDNS || r.Reason == probe.ReasonConnect || r.Reason == probe.ReasonTimeout) {
		r = a.cfg.Prober.Run(ctx, m, target)
		n++
	}
	return r, n
}

// ---- Host, self

func (a *Agent) host(w *bytes.Buffer, at time.Time) {
	if a.cfg.Host == nil {
		return
	}
	base := map[string]string{"job": "node"}
	for _, s := range a.cfg.Host.Collect() {
		writeSample(w, s.Name, base, s.Labels, s.Value, at)
	}
	for _, l := range a.cfg.Host.Textfile() {
		writeTextLine(w, l, "node", at)
	}
}

func (a *Agent) self(w *bytes.Buffer, at time.Time) {
	base := map[string]string{"job": "probe-agent"}
	writeSample(w, "beacon_probe_build_info", base, `version="`+version.Version+`"`, 1, at)
	writeSample(w, "beacon_probe_targets", base, "", float64(countTargets(a.groups)), at)
	files, size := a.queued()
	writeSample(w, "beacon_probe_queue_batches", base, "", float64(len(files)), at)
	writeSample(w, "beacon_probe_queue_bytes", base, "", float64(size), at)
	writeSample(w, "beacon_probe_push_errors_total", base, "", float64(a.pushErrors.Load()), at)
	if lp := a.lastPush.Load(); lp > 0 {
		writeSample(w, "beacon_probe_last_push_timestamp_seconds", base, "", float64(lp), at)
	}
	if a.cfg.Host != nil {
		if rss, ok := a.cfg.Host.SelfRSS(); ok {
			writeSample(w, "process_resident_memory_bytes", base, "", rss, at)
		}
	}
}

// Status is the agent's own state for its local /metrics, a look by hand or an install check: when it
// last pushed, how many pushes failed since it started, and what waits in the queue.
func (a *Agent) Status() []probe.Sample {
	files, size := a.queued()
	out := []probe.Sample{
		{Name: "beacon_probe_push_errors_total", Help: "Pushes to the gateway that failed since the agent started", Type: "counter", Value: float64(a.pushErrors.Load())},
		{Name: "beacon_probe_queue_batches", Help: "Batches waiting on disk for the gateway", Type: "gauge", Value: float64(len(files))},
		{Name: "beacon_probe_queue_bytes", Help: "Bytes waiting on disk for the gateway", Type: "gauge", Value: float64(size)},
	}
	if lp := a.lastPush.Load(); lp > 0 {
		out = append(out, probe.Sample{Name: "beacon_probe_last_push_timestamp_seconds", Help: "When the gateway last accepted a push", Type: "gauge", Value: float64(lp)})
	}
	return out
}

func countTargets(gs []targets.Group) int {
	n := 0
	for _, g := range gs {
		n += len(g.Targets)
	}
	return n
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// writeSample writes name{base…,extra} value timestamp_ms.
func writeSample(w *bytes.Buffer, name string, base map[string]string, extra string, v float64, at time.Time) {
	w.WriteString(name)
	w.WriteByte('{')
	keys := slices.Sorted(maps.Keys(base))
	for i, k := range keys {
		if i > 0 {
			w.WriteByte(',')
		}
		w.WriteString(k + `="` + labelEscaper.Replace(base[k]) + `"`)
	}
	if extra != "" {
		if len(keys) > 0 {
			w.WriteByte(',')
		}
		w.WriteString(extra)
	}
	w.WriteString("} " + strconv.FormatFloat(v, 'g', -1, 64) + " " + strconv.FormatInt(at.UnixMilli(), 10) + "\n")
}

// writeTextLine adds the job label and a timestamp to a sample line from a textfile.
func writeTextLine(w *bytes.Buffer, line, job string, at time.Time) {
	f := strings.Fields(line)
	if len(f) < 2 {
		return
	}
	series, value := f[0], f[1]
	if i := strings.IndexByte(series, '{'); i >= 0 {
		inner := strings.TrimSuffix(series[i+1:], "}")
		if inner != "" {
			inner += ","
		}
		series = series[:i] + "{" + inner + `job="` + job + `"}`
	} else {
		series += `{job="` + job + `"}`
	}
	w.WriteString(series + " " + value + " " + strconv.FormatInt(at.UnixMilli(), 10) + "\n")
}

// ---- Pushing and the queue

func (a *Agent) send(ctx context.Context, gz []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.Gateway+"/api/v1/import/prometheus", bytes.NewReader(gz))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("User-Agent", "beacon-probe/"+version.Version)
	resp, err := a.cfg.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return &pushError{code: resp.StatusCode, msg: strings.TrimSpace(string(body))}
	}
	return nil
}

type pushError struct {
	code int
	msg  string
}

func (e *pushError) Error() string { return fmt.Sprintf("push: %d %s", e.code, e.msg) }

// permanent reports a refusal that retrying cannot fix (the batch is dropped, not queued): the token
// is wrong or the machine has expired.
func permanent(err error) bool {
	var pe *pushError
	return errors.As(err, &pe) && (pe.code == http.StatusUnauthorized || pe.code == http.StatusForbidden || pe.code == http.StatusBadRequest)
}

// push sends the batch; on failure it queues it. Then it sends what was queued, oldest first. One push
// at a time: the cycle's and those of requests to probe on demand take turns.
func (a *Agent) push(ctx context.Context, batch []byte) error {
	a.pushMu.Lock()
	defer a.pushMu.Unlock()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(batch)
	_ = zw.Close()
	err := a.send(ctx, gz.Bytes())
	if err != nil {
		a.pushErrors.Add(1)
		if !permanent(err) {
			a.enqueue(gz.Bytes())
		}
		return err
	}
	a.lastPush.Store(a.cfg.Now().Unix())
	files, _ := a.queued()
	for i, f := range files {
		if i >= 10 { // the rest next time, so a long outage does not delay a cycle much
			break
		}
		b, err := os.ReadFile(f) //nolint:gosec // our own queue
		if err == nil {
			err = a.send(ctx, b)
		}
		if err != nil && !permanent(err) {
			a.pushErrors.Add(1)
			return fmt.Errorf("queued batch: %w", err)
		}
		_ = os.Remove(f)
	}
	return nil
}

func (a *Agent) enqueue(gz []byte) {
	name := filepath.Join(a.queueDir(), strconv.FormatInt(a.cfg.Now().UnixNano(), 10)+".prom.gz")
	if err := writeAtomic(name, gz); err != nil {
		a.cfg.Log.Error("cannot queue a batch", "err", err)
		return
	}
	files, size := a.queued()
	for len(files) > 0 && size > a.cfg.MaxQueue {
		if st, err := os.Stat(files[0]); err == nil {
			size -= st.Size()
		}
		_ = os.Remove(files[0])
		files = files[1:]
		a.cfg.Log.Warn("queue full: dropped the oldest batch")
	}
}

// queued lists the queued batches, oldest first, and their total size.
func (a *Agent) queued() ([]string, int64) {
	files, _ := filepath.Glob(filepath.Join(a.queueDir(), "*.prom.gz"))
	sort.Strings(files) // names are unix nanoseconds of the same length for centuries
	var size int64
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			size += st.Size()
		}
	}
	return files, size
}
