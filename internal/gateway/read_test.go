package gateway

import (
	"net/http"
	"testing"
)

// TestReadsGoWhereTheirRouteSays: a metrics reader's request reaches VictoriaMetrics on the path of the
// route it matched, rebuilt from it; a name in the path that is not a plain name, such as one that
// spells out another path with dot segments or encoded slashes, is refused and goes nowhere.
func TestReadsGoWhereTheirRouteSays(t *testing.T) {
	e := newEnv(t, false)
	for path, want := range map[string]string{
		"/api/v1/query?query=up":        "/prefix/api/v1/query",
		"/api/v1/%71uery?query=up":      "/prefix/api/v1/query",
		"/api/v1/label/job/values":      "/prefix/api/v1/label/job/values",
		"/api/v1/label/__name__/values": "/prefix/api/v1/label/__name__/values",
		"/api/v1/status/tsdb":           "/prefix/api/v1/status/tsdb",
		"/api/v1/status/active_queries": "/prefix/api/v1/status/active_queries",
	} {
		code(t, e.do("GET", path, e.reader, ""), http.StatusNoContent, "")
		r, _ := e.vm.last()
		if r.URL.Path != want || r.URL.RawPath != "" {
			t.Errorf("%s reached %s (raw %q), want %s", path, r.URL.Path, r.URL.RawPath, want)
		}
	}
	before := len(e.vm.reqs)
	for _, path := range []string{
		"/api/v1/status/..%2F..%2Fimport",
		"/api/v1/status/..%2f..%2fwrite",
		"/api/v1/status/%2E%2E",
		"/api/v1/status/a%5Cb",
		"/api/v1/status/..%5C..%5Cimport",
		"/api/v1/label/..%2F..%2F..%2Fadmin%2Ftsdb%2Fdelete_series/values",
		"/api/v1/label/a%2Fb/values",
		"/api/v1/label/%2e%2e/values",
	} {
		rec := e.do("GET", path, e.reader, "")
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	if len(e.vm.reqs) != before {
		r, _ := e.vm.last()
		t.Fatalf("a refused read reached VictoriaMetrics: %s (raw %q)", r.URL.Path, r.URL.RawPath)
	}
}
