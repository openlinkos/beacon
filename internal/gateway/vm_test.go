package gateway

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestWithVictoriaMetrics writes through the gateway into a real VictoriaMetrics and reads back what it
// stored: the labels the gateway puts on a write win over the same labels in the body, on imports and
// on remote write; and nothing a probe, a host or a heartbeat token sends can name another metric than
// the gateway read. Set BEACON_TEST_VICTORIAMETRICS to a victoria-metrics binary to run it.
func TestWithVictoriaMetrics(t *testing.T) {
	bin := os.Getenv("BEACON_TEST_VICTORIAMETRICS")
	if bin == "" {
		t.Skip("set BEACON_TEST_VICTORIAMETRICS to a victoria-metrics binary")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	cmd := exec.Command(bin, "-httpListenAddr="+addr, "-storageDataPath="+t.TempDir(), "-retentionPeriod=100y", "-loggerLevel=ERROR")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	base := "http://" + addr
	for i := 0; ; i++ {
		if r, err := http.Get(base + "/health"); err == nil {
			_ = r.Body.Close()
			if r.StatusCode == http.StatusOK {
				break
			}
		}
		if i > 100 {
			t.Fatal("VictoriaMetrics did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// VictoriaMetrics itself takes a __name__ label and a name quoted inside the braces as the metric
	post := func(path, body string) {
		t.Helper()
		r, err := http.Post(base+path, "text/plain", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
	}
	post("/api/v1/import/prometheus", "vmtest_outer{__name__=\"vmtest_inner\"} 1\n{\"vmtest_quoted\"} 1\n")

	e := newEnv(t, false)
	d := e.deps
	d.VM, _ = url.Parse(base)
	e.h = Handler(d)
	for _, c := range []struct {
		token, body string
	}{
		{e.host, `probe_success{job="probe",host="cdn-pre-us1",service="gw-host"} 1` + "\n" +
			`node_load1{__name__="probe_success",service="gw-name"} 0` + "\n" +
			`{"probe_success",service="gw-quoted"} 0` + "\n" +
			heartbeatMetric + `{heartbeat="nas"} 9999999999` + "\n"},
		{e.probe, heartbeatMetric + `{heartbeat="worker-backup"} 9999999999` + "\n" + heartbeatMetric + `{heartbeat="edge-backup",host="x"} 1` + "\n"},
		{e.beat, heartbeatMetric + `{heartbeat="edge-backup",job="probe",sender="x"} 2` + "\n"},
	} {
		code(t, e.do("POST", "/api/v1/import/prometheus", c.token, c.body), http.StatusNoContent, "")
	}
	code(t, e.write(e.host, writeRequest(
		[]string{"__name__", "probe_success", "job", "probe", "host", "cdn-pre-us1", "service", "gw-rw"},
		[]string{"__name__", "node_load1", "__name__", "probe_success", "service", "gw-rw-twice"},
		[]string{"__name__", heartbeatMetric, "heartbeat", "nas"},
	)), http.StatusNoContent, "")

	if r, err := http.Get(base + "/internal/force_flush"); err == nil {
		_ = r.Body.Close()
	}
	want := []string{
		"__name__=" + heartbeatMetric + ",heartbeat=edge-backup,host=cdn-pre-us1,job=probe,location=cdn-pre-us1,network=CN2 GIA,region=overseas",
		"__name__=" + heartbeatMetric + ",heartbeat=nas,job=heartbeat,sender=nas,source=heartbeat",
		"__name__=probe_success,host=worker-us,job=host,service=gw-host",
		"__name__=probe_success,host=worker-us,job=host,service=gw-rw",
		"__name__=vmtest_inner",
		"__name__=vmtest_quoted",
	}
	var got []string
	for i := 0; i < 50 && len(got) < len(want); i++ {
		time.Sleep(100 * time.Millisecond)
		got = export(t, base)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("VictoriaMetrics holds\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// export is every series VictoriaMetrics holds, as sorted name=value pairs.
func export(t *testing.T, base string) []string {
	t.Helper()
	r, err := http.PostForm(base+"/api/v1/export", url.Values{"match[]": {`{__name__=~".+"}`}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	var out []string
	sc := bufio.NewScanner(r.Body)
	for sc.Scan() {
		var line struct {
			Metric map[string]string `json:"metric"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		var kv []string
		for k, v := range line.Metric {
			kv = append(kv, k+"="+v)
		}
		slices.Sort(kv)
		out = append(out, strings.Join(kv, ","))
	}
	slices.Sort(out)
	return slices.Compact(out)
}
