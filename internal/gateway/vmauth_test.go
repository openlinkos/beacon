package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/openlinkos/beacon/internal/vm"
)

// TestBackendGetsItsOwnCredentials: what the gateway passes to VictoriaMetrics carries VictoriaMetrics'
// basic auth, never the caller's token.
func TestBackendGetsItsOwnCredentials(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	for _, tc := range []struct {
		transport http.RoundTripper
		want      string
	}{
		{nil, ""},
		{vm.BasicAuth(nil, "beacon", "s3cret"), "Basic YmVhY29uOnMzY3JldA=="},
	} {
		got = nil
		req := httptest.NewRequest("GET", "/api/v1/query?query=up", nil)
		req.Header.Set("Authorization", "Bearer the-callers-token")
		rec := httptest.NewRecorder()
		proxy(u, tc.transport).ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent || len(got) != 1 || got[0] != tc.want {
			t.Fatalf("with %v: %d, VictoriaMetrics saw %q, want %q", tc.transport, rec.Code, got, tc.want)
		}
	}
}
