package alertmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDecode(t *testing.T) {
	body := `{"version":"4","groupKey":"{}:{alertname=\"ServiceDown\"}","status":"firing","receiver":"bridge",
	"groupLabels":{"alertname":"ServiceDown","service":"git-linkos-io"},"commonLabels":{"severity":"critical"},
	"alerts":[{"status":"firing","labels":{"alertname":"ServiceDown"},"startsAt":"2026-10-30T10:00:00Z","fingerprint":"abc"}]}`
	w, err := Decode(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if w.GroupLabels["service"] != "git-linkos-io" || len(w.Alerts) != 1 || w.Alerts[0].StartsAt.IsZero() {
		t.Fatalf("decoded %+v", w)
	}
	for _, bad := range []string{`{"version":"3","groupKey":"x","status":"firing"}`, `{"version":"4","status":"firing"}`, `{"version":"4","groupKey":"x","status":"odd"}`, `nope`} {
		if _, err := Decode(strings.NewReader(bad)); err == nil {
			t.Errorf("%s should fail", bad)
		}
	}
}

func TestSilences(t *testing.T) {
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/silences":
			_ = json.NewDecoder(r.Body).Decode(&created)
			_, _ = w.Write([]byte(`{"silenceID":"s-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/silences":
			_, _ = w.Write([]byte(`[{"id":"s-1","status":{"state":"active"}},{"id":"s-0","status":{"state":"expired"}}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v2/silence/s-1":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/-/ready":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, nil)
	ctx := context.Background()
	now := time.Now()
	id, err := c.CreateSilence(ctx, map[string]string{"alertname": "ServiceDown"}, now, now.Add(time.Hour), "Alice", "deploying")
	if err != nil || id != "s-1" {
		t.Fatalf("CreateSilence: %q %v", id, err)
	}
	if created["createdBy"] != "Alice" || created["comment"] != "deploying" {
		t.Errorf("request: %v", created)
	}
	ms := created["matchers"].([]any)
	if m := ms[0].(map[string]any); m["name"] != "alertname" || m["isEqual"] != true || m["isRegex"] != false {
		t.Errorf("matcher: %v", m)
	}
	if _, err := c.CreateSilence(ctx, nil, now, now, "", ""); err == nil {
		t.Error("a silence without matchers should be refused")
	}
	ss, err := c.Silences(ctx)
	if err != nil || len(ss) != 1 || ss[0].ID != "s-1" {
		t.Fatalf("Silences: %+v %v", ss, err)
	}
	if err := c.ExpireSilence(ctx, "s-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.ExpireSilence(ctx, "missing"); err == nil {
		t.Error("expiring an unknown silence should fail")
	}
	if err := c.Ready(ctx); err == nil {
		t.Error("Ready should report 503")
	}
}
