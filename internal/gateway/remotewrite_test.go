package gateway

import (
	"encoding/binary"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// writeRequest is a remote-write body, compressed, with one series for each list of label names and
// values, each with one sample.
func writeRequest(series ...[]string) string {
	var wr []byte
	for _, kv := range series {
		var ts []byte
		for i := 0; i+1 < len(kv); i += 2 {
			ts = appendField(ts, 1, appendField(appendField(nil, 1, []byte(kv[i])), 2, []byte(kv[i+1])))
		}
		smp := binary.AppendUvarint(nil, 1<<3|1)
		smp = binary.LittleEndian.AppendUint64(smp, math.Float64bits(1))
		smp = binary.AppendUvarint(smp, 2<<3)
		smp = binary.AppendUvarint(smp, 1793436000000)
		wr = appendField(wr, 1, appendField(ts, 2, smp))
	}
	return string(snappyLiterals(wr))
}

// writtenSeries are the label sets of a remote-write body as the gateway forwarded it, each as
// name=value pairs joined with commas.
func writtenSeries(t *testing.T, body string) []string {
	t.Helper()
	raw, err := snappyDecode([]byte(body), maxWriteBytes)
	if err != nil {
		t.Fatalf("forwarded body: %v", err)
	}
	var out []string
	_, err = filterWrite(raw, func(s textSample) string {
		var kv []string
		for _, l := range s.labels {
			kv = append(kv, l[0]+"="+l[1])
		}
		out = append(out, strings.Join(kv, ","))
		return ""
	}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *env) write(token, body string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/v1/write", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Encoding", "snappy")
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func TestSnappyDecode(t *testing.T) {
	for name, tc := range map[string]struct {
		in   []byte
		want string
	}{
		"literal":                    {[]byte{3, 2 << 2, 'a', 'b', 'c'}, "abc"},
		"copy with one byte":         {[]byte{9, 2 << 2, 'a', 'b', 'c', (6-4)<<2 | 1, 3}, "abcabcabc"},
		"copy with two bytes":        {[]byte{7, 0, 'x', (6-1)<<2 | 2, 1, 0}, "xxxxxxx"},
		"copy with four bytes":       {[]byte{4, 1 << 2, 'a', 'b', (2-1)<<2 | 3, 2, 0, 0, 0}, "abab"},
		"a longer literal":           {append([]byte{70, 60 << 2, 69}, []byte(strings.Repeat("z", 70))...), strings.Repeat("z", 70)},
		"what snappyLiterals writes": {snappyLiterals([]byte(strings.Repeat("0123456789", 20000))), strings.Repeat("0123456789", 20000)},
	} {
		got, err := snappyDecode(tc.in, 1<<20)
		if err != nil || string(got) != tc.want {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	for name, in := range map[string][]byte{
		"empty":               {},
		"short":               {5, 1 << 2, 'a'},
		"long":                {1, 1 << 2, 'a', 'b'},
		"offset before start": {4, 0, 'a', 1 /* a copy of 4 */, 2},
		"zero offset":         {5, 0, 'a', 1 /* a copy of 4 */, 0},
		"past its length":     {2, 0, 'a', 1 /* a copy of 4 */, 1},
		"gzip":                []byte("\x1f\x8b\x08\x00"),
	} {
		if got, err := snappyDecode(in, 1<<20); err == nil {
			t.Errorf("%s: decoded %q", name, got)
		}
	}
	if _, err := snappyDecode(snappyLiterals(make([]byte, 2<<20)), 1<<20); err == nil || !strings.Contains(err.Error(), "at most 1 MiB") {
		t.Errorf("a body larger than the limit once decompressed: %v", err)
	}
}

// TestRemoteWriteIsWrittenAgain: the gateway reads remote write and passes on what it wrote itself, each
// series' labels once, with what may not pass dropped and counted.
func TestRemoteWriteIsWrittenAgain(t *testing.T) {
	e := newEnv(t, false)
	body := writeRequest(
		[]string{"__name__", "node_load1", "instance", "a"},
		[]string{"__name__", "node_load1", "job", "probe", "job", "beacon"},                           // a label twice
		[]string{"__name__", "node_load1", "__name__", heartbeatMetric, "heartbeat", "worker-backup"}, // the name twice
		[]string{"__name__", heartbeatMetric, "heartbeat", "worker-backup"},
		[]string{"__name__", heartbeatMetric, "heartbeat", "edge-backup"},
		[]string{"__name__", heartbeatMetric, "heartbeat", "nas"},
	)
	code(t, e.write(e.host, body), http.StatusNoContent, "")
	r, fwd := e.vm.last()
	got := writtenSeries(t, fwd)
	want := []string{"__name__=node_load1,instance=a", "__name__=" + heartbeatMetric + ",heartbeat=worker-backup"}
	if !slices.Equal(got, want) {
		t.Fatalf("forwarded %q, want %q", got, want)
	}
	if r.Header.Get("Content-Encoding") != "snappy" || r.Header.Get("Content-Type") != "application/x-protobuf" {
		t.Errorf("forwarded with %v", r.Header)
	}
	m := scrape(e)
	for _, line := range []string{
		`beacon_gateway_samples_dropped_total{subject="worker-us",reason="labels"} 2`,
		`beacon_gateway_samples_dropped_total{subject="worker-us",reason="heartbeat"} 2`,
	} {
		if !strings.Contains(m, line) {
			t.Errorf("metrics lack %s:\n%s", line, m)
		}
	}

	// VictoriaMetrics' own protocol is refused, so that vmagent goes back to Prometheus'; what is not a
	// request is refused whole
	code(t, e.write(e.host, body, "Content-Encoding", "zstd"), http.StatusUnsupportedMediaType, "write.encoding")
	code(t, e.write(e.host, "\x28\xb5\x2f\xfd garbage"), http.StatusBadRequest, "bad_input")
	code(t, e.write(e.host, string(snappyLiterals([]byte{0x0b, 0xff}))), http.StatusBadRequest, "bad_input")
	// an empty body is an empty request
	code(t, e.write(e.host, ""), http.StatusNoContent, "")
}
