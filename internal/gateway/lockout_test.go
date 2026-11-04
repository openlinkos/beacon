package gateway

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/testutil"
)

// TestLockoutNamesTheWait checks that the machines' gateway, like the REST API, tells a locked-out
// address how long to wait: the code stays auth.too_many_failures, the message names the wait and the
// answer carries Retry-After, so a probe agent knows when to send again instead of backing off blind.
func TestLockoutNamesTheWait(t *testing.T) {
	e := testutil.New(t)
	secret, _, err := e.Store.CreateToken("probe", []string{"changes:write"}, time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(Deps{Auth: &api.Auth{Store: e.Store}})
	do := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/changes", strings.NewReader(`{"source":"ci","summary":"x"}`))
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	for range 20 {
		do("lbt_wrong")
	}
	rec := do(secret)
	body := rec.Body.String()
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(body, "auth.too_many_failures") || !strings.Contains(body, "try again in") {
		t.Fatalf("lockout: %d %s", rec.Code, body)
	}
	wait, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || wait < 1 || wait > 600 {
		t.Fatalf("Retry-After: %q (%v)", rec.Header().Get("Retry-After"), err)
	}
}
