package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGatewayCountsFailuresApart checks that the gateway's lockout and the API's are separate: what
// fails on one never locks the other.
func TestGatewayCountsFailuresApart(t *testing.T) {
	e := newEnv(t, false)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil)
	for range 20 {
		e.auth.Fail(r) // the API's failures, from the same address
	}
	if !e.auth.Blocked(r) {
		t.Fatal("the API's lockout did not take")
	}
	code(t, e.do("POST", "/api/v1/write", e.probe, ""), http.StatusNoContent, "")

	e = newEnv(t, false)
	for i := range 20 {
		e.do("POST", "/api/v1/write", fmt.Sprintf("lbt_nope%d", i), "x")
	}
	code(t, e.do("POST", "/api/v1/write", e.probe2, "x"), http.StatusTooManyRequests, "auth.too_many_failures")
	if e.auth.Blocked(r) {
		t.Error("the gateway's failures locked the API out")
	}
}

// TestGatewayLockoutKeepsWorkingTokens checks that a locked address goes on pushing with a token that
// worked from it before, while a token new to it waits like an unknown one.
func TestGatewayLockoutKeepsWorkingTokens(t *testing.T) {
	e := newEnv(t, false)
	code(t, e.do("POST", "/api/v1/write", e.probe, ""), http.StatusNoContent, "")
	for i := range 20 {
		code(t, e.do("POST", "/api/v1/write", fmt.Sprintf("lbt_nope%d", i), "x"), http.StatusUnauthorized, "auth.invalid")
	}
	code(t, e.do("POST", "/api/v1/write", e.probe, ""), http.StatusNoContent, "")
	code(t, e.do("POST", "/api/v1/write", e.probe2, "x"), http.StatusTooManyRequests, "auth.too_many_failures")
	code(t, e.do("POST", "/api/v1/write", "lbt_nope", "x"), http.StatusTooManyRequests, "auth.too_many_failures")
}

// TestRulesTokenUnderLockout checks that the rule engine's token is held to the lockout like any other:
// wrong guesses at it count, and a locked address cannot try it.
func TestRulesTokenUnderLockout(t *testing.T) {
	const rules = "rules-0123456789abcdef0123456789"
	body := `[{"labels":{"alertname":"ServiceDown","service":"git"},"startsAt":"2026-11-03T00:00:00Z"}]`
	e := newEnv(t, true)
	for i := range 20 {
		code(t, e.do("POST", "/api/v2/alerts", fmt.Sprintf("rules-guess-%02d-0123456789abcdef", i), body), http.StatusUnauthorized, "auth.invalid")
	}
	code(t, e.do("POST", "/api/v2/alerts", rules, body), http.StatusTooManyRequests, "auth.too_many_failures")
	code(t, e.do("POST", "/api/v2/alerts", e.flux, body), http.StatusTooManyRequests, "auth.too_many_failures")
	if len(e.posted) != 0 {
		t.Fatalf("alerts taken from a locked address: %+v", e.posted)
	}

	// the rule engine, already sending from its address, keeps sending when another client there fails
	e = newEnv(t, true)
	code(t, e.do("POST", "/api/v2/alerts", rules, body), http.StatusOK, "")
	for i := range 20 {
		e.do("POST", "/api/v2/alerts", fmt.Sprintf("lbt_nope%d", i), body)
	}
	code(t, e.do("POST", "/api/v2/alerts", rules, body), http.StatusOK, "")
	code(t, e.do("POST", "/api/v2/alerts", "", body), http.StatusUnauthorized, "auth.required")
}
