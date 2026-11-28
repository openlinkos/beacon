package gateway

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

func gzipped(s string) string {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return b.String()
}

func (e *env) gzipImport(token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/v1/import/prometheus", strings.NewReader(gzipped(body)))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

// TestWritesNameTheirKind: whatever job a machine claims, the gateway sets it to the kind of its token,
// on remote write and on imports, so rules can tell a probe's results from a host's or a heartbeat's.
func TestWritesNameTheirKind(t *testing.T) {
	e := newEnv(t, false)
	for _, tc := range []struct {
		path, token, want string
	}{
		{"/api/v1/write?extra_label=job=beacon", e.probe, "location=cdn-pre-us1|region=overseas|network=CN2 GIA|host=cdn-pre-us1|job=probe"},
		{"/api/v1/import/prometheus?extra_label=job=beacon", e.probe, "location=cdn-pre-us1|region=overseas|network=CN2 GIA|host=cdn-pre-us1|job=probe"},
		{"/api/v1/write?extra_label=job=probe", e.host, "host=worker-us|job=host"},
		{"/api/v1/import/prometheus?extra_label=job=probe", e.host, "host=worker-us|job=host"},
	} {
		code(t, e.do("POST", tc.path, tc.token, `probe_success{job="beacon"} 1`), http.StatusNoContent, "")
		r, _ := e.vm.last()
		if got := strings.Join(r.URL.Query()["extra_label"], "|"); got != tc.want {
			t.Errorf("%s: extra_label %s, want %s", tc.path, got, tc.want)
		}
	}
}

// TestHeartbeatTokenWritesItsOwnHeartbeat: a heartbeat token writes its heartbeat's sample and nothing
// else, labelled with the heartbeat it was enrolled for whatever the sample says.
func TestHeartbeatTokenWritesItsOwnHeartbeat(t *testing.T) {
	e := newEnv(t, false)
	line := "beacon_heartbeat_last_success_timestamp_seconds{heartbeat=\"someone-else\"} 1793436000\n"
	code(t, e.do("POST", "/api/v1/import/prometheus", e.beat, "# a comment\n\n"+line), http.StatusNoContent, "")
	r, body := e.vm.last()
	if got := strings.Join(r.URL.Query()["extra_label"], "|"); got != "job=heartbeat|heartbeat=nas|source=heartbeat|sender=nas" {
		t.Fatalf("extra_label %s", got)
	}
	if !strings.Contains(body, line) {
		t.Fatalf("forwarded %q", body)
	}
	code(t, e.gzipImport(e.beat, line), http.StatusNoContent, "")
	if r, body := e.vm.last(); body != line || r.Header.Get("Content-Encoding") != "" {
		t.Fatalf("a gzipped heartbeat forwarded as %q", body)
	}
	before := len(e.vm.reqs)
	for _, other := range []string{
		"probe_success{job=\"probe\",location=\"cdn-pre-us1\"} 1",
		"beacon_resource_expiry_timestamp_seconds{resource=\"example.com\"} 9999999999",
		line + "beacon_heartbeat_max_age_seconds{heartbeat=\"nas\"} 1e9",
		"beacon_heartbeat_last_success_timestamp_seconds_total 1",
	} {
		code(t, e.do("POST", "/api/v1/import/prometheus", e.beat, other), http.StatusBadRequest, "heartbeat.metric")
		code(t, e.gzipImport(e.beat, other), http.StatusBadRequest, "heartbeat.metric")
	}
	if len(e.vm.reqs) != before {
		t.Fatalf("a refused heartbeat reached VictoriaMetrics")
	}
	code(t, e.do("POST", "/api/v1/import/prometheus", e.beat, strings.Repeat(line, maxHeartbeatSamples+1)), http.StatusBadRequest, "import.too_large")
	code(t, e.do("POST", "/api/v1/write", e.beat, "x"), http.StatusForbidden, "auth.scope")
}

// TestHeartbeatTokenNeedsItsHeartbeat: a heartbeat token that names no heartbeat (made before tokens
// had to) cannot write, since it would speak for every heartbeat.
func TestHeartbeatTokenNeedsItsHeartbeat(t *testing.T) {
	vm := &seen{}
	u, _ := url.Parse(vm.server(t).URL)
	g := &gateway{Deps: Deps{Now: time.Now}, vm: proxy(u)}
	r := httptest.NewRequest("POST", "/api/v1/import/prometheus", strings.NewReader("beacon_heartbeat_last_success_timestamp_seconds 1\n"))
	rec := httptest.NewRecorder()
	g.importText(rec, r, store.Token{Name: "heartbeat-legacy", Scopes: []string{"heartbeat:write"}})
	code(t, rec, http.StatusForbidden, "heartbeat.unnamed")
	if r, _ := vm.last(); r != nil {
		t.Fatal("an unnamed heartbeat reached VictoriaMetrics")
	}
}

// TestImportsAreBounded: one import carries at most so many samples; beyond that it is refused (as
// permanently wrong, so a probe agent drops the batch rather than queue it for ever).
func TestImportsAreBounded(t *testing.T) {
	e := newEnv(t, false)
	var b strings.Builder
	for i := range maxImportSamples + 1 {
		fmt.Fprintf(&b, "s{i=\"%d\"} 1\n", i)
	}
	code(t, e.gzipImport(e.probe, b.String()), http.StatusBadRequest, "import.too_large")
	code(t, e.do("POST", "/api/v1/import/prometheus", e.host, b.String()), http.StatusBadRequest, "import.too_large")
	// at the limit it goes through, decompressed
	var ok strings.Builder
	for i := range maxImportSamples {
		fmt.Fprintf(&ok, "s{i=\"%d\"} 1\n", i)
	}
	code(t, e.gzipImport(e.probe, "# HELP s x\n"+ok.String()), http.StatusNoContent, "")
	if r, body := e.vm.last(); body != "# HELP s x\n"+ok.String() || r.Header.Get("Content-Encoding") != "" {
		t.Fatalf("forwarded %d bytes, Content-Encoding %q", len(body), r.Header.Get("Content-Encoding"))
	}
	code(t, e.gzipImport(e.probe, strings.Repeat("#", maxImportBytes+1)), http.StatusBadRequest, "import.too_large")
	// a body that says it is gzipped and is not, or another encoding, is refused
	r := httptest.NewRequest("POST", "/api/v1/import/prometheus", strings.NewReader("probe_success 1"))
	r.Header.Set("Authorization", "Bearer "+e.probe)
	r.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	code(t, rec, http.StatusBadRequest, "bad_input")
	r.Header.Set("Content-Encoding", "br")
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	code(t, rec, http.StatusBadRequest, "bad_input")
}
