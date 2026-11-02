package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/probenow"
	"github.com/openlinkos/beacon/internal/targets"
)

func TestRequestsLongPoll(t *testing.T) {
	e := newEnv(t, false)
	// nothing pending: an empty list at once
	rec := e.do("GET", "/api/v1/agent/requests?wait=0", e.probe, "")
	code(t, rec, http.StatusOK, "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("nothing pending: %s", rec.Body)
	}
	// a request for a target that cdn-pre-us1 alone has (cdn-pre-us1-node.yaml): cdn-pre-de1 sees nothing
	v, err := e.board.Create("claude-code", []probenow.Want{{Target: "203.0.113.1:443"}})
	if err != nil || strings.Join(v.Expected, ",") != "cdn-pre-us1" {
		t.Fatalf("create: %+v %v", v, err)
	}
	rec = e.do("GET", "/api/v1/agent/requests?wait=0", e.probe2, "")
	code(t, rec, http.StatusOK, "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("another location: %s", rec.Body)
	}
	rec = e.do("GET", "/api/v1/agent/requests?wait=0", e.probe, "")
	code(t, rec, http.StatusOK, "")
	var reqs []targets.Request
	if err := json.Unmarshal(rec.Body.Bytes(), &reqs); err != nil || len(reqs) != 1 || reqs[0].ID != v.ID || len(reqs[0].Groups) != 1 ||
		reqs[0].Groups[0].Targets[0] != "203.0.113.1:443" || reqs[0].Groups[0].Labels["module"] != "tcp_443" || reqs[0].Groups[0].Labels["service"] != "node" {
		t.Fatalf("pending for cdn-pre-us1: %s %v", rec.Body, err)
	}
	// a hanging poll is woken at once by a request for a target it has (all-web.yaml)
	woke := make(chan *httptest.ResponseRecorder, 1)
	go func() { woke <- e.do("GET", "/api/v1/agent/requests?wait=5s", e.probe2, "") }()
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	v2, err := e.board.Create("claude-code", []probenow.Want{{Target: "https://linkos.org/"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-woke:
		code(t, rec, http.StatusOK, "")
		if time.Since(start) > time.Second || !strings.Contains(rec.Body.String(), `"https://linkos.org/"`) {
			t.Fatalf("woken after %v with %s", time.Since(start), rec.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the poll was not woken")
	}
	// answered, the request is no longer pending for cdn-pre-de1, and the wait passes with nothing to hand out
	code(t, e.do("POST", "/api/v1/agent/requests/"+v2.ID, e.probe2, `{"results":[{"target":"https://linkos.org/","module":"http_head","success":true}]}`), http.StatusNoContent, "")
	start = time.Now()
	rec = e.do("GET", "/api/v1/agent/requests?wait=40ms", e.probe2, "")
	code(t, rec, http.StatusOK, "")
	if strings.TrimSpace(rec.Body.String()) != "[]" || time.Since(start) < 40*time.Millisecond {
		t.Fatalf("after the wait: %s in %v", rec.Body, time.Since(start))
	}
	code(t, e.do("GET", "/api/v1/agent/requests?wait=soon", e.probe, ""), http.StatusBadRequest, "bad_input")
	code(t, e.do("GET", "/api/v1/agent/requests?wait=0", e.expired, ""), http.StatusForbidden, "location.inactive")
	code(t, e.do("GET", "/api/v1/agent/requests?wait=0", e.host, ""), http.StatusForbidden, "auth.scope")
	if !strings.Contains(scrape(e), `linkos_gateway_requests_total{route="requests",code="200"}`) {
		t.Errorf("polls are counted:\n%s", scrape(e))
	}
}

func TestAnswers(t *testing.T) {
	e := newEnv(t, false)
	v, err := e.board.Create("claude-code", []probenow.Want{{Target: "https://linkos.org/"}})
	if err != nil || strings.Join(v.Expected, ",") != "cdn-pre-de1,cdn-pre-us1" {
		t.Fatalf("create: %+v %v", v, err)
	}
	body := `{"results":[{"target":"https://linkos.org/","module":"http_head","success":false,"reason":"tls","duration_seconds":1.5,"attempts":2,"cert_expiry":1800000000}]}`
	code(t, e.do("POST", "/api/v1/agent/requests/"+v.ID, e.probe, body), http.StatusNoContent, "")
	code(t, e.do("POST", "/api/v1/agent/requests/"+v.ID, e.probe2, `{"results":[{"target":"https://linkos.org/","module":"http_head","success":true,"http_status":200}]}`), http.StatusNoContent, "")
	got, ok := e.board.Await(t.Context(), v.ID)
	if !ok || !got.Done || len(got.Results) != 2 || got.Results[0].Location != "cdn-pre-us1" || got.Results[0].Reason != "tls" ||
		got.Results[0].CertExpiry != 1800000000 || got.Results[0].Attempts != 2 || got.Results[1].Location != "cdn-pre-de1" || !got.Results[1].Success {
		t.Fatalf("answers: %+v", got)
	}
	code(t, e.do("POST", "/api/v1/agent/requests/0123456789abcdef", e.probe, body), http.StatusNotFound, "request.not_found")
	code(t, e.do("POST", "/api/v1/agent/requests/not-an-id", e.probe, body), http.StatusNotFound, "request.not_found")
	code(t, e.do("POST", "/api/v1/agent/requests/"+v.ID, e.probe, "{nope"), http.StatusBadRequest, "bad_input")
	code(t, e.do("POST", "/api/v1/agent/requests/"+v.ID, e.expired, body), http.StatusForbidden, "location.inactive")
	code(t, e.do("POST", "/api/v1/agent/requests/"+v.ID, e.host, body), http.StatusForbidden, "auth.scope")
	for _, want := range []string{"linkos_probe_now_requests_total 1", `linkos_probe_now_answers_total{location="cdn-pre-us1"} 1`,
		`linkos_gateway_requests_total{route="answers",code="204"} 2`} {
		if !strings.Contains(scrape(e), want) {
			t.Errorf("metrics lack %s:\n%s", want, scrape(e))
		}
	}
}

// An older gateway, or one without probes on demand, answers 404: the probe then asks again in an hour.
func TestRequestsWithoutABoard(t *testing.T) {
	e := newEnv(t, false)
	cat, _ := catalog.Load(t.TempDir())
	h := Handler(Deps{Auth: &api.Auth{Store: e.st}, Catalog: func() *catalog.Catalog { return cat }, Targets: func() *targets.Set { return &targets.Set{} }})
	for _, c := range []struct{ method, path string }{{"GET", "/api/v1/agent/requests?wait=0"}, {"POST", "/api/v1/agent/requests/0123456789abcdef"}} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer "+e.probe)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		code(t, rec, http.StatusNotFound, "not_found")
	}
}
