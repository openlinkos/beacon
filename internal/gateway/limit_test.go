package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/testutil"
)

// TestChangeLimit: a token posts 60 change events a minute; the 61st gets 429 changes.too_many, is
// counted, and the next minute starts afresh. Another token is not held up by it.
func TestChangeLimit(t *testing.T) {
	e := testutil.New(t)
	mk := func(name string) string {
		s, _, err := e.Store.CreateToken(name, []string{"changes:write"}, time.Hour, "test")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	ci, flux := mk("ci"), mk("flux")
	now := time.Now()
	m := metrics.New("test")
	h := Handler(Deps{Auth: &api.Auth{Store: e.Store}, Catalog: func() *catalog.Catalog { return &catalog.Catalog{} },
		Changes: e.Bridge.RecordChange, Metrics: m, Now: func() time.Time { return now }})
	do := func(path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	for i := range changeLimit {
		if rec := do("/api/v1/changes", ci, `{"source":"ci","summary":"x"}`); rec.Code != http.StatusCreated {
			t.Fatalf("event %d: %d %s", i+1, rec.Code, rec.Body)
		}
	}
	code(t, do("/api/v1/changes", ci, `{"source":"ci","summary":"x"}`), http.StatusTooManyRequests, "changes.too_many")
	code(t, do("/api/v1/changes/flux", ci, `{"involvedObject":{"kind":"Kustomization","name":"apps"},"reason":"ReconciliationSucceeded","message":"m"}`),
		http.StatusTooManyRequests, "changes.too_many")
	code(t, do("/api/v1/changes", flux, `{"source":"flux","summary":"x"}`), http.StatusCreated, "")

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	for _, want := range []string{`beacon_gateway_changes_limited_total{token="ci"} 2`, `beacon_gateway_requests_total{route="changes",code="429"} 2`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("metrics lack %s", want)
		}
	}

	now = now.Add(changeWindow)
	code(t, do("/api/v1/changes", ci, `{"source":"ci","summary":"x"}`), http.StatusCreated, "")
	if got, err := e.Bridge.Changes(bridge.ChangeQuery{Limit: 1000}); err != nil || len(got) != changeLimit+2 {
		t.Errorf("stored %d events: %v", len(got), err)
	}
}
