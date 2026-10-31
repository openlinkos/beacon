// Package hostmetrics reads a few host metrics from /proc, with the Prometheus Node Exporter's names, so
// that a probe machine needs no node_exporter: CPU time, memory, load, filesystems, network and disk
// traffic, pressure, time sync, and the heartbeat files of a textfile directory. It is a small subset on
// purpose: the agent runs on CDN machines with little memory to spare.
//
// The parsing reads files under a root directory (/proc normally), so it is tested anywhere; the few
// system calls (filesystem sizes, time sync, uname) are in the Linux file.
package hostmetrics

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/probe"
)

// Collector reads the metrics. ProcRoot is /proc and TextfileDir the heartbeat directory ("" for none).
type Collector struct {
	ProcRoot    string
	TextfileDir string
	Now         func() time.Time
}

// userHZ is the kernel's clock tick for /proc/stat (100 on every Linux the agent runs on).
const userHZ = 100

func (c *Collector) read(name string) []byte {
	b, err := os.ReadFile(filepath.Join(c.ProcRoot, name)) //nolint:gosec // under /proc
	if err != nil {
		return nil
	}
	return b
}

func lines(b []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func num(s string) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

func label(kv ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(kv[i+1])
		b.WriteString(kv[i] + `="` + v + `"`)
	}
	return b.String()
}

var cpuModes = []string{"user", "nice", "system", "idle", "iowait", "irq", "softirq", "steal"}

// Collect returns every metric it can read; what it cannot read is left out.
func (c *Collector) Collect() []probe.Sample {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	var out []probe.Sample
	add := func(name, typ, help, labels string, v float64) {
		out = append(out, probe.Sample{Name: name, Type: typ, Help: help, Labels: labels, Value: v})
	}
	add("node_time_seconds", "gauge", "System time in seconds since the epoch", "", float64(now().UnixNano())/1e9)

	for _, l := range lines(c.read("stat")) {
		f := strings.Fields(l)
		switch {
		case len(f) > 8 && strings.HasPrefix(f[0], "cpu") && f[0] != "cpu":
			for i, mode := range cpuModes {
				if v, ok := num(f[i+1]); ok {
					add("node_cpu_seconds_total", "counter", "Seconds the CPUs spent in each mode", label("cpu", f[0][3:], "mode", mode), v/userHZ)
				}
			}
		case len(f) == 2 && f[0] == "btime":
			if v, ok := num(f[1]); ok {
				add("node_boot_time_seconds", "gauge", "Node boot time, in unixtime", "", v)
			}
		}
	}

	for _, l := range lines(c.read("meminfo")) {
		key, rest, ok := strings.Cut(l, ":")
		f := strings.Fields(rest)
		if !ok || len(f) == 0 {
			continue
		}
		switch key {
		case "MemTotal", "MemFree", "MemAvailable", "Buffers", "Cached", "SwapTotal", "SwapFree":
			if v, ok := num(f[0]); ok {
				add("node_memory_"+key+"_bytes", "gauge", "Memory information field "+key+"_bytes", "", v*1024)
			}
		}
	}

	if f := strings.Fields(string(c.read("loadavg"))); len(f) >= 3 {
		for i, n := range []string{"node_load1", "node_load5", "node_load15"} {
			if v, ok := num(f[i]); ok {
				add(n, "gauge", strings.Replace(n, "node_load", "Load average over ", 1)+" minutes", "", v)
			}
		}
	}

	for _, l := range lines(c.read("net/dev")) {
		dev, rest, ok := strings.Cut(l, ":")
		dev = strings.TrimSpace(dev)
		f := strings.Fields(rest)
		if !ok || len(f) < 16 || dev == "lo" {
			continue
		}
		for _, x := range []struct {
			name string
			i    int
		}{{"receive_bytes", 0}, {"receive_packets", 1}, {"receive_errs", 2}, {"receive_drop", 3}, {"transmit_bytes", 8}, {"transmit_packets", 9}, {"transmit_errs", 10}, {"transmit_drop", 11}} {
			if v, ok := num(f[x.i]); ok {
				add("node_network_"+x.name+"_total", "counter", "Network device statistic "+x.name, label("device", dev), v)
			}
		}
	}

	for _, l := range lines(c.read("diskstats")) {
		f := strings.Fields(l)
		if len(f) < 14 || !diskRe.MatchString(f[2]) {
			continue
		}
		for _, x := range []struct {
			name string
			i    int
			mul  float64
		}{{"reads_completed", 3, 1}, {"read_bytes", 5, 512}, {"writes_completed", 7, 1}, {"written_bytes", 9, 512}, {"io_time_seconds", 12, 0.001}} {
			if v, ok := num(f[x.i]); ok {
				add("node_disk_"+x.name+"_total", "counter", "Disk statistic "+x.name, label("device", f[2]), v*x.mul)
			}
		}
	}

	for _, res := range []string{"cpu", "memory", "io"} {
		for _, l := range lines(c.read("pressure/" + res)) {
			f := strings.Fields(l)
			if len(f) < 5 || !strings.HasPrefix(f[4], "total=") {
				continue
			}
			v, ok := num(strings.TrimPrefix(f[4], "total="))
			if !ok {
				continue
			}
			name := map[string]string{"cpu": "node_pressure_cpu_waiting_seconds_total", "memory": "node_pressure_memory_waiting_seconds_total", "io": "node_pressure_io_waiting_seconds_total"}[res]
			if f[0] == "full" {
				name = strings.Replace(name, "waiting", "stalled", 1)
			}
			add(name, "counter", "Pressure stall time ("+res+", "+f[0]+")", "", v/1e6)
		}
	}

	out = append(out, c.filesystems()...)
	out = append(out, c.system()...)
	return out
}

