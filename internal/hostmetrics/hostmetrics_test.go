package hostmetrics

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/probe"
)

func fake(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestCollect(t *testing.T) {
	root := fake(t, map[string]string{
		"stat": "cpu  300 0 200 10000 50 0 10 0 0 0\ncpu0 150 0 100 5000 25 0 5 0 0 0\ncpu1 150 0 100 5000 25 0 5 0 0 0\nintr 1\nbtime 1793400000\n",
		"meminfo": "MemTotal:        1999972 kB\nMemFree:          100000 kB\nMemAvailable:     512000 kB\nBuffers:           10 kB\n" +
			"Cached:          200000 kB\nSwapCached:            0 kB\nSwapTotal:             0 kB\nSwapFree:              0 kB\n",
		"loadavg": "0.17 0.08 0.02 1/123 4567\n",
		"net/dev": "Inter-|   Receive                                                |  Transmit\n" +
			" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
			"    lo: 100 1 0 0 0 0 0 0 100 1 0 0 0 0 0 0\n" +
			"  eth0: 123456 789 1 2 0 0 0 0 654321 987 3 4 0 0 0 0\n",
		"diskstats":       "   8       0 sda 100 0 2000 50 200 0 4000 80 0 1500 130 0 0 0 0\n   8       1 sda1 1 0 2 0 0 0 0 0 0 0 0 0 0 0 0\n",
		"pressure/cpu":    "some avg10=0.00 avg60=0.00 avg300=0.00 total=2500000\n",
		"pressure/memory": "some avg10=0.00 avg60=0.00 avg300=0.00 total=100\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=50\n",
		"self/mounts":     "/dev/vda3 / ext4 rw 0 0\nproc /proc proc rw 0 0\n/dev/vda3 / ext4 rw 0 0\n/dev/sdb /data\\040disk xfs rw 0 0\n",
	})
	c := &Collector{ProcRoot: root, Now: func() time.Time { return time.Unix(1793436000, 0) }}
	var buf bytes.Buffer
	if err := probe.WriteSamples(&buf, c.Collect()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"# TYPE node_cpu_seconds_total counter\n",
		`node_cpu_seconds_total{cpu="0",mode="user"} 1.5`,
		`node_cpu_seconds_total{cpu="1",mode="idle"} 50`,
		"node_boot_time_seconds 1.7934e+09",
		"node_memory_MemAvailable_bytes 5.24288e+08",
		"node_load1 0.17",
		`node_network_receive_bytes_total{device="eth0"} 123456`,
		`node_network_transmit_errs_total{device="eth0"} 3`,
		`node_disk_read_bytes_total{device="sda"} 1.024e+06`,
		`node_disk_io_time_seconds_total{device="sda"} 1.5`,
		"node_pressure_cpu_waiting_seconds_total 2.5",
		"node_pressure_memory_stalled_seconds_total 5e-05",
		"node_time_seconds 1.793436e+09",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, not := range []string{`device="lo"`, `device="sda1"`, `cpu=""`} {
		if strings.Contains(out, not) {
			t.Errorf("should not contain %s", not)
		}
	}
	ms := c.mounts()
	if len(ms) != 2 || ms[1].Point != "/data disk" || ms[1].Type != "xfs" {
		t.Errorf("mounts %+v", ms)
	}
}

func TestMissingFilesAreLeftOut(t *testing.T) {
	c := &Collector{ProcRoot: t.TempDir()}
	for _, s := range c.Collect() {
		if s.Name != "node_time_seconds" && !strings.HasPrefix(s.Name, "node_uname") && !strings.HasPrefix(s.Name, "node_timex") {
			t.Errorf("unexpected %s", s.Name)
		}
	}
}

func TestTextfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "heartbeat_lo1-backup.prom"),
		[]byte("# a comment\nbeacon_heartbeat_last_success_timestamp_seconds{heartbeat=\"lo1-backup\"} 1793436000\nnot a sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".heartbeat_tmp.123"), []byte("x 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := (&Collector{TextfileDir: dir}).Textfile()
	if len(got) != 1 || got[0] != `beacon_heartbeat_last_success_timestamp_seconds{heartbeat="lo1-backup"} 1793436000` {
		t.Fatalf("textfile %q", got)
	}
}
