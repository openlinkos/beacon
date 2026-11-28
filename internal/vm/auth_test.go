package vm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBasicAuthGoesWithEveryQuery: a client given VictoriaMetrics' credentials sends them, and the
// server's refusal without them is an error.
func TestBasicAuthGoesWithEveryQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "beacon" || p != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL, nil).Query(context.Background(), "up"); err == nil {
		t.Fatal("a query without the credentials went through")
	}
	c := New(srv.URL, &http.Client{Transport: BasicAuth(nil, "beacon", "s3cret")})
	if _, err := c.Query(context.Background(), "up"); err != nil {
		t.Fatalf("a query with the credentials: %v", err)
	}
	if BasicAuth(http.DefaultTransport, "", "") != http.DefaultTransport {
		t.Fatal("no username should leave the transport as it is")
	}
}
