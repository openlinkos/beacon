package gateway

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// TestMachinesWriteOnlyTheirHeartbeats: a probe or a host writes a heartbeat's sample only for a heartbeat
// the catalogue gives its machine; the gateway drops and counts the others and passes the rest on. A
// heartbeat that names no machine is its own heartbeat token's alone.
func TestMachinesWriteOnlyTheirHeartbeats(t *testing.T) {
	e := newEnv(t, false)
	beat := func(name string) string { return heartbeatMetric + `{heartbeat="` + name + `"} 1793436000` + "\n" }
	body := "probe_success 1\n" + beat("edge-backup") + beat("worker-backup") + beat("nas") + beat("unlisted") +
		heartbeatMetric + " 1793436000\n"
	code(t, e.gzipImport(e.probe, body), http.StatusNoContent, "")
	if _, fwd := e.vm.last(); fwd != "probe_success 1\n"+beat("edge-backup") {
		t.Fatalf("a probe's import forwarded as %q", fwd)
	}
	code(t, e.do("POST", "/api/v1/import/prometheus", e.host, body), http.StatusNoContent, "")
	if _, fwd := e.vm.last(); fwd != "probe_success 1\n"+beat("worker-backup") {
		t.Fatalf("a host's import forwarded as %q", fwd)
	}
	code(t, e.write(e.probe, writeRequest([]string{"__name__", heartbeatMetric, "heartbeat", "worker-backup"},
		[]string{"__name__", heartbeatMetric, "heartbeat", "edge-backup"})), http.StatusNoContent, "")
	if _, fwd := e.vm.last(); !slices.Equal(writtenSeries(t, fwd), []string{"__name__=" + heartbeatMetric + ",heartbeat=edge-backup"}) {
		t.Fatalf("a probe's remote write forwarded as %q", writtenSeries(t, fwd))
	}
	m := scrape(e)
	for _, line := range []string{
		`beacon_gateway_samples_dropped_total{subject="cdn-pre-us1",reason="heartbeat"} 5`,
		`beacon_gateway_samples_dropped_total{subject="worker-us",reason="heartbeat"} 4`,
	} {
		if !strings.Contains(m, line) {
			t.Errorf("metrics lack %s:\n%s", line, m)
		}
	}
	// the heartbeat's own token still writes it
	code(t, e.do("POST", "/api/v1/import/prometheus", e.beat, beat("nas")), http.StatusNoContent, "")
}

// TestTheNameIsBeforeTheBraces: a sample names its metric before the braces only. VictoriaMetrics would
// take a __name__ label, or a quoted name inside the braces, as the metric; the gateway drops such a line
// from a probe or a host, and refuses a heartbeat token's import with one.
func TestTheNameIsBeforeTheBraces(t *testing.T) {
	e := newEnv(t, false)
	odd := []string{
		`node_load1{__name__="probe_success"} 1`,
		`{__name__="probe_success",service="git"} 0`,
		`{"probe_success",service="git"} 0`,
		`node_load1{"__name__"="probe_success"} 1`,
		`node_load1 1 {"probe_success"} 0`,
		`node_load1{job="a",job="b"} 1`,
		`node_load1{a=b} 1`,
		"node_load1 1\rprobe_success 0",
		"node_load1{a=\"" + strings.Repeat("x", maxImportLine) + "\"} 1",
	}
	body := "node_load1 1\n" + strings.Join(odd, "\n") + "\nnode_load5 2\n"
	for _, token := range []string{e.probe, e.host} {
		code(t, e.do("POST", "/api/v1/import/prometheus", token, body), http.StatusNoContent, "")
		if _, fwd := e.vm.last(); fwd != "node_load1 1\nnode_load5 2\n" {
			t.Fatalf("forwarded %q", fwd)
		}
	}
	m := scrape(e)
	for _, line := range []string{
		`beacon_gateway_samples_dropped_total{subject="worker-us",reason="name_label"} 1`,
		`beacon_gateway_samples_dropped_total{subject="worker-us",reason="labels"} 1`,
		`beacon_gateway_samples_dropped_total{subject="worker-us",reason="syntax"} 7`,
	} {
		if !strings.Contains(m, line) {
			t.Errorf("metrics lack %s:\n%s", line, m)
		}
	}
	before := len(e.vm.reqs)
	for want, line := range map[string]string{
		"heartbeat.metric": heartbeatMetric + `{__name__="probe_success"} 1`,
		"bad_input":        `{"` + heartbeatMetric + `"} 1`,
	} {
		code(t, e.do("POST", "/api/v1/import/prometheus", e.beat, line), http.StatusBadRequest, want)
	}
	if len(e.vm.reqs) != before {
		t.Fatal("a refused heartbeat reached VictoriaMetrics")
	}
}

func TestParseSample(t *testing.T) {
	for in, want := range map[string]string{
		`m 1`:                           "m",
		`m{} 1 1793436000000`:           "m",
		`m{a="b"} 1`:                    "m a=b",
		`m {a="b",} -1.5e3`:             "m a=b",
		`m{ a = "b" , c="d" } +Inf`:     "m a=b c=d",
		`m{a="x\"}\\",b="\n\q"} NaN`:    "m a=x\"}\\ b=\n\\q",
		"m{a=\"b\"}\t1\t17 # a comment": "m a=b",
		`ns:m_total{a="{b}"} 0`:         "ns:m_total a={b}",
		"  m 1":                         "m",
	} {
		s, ok := parseSample([]byte(in))
		if !ok {
			t.Errorf("%q not read", in)
			continue
		}
		got := s.name
		for _, l := range s.labels {
			got += " " + l[0] + "=" + l[1]
		}
		if got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{
		``, `m`, `m{a="b"}`, `1m 1`, `m{a="b" 1`, `m{a="b"c="d"} 1`, `m{"a"="b"} 1`, `m{a='b'} 1`,
		`m 1 2 3`, `m 1 x`, `m x`, `m{a="b"}1x`, `m 1#x`, `m{a="b\"} 1`, `m{1a="b"} 1`, `m-x 1`, `{"m"} 1`,
	} {
		if s, ok := parseSample([]byte(in)); ok {
			t.Errorf("%q read as %+v", in, s)
		}
	}
	if s, _ := parseSample([]byte("m{a=\"b\"} 1 17 # c")); s.end != len(`m{a="b"} 1 17`) {
		t.Errorf("a comment is not cut: end %d", s.end)
	}
}
