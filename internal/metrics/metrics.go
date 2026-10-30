// Package metrics exposes metrics in the Prometheus text format. There are few metrics, so writing
// them by hand is simpler than pulling in client_golang (a dozen or so dependencies).
//
// Counters are kept here; everything else (catalogue series, bridge state) is read by callbacks at
// scrape time.
package metrics

import (
	"bufio"
	"fmt"
	"math"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Sample is a labelled value. Labels is produced with Labels().
type Sample struct {
	Labels string
	Value  float64
}

// Labels produces {k="v",...} in the given order, with the values escaped.
func Labels(kv ...string) string {
	if len(kv) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(kv[i])
		b.WriteString(`="`)
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(kv[i+1])
		b.WriteString(v)
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

type gauge struct {
	name, help, typ string
	fn              func() []Sample
}

type counter struct {
	help string
	vals sync.Map // labels → *atomic.Uint64
}

// Metrics is the registry behind /metrics.
type Metrics struct {
	version  string
	start    time.Time
	mu       sync.Mutex
	gauges   []gauge
	counters sync.Map // name → *counter
}

// New creates a registry. version appears in linkos_status_build_info.
func New(version string) *Metrics { return &Metrics{version: version, start: time.Now()} }

// Register adds a metric computed at scrape time. typ is gauge or counter.
func (m *Metrics) Register(name, help, typ string, fn func() []Sample) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gauges = append(m.gauges, gauge{name: name, help: help, typ: typ, fn: fn})
}

// Inc adds one to the counter name with the given labels (made with Labels).
func (m *Metrics) Inc(name, help, labels string) {
	v, _ := m.counters.LoadOrStore(name, &counter{help: help})
	c := v.(*counter)
	n, _ := c.vals.LoadOrStore(labels, new(atomic.Uint64))
	n.(*atomic.Uint64).Add(1)
}

func fmtFloat(v float64) string {
	if math.IsInf(v, 1) {
		return "+Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// ServeHTTP writes out all metrics.
func (m *Metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	bw := bufio.NewWriter(w)
	defer func() { _ = bw.Flush() }()

	var names []string
	m.counters.Range(func(k, _ any) bool { names = append(names, k.(string)); return true })
	sort.Strings(names)
	for _, name := range names {
		v, _ := m.counters.Load(name)
		c := v.(*counter)
		fmt.Fprintf(bw, "# HELP %s %s\n# TYPE %s counter\n", name, c.help, name)
		var keys []string
		c.vals.Range(func(k, _ any) bool { keys = append(keys, k.(string)); return true })
		sort.Strings(keys)
		for _, k := range keys {
			n, _ := c.vals.Load(k)
			fmt.Fprintf(bw, "%s%s %d\n", name, k, n.(*atomic.Uint64).Load())
		}
	}

	m.mu.Lock()
	gs := append([]gauge(nil), m.gauges...)
	m.mu.Unlock()
	for _, g := range gs {
		fmt.Fprintf(bw, "# HELP %s %s\n# TYPE %s %s\n", g.name, g.help, g.name, g.typ)
		for _, s := range g.fn() {
			fmt.Fprintf(bw, "%s%s %s\n", g.name, s.Labels, fmtFloat(s.Value))
		}
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Fprintf(bw, "# HELP linkos_status_build_info Version\n# TYPE linkos_status_build_info gauge\nlinkos_status_build_info%s 1\n", Labels("version", m.version, "go", runtime.Version()))
	fmt.Fprintf(bw, "# TYPE linkos_status_start_time_seconds gauge\nlinkos_status_start_time_seconds %d\n", m.start.Unix())
	fmt.Fprintf(bw, "# TYPE go_goroutines gauge\ngo_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(bw, "# TYPE go_memstats_heap_inuse_bytes gauge\ngo_memstats_heap_inuse_bytes %d\n", ms.HeapInuse)
	fmt.Fprintf(bw, "# TYPE go_memstats_sys_bytes gauge\ngo_memstats_sys_bytes %d\n", ms.Sys)
	if rss := residentBytes(); rss > 0 {
		fmt.Fprintf(bw, "# TYPE process_resident_memory_bytes gauge\nprocess_resident_memory_bytes %d\n", rss)
	}
}

// residentBytes reads /proc/self/statm (Linux only).
func residentBytes() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, _ := strconv.ParseInt(f[1], 10, 64)
	return pages * int64(os.Getpagesize())
}
