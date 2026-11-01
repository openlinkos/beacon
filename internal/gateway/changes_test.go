package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

// fluxSample is what Flux's notification-controller sends a generic provider, as its documentation
// shows it, with an Alert's eventMetadata naming the service.
const fluxSample = `{
  "involvedObject": {
    "kind": "Kustomization", "namespace": "flux-system", "name": "apps",
    "uid": "7d0cdc51-ddcf-4743-b223-83ca5c699632", "apiVersion": "kustomize.toolkit.fluxcd.io/v1", "resourceVersion": "56921"
  },
  "severity": "info",
  "timestamp": "2026-11-02T08:41:07Z",
  "message": "Deployment/apps/hub configured\nService/apps/hub unchanged",
  "reason": "ReconciliationSucceeded",
  "metadata": {
    "kustomize.toolkit.fluxcd.io/revision": "main@sha1:731f7eaddfb6af01cb2173e18f0f75b0ba780ef1",
    "service": "hub-linkos-io"
  },
  "reportingController": "kustomize-controller",
  "reportingInstance": "kustomize-controller-7f5c6f8b8d-4lr7x"
}`

func TestFluxChange(t *testing.T) {
	var ev fluxEvent
	if err := json.Unmarshal([]byte(fluxSample), &ev); err != nil {
		t.Fatal(err)
	}
	c := fluxChangeOf(ev, "ignored", "edge-1")
	if c.Source != "flux" || c.Kind != "ReconciliationSucceeded" || c.Object != "Kustomization/flux-system/apps" || c.Severity != "info" ||
		c.Revision != "main@sha1:731f7eaddfb6af01cb2173e18f0f75b0ba780ef1" || c.Service != "hub-linkos-io" || c.Machine != "edge-1" ||
		!c.Time.Equal(time.Date(2026, 11, 2, 8, 41, 7, 0, time.UTC)) || !strings.HasPrefix(c.Summary, "Deployment/apps/hub configured") {
		t.Fatalf("mapped: %+v", c)
	}
	ev.Metadata = map[string]string{"summary": "prod cluster"}
	ev.Reason = ""
	if c := fluxChangeOf(ev, "git-linkos-io", ""); c.Kind != "event" || c.Service != "git-linkos-io" || !strings.HasPrefix(c.Summary, "prod cluster: ") {
		t.Fatalf("without a reason, service from the address: %+v", c)
	}
}

func TestChangeRoutes(t *testing.T) {
	e := testutil.New(t)
	mk := func(name string, scopes ...string) string {
		s, _, err := e.Store.CreateToken(name, scopes, time.Hour, "test")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	flux, reader := mk("flux", "changes:write"), mk("reader", "status:read")
	h := Handler(Deps{Auth: &api.Auth{Store: e.Store}, Catalog: func() *catalog.Catalog { return &catalog.Catalog{} }, Changes: e.Bridge.RecordChange})
	do := func(path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec := do("/api/v1/changes", flux, `{"source":"github-actions","kind":"deploy","service":"hub-linkos-io","summary":"hub v2.1","url":"https://ci.example/runs/1","time":"`+now+`"}`)
	code(t, rec, http.StatusCreated, "")
	var c store.Change
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil || c.ID == "" || c.PostedBy != "flux" || c.Summary != "hub v2.1" {
		t.Fatalf("created: %s %v", rec.Body, err)
	}
	code(t, do("/api/v1/changes", flux, `{"source":"ci","summary":"x","colour":"red"}`), http.StatusBadRequest, "bad_input")
	code(t, do("/api/v1/changes", flux, `{"source":"ci"}`), http.StatusBadRequest, "bad_input")
	code(t, do("/api/v1/changes", reader, `{"source":"ci","summary":"x"}`), http.StatusForbidden, "auth.scope")

	sample := strings.Replace(fluxSample, "2026-11-02T08:41:07Z", now, 1)
	code(t, do("/api/v1/changes/flux?machine=edge-1", flux, sample), http.StatusCreated, "")
	code(t, do("/api/v1/changes/flux", flux, `[1]`), http.StatusBadRequest, "bad_input")
	got, err := e.Bridge.Changes(bridge.ChangeQuery{})
	if err != nil || len(got) != 2 || got[0].Source != "flux" && got[1].Source != "flux" {
		t.Fatalf("stored: %+v %v", got, err)
	}

	off := Handler(Deps{Auth: &api.Auth{Store: e.Store}, Catalog: func() *catalog.Catalog { return &catalog.Catalog{} }})
	r := httptest.NewRequest("POST", "/api/v1/changes", strings.NewReader(`{"source":"ci","summary":"x"}`))
	r.Header.Set("Authorization", "Bearer "+flux)
	rec = httptest.NewRecorder()
	off.ServeHTTP(rec, r)
	code(t, rec, http.StatusServiceUnavailable, "upstream")
}
