package gateway

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
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
		`node_load1{instance="a",instance="b"} 1`,
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

// TestRuleEngineNamesAreItsOwn: a probe or a host does not write what the rules read as the rule engine's
// own: a name with a colon (a recording rule's), ALERTS or ALERTS_FOR_STATE. The gateway drops and counts
// such a sample, on imports and on remote write; a heartbeat token writes its heartbeat alone.
func TestRuleEngineNamesAreItsOwn(t *testing.T) {
	e := newEnv(t, false)
	reserved := []string{
		`beacon:heartbeat_last_success{heartbeat="edge-backup"} 1793436000`,
		`beacon:service_failing_locations{service="git",failure_domain="origin-1"} 0`,
		`beacon:service_down 0`,
		`ALERTS{alertname="ServiceDown",alertstate="firing",service="git"} 1`,
		`ALERTS_FOR_STATE{alertname="HeartbeatMissing",heartbeat="nas"} 1793436000`,
	}
	body := "node_load1 1\n" + strings.Join(reserved, "\n") + "\nnode_load_x 2\n"
	for _, token := range []string{e.probe, e.host} {
		code(t, e.do("POST", "/api/v1/import/prometheus", token, body), http.StatusNoContent, "")
		if _, fwd := e.vm.last(); fwd != "node_load1 1\nnode_load_x 2\n" {
			t.Fatalf("forwarded %q", fwd)
		}
	}
	code(t, e.write(e.host, writeRequest(
		[]string{"__name__", "beacon:service_down", "service", "git"},
		[]string{"__name__", "ALERTS_FOR_STATE", "alertname", "ServiceDown"},
		[]string{"__name__", "node_load1"},
	)), http.StatusNoContent, "")
	if _, fwd := e.vm.last(); !slices.Equal(writtenSeries(t, fwd), []string{"__name__=node_load1"}) {
		t.Fatalf("remote write forwarded as %q", writtenSeries(t, fwd))
	}
	m := scrape(e)
	for _, line := range []string{
		`beacon_gateway_samples_dropped_total{subject="cdn-pre-us1",reason="reserved"} 5`,
		`beacon_gateway_samples_dropped_total{subject="worker-us",reason="reserved"} 7`,
	} {
		if !strings.Contains(m, line) {
			t.Errorf("metrics lack %s:\n%s", line, m)
		}
	}
	before := len(e.vm.reqs)
	code(t, e.do("POST", "/api/v1/import/prometheus", e.beat, `beacon:heartbeat_last_success{heartbeat="nas"} 1793436000`), http.StatusBadRequest, "heartbeat.metric")
	if len(e.vm.reqs) != before {
		t.Fatal("a refused heartbeat reached VictoriaMetrics")
	}
}

// TestTheGatewaysLabelsMayRepeat: a sample may give a label that the gateway sets itself more than once
// (an older probe agent adds job to a textfile line that has one), as the gateway's replaces them all;
// any other label given twice still drops the sample.
func TestTheGatewaysLabelsMayRepeat(t *testing.T) {
	e := newEnv(t, false)
	ok := `backup_jobs{job="nightly",job="node"} 3` + "\n" + `node_load1{host="a",location="b",location="c",host="e"} 1` + "\n"
	twice := `node_load5{instance="a",instance="b"} 1` + "\n" + heartbeatMetric + `{heartbeat="edge-backup",heartbeat="nas"} 1793436000` + "\n"
	code(t, e.do("POST", "/api/v1/import/prometheus", e.probe, ok+twice), http.StatusNoContent, "")
	if _, fwd := e.vm.last(); fwd != ok {
		t.Fatalf("a probe's import forwarded as %q", fwd)
	}
	// a host's are host and job alone
	code(t, e.do("POST", "/api/v1/import/prometheus", e.host, ok+twice), http.StatusNoContent, "")
	if _, fwd := e.vm.last(); fwd != `backup_jobs{job="nightly",job="node"} 3`+"\n" {
		t.Fatalf("a host's import forwarded as %q", fwd)
	}
	m := scrape(e)
	for _, line := range []string{
		`beacon_gateway_samples_dropped_total{subject="cdn-pre-us1",reason="labels"} 2`,
		`beacon_gateway_samples_dropped_total{subject="worker-us",reason="labels"} 3`,
	} {
		if !strings.Contains(m, line) {
			t.Errorf("metrics lack %s:\n%s", line, m)
		}
	}
	// remote write leaves them out, as the gateway sets them
	code(t, e.write(e.host, writeRequest([]string{"__name__", "backup_jobs", "job", "nightly", "instance", "a", "job", "node", "host", "x"})), http.StatusNoContent, "")
	if _, fwd := e.vm.last(); !slices.Equal(writtenSeries(t, fwd), []string{"__name__=backup_jobs,instance=a"}) {
		t.Fatalf("remote write forwarded as %q", writtenSeries(t, fwd))
	}
	// a heartbeat token's are job, heartbeat, source and sender
	line := heartbeatMetric + `{heartbeat="a",heartbeat="b",job="x",job="y"} 1793436000` + "\n"
	code(t, e.do("POST", "/api/v1/import/prometheus", e.beat, line), http.StatusNoContent, "")
	if _, fwd := e.vm.last(); fwd != line {
		t.Fatalf("a heartbeat forwarded as %q", fwd)
	}
	code(t, e.do("POST", "/api/v1/import/prometheus", e.beat, heartbeatMetric+`{instance="a",instance="b"} 1793436000`), http.StatusBadRequest, "bad_input")
}