// diskRe matches whole disks, not partitions or virtual devices.
var diskRe = regexp.MustCompile(`^(sd[a-z]+|vd[a-z]+|xvd[a-z]+|nvme\d+n\d+|mmcblk\d+)$`)

// fsTypes are the filesystems worth watching (no tmpfs, overlay, proc …).
var fsTypes = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true, "f2fs": true, "vfat": true}

// Mount is a mounted filesystem from /proc/self/mounts.
type Mount struct{ Device, Point, Type string }

// mounts lists the watched filesystems, each mount point once.
func (c *Collector) mounts() []Mount {
	var out []Mount
	seen := map[string]bool{}
	for _, l := range lines(c.read("self/mounts")) {
		f := strings.Fields(l)
		if len(f) < 3 || !fsTypes[f[2]] {
			continue
		}
		point := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(f[1])
		if seen[point] {
			continue
		}
		seen[point] = true
		out = append(out, Mount{Device: f[0], Point: point, Type: f[2]})
	}
	return out
}

var promLineRe = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*(\{[^}]*\})? [-+0-9.eEInfNa]+( -?[0-9]+)?$`)

// Textfile returns the sample lines of the *.prom files in the textfile directory (comments and anything
// that does not look like a sample are dropped, so one broken file cannot break the whole scrape).
func (c *Collector) Textfile() []string {
	if c.TextfileDir == "" {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(c.TextfileDir, "*.prom"))
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // inside the textfile directory
		if err != nil {
			continue
		}
		for _, l := range lines(b) {
			if promLineRe.MatchString(l) {
				out = append(out, l)
			}
		}
	}
	return out
}

// SelfRSS is the resident memory of this process (VmRSS of /proc/self/status), to keep an eye on how
// little the agent uses.
func (c *Collector) SelfRSS() (float64, bool) {
	for _, l := range lines(c.read("self/status")) {
		if v, ok := strings.CutPrefix(l, "VmRSS:"); ok {
			f := strings.Fields(v)
			if len(f) > 0 {
				if kb, ok := num(f[0]); ok {
					return kb * 1024, true
				}
			}
		}
	}
	return 0, false
}
