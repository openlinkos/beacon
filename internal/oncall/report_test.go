package oncall

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const fixtureReportToken = "lbt_fixture_only"

func fixtureReporter(t *testing.T, base string) *runReporter {
	t.Helper()
	r, err := newRunReporter(Options{ReportURL: base, ReportToken: fixtureReportToken,
		Concurrency: 1, Queue: 1, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestExecutionReportBaseAndToken(t *testing.T) {
	for _, input := range []string{"https://beacon.example.com", "https://beacon.example.com/prefix/", "http://127.0.0.1:8081", "http://[::1]:8081"} {
		if _, err := reportBase(input); err != nil {
			t.Errorf("valid base rejected: %s", input)
		}
	}
	const marker = "fixture-private"
	for _, input := range []string{
		"", "/base", "http://example.com/" + marker, "http://10.0.0.1", "https:" + marker, "file:///tmp/" + marker,
		"https://host:" + marker, "https://host:0", "https://host:65536", "https://host:",
		"https://" + marker + "@host", "https://host?token=" + marker, "https://host?", "https://host#" + marker,
		"https://host#", "https://host/one two", "https://host/%2fsecond", "https://[example.com]", "https://::1",
	} {
		if _, err := reportBase(input); err == nil || strings.Contains(err.Error(), marker) {
			t.Fatalf("invalid base accepted or leaked: %v", err)
		}
	}
	for _, invalid := range []string{"", "token\r\nmarker", "é", "Bearer token", "=begin", "orig=in", strings.Repeat("a", 4097)} {
		if validReportToken(invalid) {
			t.Fatal("invalid report token accepted")
		}
	}
	if !validReportToken("abc-de_fg.12~+/==") {
		t.Fatal("a valid bearer token was rejected")
	}
	for _, invalid := range []string{"", ".", "..", "nested/segment", "one%2ftwo", "id?x=1", "id#fragment"} {
		if validReportID(invalid) {
			t.Fatal("unsafe path segment accepted")
		}
	}
	for _, o := range []Options{
		{ReportURL: "https://beacon.example.com"},
		{ReportToken: fixtureReportToken},
		{ReportURL: "https://beacon.example.com", ReportToken: fixtureReportToken, Secret: []byte(fixtureReportToken)},
	} {
		if _, err := newRunReporter(o); err == nil || strings.Contains(err.Error(), fixtureReportToken) {
			t.Fatalf("invalid reporting configuration: %v", err)
		}
	}
}

func TestReportRetriesTransientResponse(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil || string(got) != `{"status":"succeeded","exit_code":0}` {
			t.Error("unexpected report content")
		}
		if r.Method != http.MethodPut || r.URL.Path != "/prefix/api/v1/alerts/known-alert/agent-runs/known-delivery" ||
			r.Header.Get("Authorization") != "Bearer "+fixtureReportToken || r.Header.Get("Content-Type") != "application/json" {
			t.Error("unexpected incident URL, method or headers")
		}
		if attempts.Add(1) == 1 {
			http.Error(w, "private provider body", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	r := fixtureReporter(t, server.URL+"/prefix/")
	err := r.send(t.Context(), reportTask{alert: "known-alert", delivery: "known-delivery", report: executionResult(false, 0)})
	if err != nil || attempts.Load() != 2 {
		t.Fatalf("transient response: attempts=%d, error=%v", attempts.Load(), err)
	}
}

func TestReportRefusesRedirectAndRedactsErrors(t *testing.T) {
	var redirects atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirects.Add(1) }))
	defer destination.Close()
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusTemporaryRedirect, "report HTTP 307"},
		{http.StatusForbidden, "report HTTP 403"},
		{http.StatusTooManyRequests, "report HTTP 429"},
	} {
		var attempts atomic.Int32
		receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.Header().Set("Retry-After", "60")
			w.Header().Set("Location", destination.URL+"/private-redirect-marker")
			http.Error(w, "private provider body", tc.status)
		}))
		reporter := fixtureReporter(t, receiver.URL)
		err := reporter.send(t.Context(), reportTask{alert: "alert", delivery: "delivery", report: runReport{Status: runRunning}})
		receiver.Close()
		if err == nil || err.Error() != tc.want || attempts.Load() != 1 {
			t.Errorf("%d: requests=%d, err=%v", tc.status, attempts.Load(), err)
		}
	}
	if redirects.Load() != 0 {
		t.Error("a redirect received an execution report")
	}
}

func TestExecutionReportOptionalExitCode(t *testing.T) {
	for _, tc := range []struct {
		timedOut bool
		code     int
		want     string
	}{
		{false, 0, `{"status":"succeeded","exit_code":0}`},
		{false, 23, `{"status":"failed","exit_code":23}`},
		{false, -1, `{"status":"failed"}`},
		{true, 0, `{"status":"timed_out"}`},
		{true, -1, `{"status":"timed_out"}`},
	} {
		got, err := json.Marshal(executionResult(tc.timedOut, tc.code))
		if err != nil || string(got) != tc.want {
			t.Errorf("report = %s, want %s", got, tc.want)
		}
	}
}