// TestLabelsPerSampleAreBounded: a sample has at most maxLabels labels besides its name; one with more is
// dropped and counted, and checking it takes no longer than reading it.
func TestLabelsPerSampleAreBounded(t *testing.T) {
	e := newEnv(t, false)
	line := func(n int) string {
		var b strings.Builder
		b.WriteString("m{")
		for i := range n {
			fmt.Fprintf(&b, `l%d="v",`, i)
		}
		b.WriteString("} 1\n")
		return b.String()
	}
	code(t, e.do("POST", "/api/v1/import/prometheus", e.host, line(maxLabels)+line(maxLabels+1)+line(5000)), http.StatusNoContent, "")
	if _, fwd := e.vm.last(); fwd != line(maxLabels) {
		t.Fatalf("forwarded %d bytes", len(fwd))
	}
	series := func(n int) []string {
		kv := []string{"__name__", "m"}
		for i := range n {
			kv = append(kv, fmt.Sprintf("l%d", i), "v")
		}
		return kv
	}
	body := writeRequest(series(maxLabels), series(maxLabels+1), series(40_000))
	start := time.Now()
	code(t, e.write(e.host, body), http.StatusNoContent, "")
	if took := time.Since(start); took > time.Second {
		t.Errorf("a series of 40,000 labels took %v", took)
	}
	if _, fwd := e.vm.last(); len(writtenSeries(t, fwd)) != 1 {
		t.Fatalf("forwarded %d series", len(writtenSeries(t, fwd)))
	}
	if m := scrape(e); !strings.Contains(m, `beacon_gateway_samples_dropped_total{subject="worker-us",reason="too_many_labels"} 4`) {
		t.Errorf("metrics:\n%s", m)
	}
	wide := strings.TrimPrefix(line(maxLabels+1), "m")
	code(t, e.do("POST", "/api/v1/import/prometheus", e.beat, heartbeatMetric+wide), http.StatusBadRequest, "bad_input")
}

func TestDuplicate(t *testing.T) {
	many := textSample{}
	for i := range 40_000 {
		many.labels = append(many.labels, [2]string{fmt.Sprintf("l%d", i), "v"})
	}
	start := time.Now()
	if many.duplicate(nil) {
		t.Fatal("distinct labels read as given twice")
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("40,000 labels took %v", took)
	}
	forced := []string{"host", "job"}
	for _, s := range []textSample{many, {labels: [][2]string{{"a", "1"}}}} {
		s.labels = append(s.labels, [2]string{"job", "x"}, [2]string{"job", "y"})
		if !s.duplicate(nil) || s.duplicate(forced) {
			t.Errorf("%d labels, job twice", len(s.labels))
		}
		s.labels = append(s.labels, [2]string{"a", "2"}, [2]string{"l0", "w"})
		if !s.duplicate(forced) {
			t.Errorf("%d labels, a or l0 twice", len(s.labels))
		}
	}
}
