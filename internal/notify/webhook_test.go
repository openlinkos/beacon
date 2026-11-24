package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/version"
)

func TestValidWebhookURL(t *testing.T) {
	for _, endpoint := range []string{
		"http://notify:8080/receiver?token=fixture",
		"https://notify.example.com/path%23segment",
		"http://127.0.0.1",
		"http://[::1]:8080/hook",
	} {
		if !ValidWebhookURL(endpoint) {
			t.Errorf("valid endpoint rejected: %q", endpoint)
		}
	}
	for _, endpoint := range []string{
		"",
		"/receiver",
		"//notify.example.com/receiver",
		"ftp://notify.example.com/receiver",
		"http:notify.example.com",
		"http://:8080/receiver",
		"http://notify.example.com:",
		"http://notify.example.com:0",
		"http://notify.example.com:65536",
		"http://notify.example.com:bad",
		"http://::1",
		"http://[notify.example.com]",
		"https://key@notify.example.com/receiver",
		"https://user:password@notify.example.com/receiver",
		"https://notify.example.com/receiver#fragment",
		"https://notify.example.com/receiver#",
		"https://notify.example.com/receiver with space",
		"http://notify.example.com/%zz",
	} {
		if ValidWebhookURL(endpoint) {
			t.Errorf("invalid endpoint accepted: %q", endpoint)
		}
	}
}

func TestWebhookSendsSignedJSON(t *testing.T) {
	body := []byte("{\"title\":\"Fixture alert\"}\n")
	const key = "fixture-secret"
	const signature = "sha256=cc561662fbe08835ae247605238dd757474f0fda737b18c5c4e3ecbc81fa44c3"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/receiver" || r.URL.Query().Get("token") != "fixture" {
			t.Error("unexpected request target or method")
		}
		for k, want := range map[string]string{
			"Content-Type":       "application/json",
			"User-Agent":         version.UserAgent(),
			"X-Beacon-Signature": signature,
		} {
			if r.Header.Get(k) != want {
				t.Errorf("incorrect %s header", k)
			}
		}
		got, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(got, body) {
			t.Error("request body differs from the signed bytes")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	hook := Webhook{URL: server.URL + "/receiver?token=fixture", Secret: key}
	if err := hook.Send(t.Context(), body); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookDoesNotFollowRedirects(t *testing.T) {
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/marker-in-location", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	hook := Webhook{URL: server.URL, Secret: "fixture-secret"}
	err := hook.Send(t.Context(), []byte("{}"))
	if err == nil || err.Error() != "notification webhook: HTTP 307" || requests.Load() != 0 {
		t.Fatalf("redirect handling: err=%v, destination requests=%d", err, requests.Load())
	}
}

func TestWebhookDoesNotReadResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	hook := Webhook{URL: server.URL, Secret: "fixture-secret"}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := hook.Send(ctx, []byte("{}")); err != nil || ctx.Err() != nil {
		t.Fatalf("delivery depends on reading the response body: %v, context %v", err, ctx.Err())
	}
}

func TestWebhookRedactsDeliveryErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "private response marker", http.StatusForbidden)
	}))
	defer server.Close()
	offline := httptest.NewServer(http.NotFoundHandler())
	offline.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	body := []byte("{\"title\": \"private request marker\"}")
	tests := []struct {
		name, target, want string
		ctx                context.Context
		cause              error
	}{
		{"invalid URL", "http://fixture%zz/marker", "invalid endpoint", t.Context(), nil},
		{"remote status", server.URL + "/?token=private-url-marker", "HTTP 403", t.Context(), nil},
		{"transport failure", offline.URL + "/?token=private-url-marker", "request failed", t.Context(), nil},
		{"cancelled", server.URL + "/?token=private-url-marker", "context canceled", ctx, context.Canceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hook := Webhook{URL: tc.target, Secret: "private signing marker"}
			err := hook.Send(tc.ctx, body)
			if want := fmt.Sprintf("notification webhook: %s", tc.want); err == nil || err.Error() != want {
				t.Fatalf("expected %q error, got %v", want, err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Errorf("underlying context error was lost: %v", err)
			}
		})
	}
}

func TestWebhookRejectsOversizedRequestAndMissingSecret(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	hook := Webhook{URL: server.URL, Secret: "fixture-secret"}
	if err := hook.Send(t.Context(), []byte(strings.Repeat("x", webhookRequestLimit+1))); err == nil ||
		!strings.Contains(err.Error(), "exceeds 1 MiB") {
		t.Errorf("oversized body: %v", err)
	}
	hook.Secret = ""
	if err := hook.Send(t.Context(), []byte("{}")); err == nil || !strings.Contains(err.Error(), "signing secret is missing") {
		t.Errorf("missing signing secret: %v", err)
	}
	if requests.Load() != 0 {
		t.Errorf("invalid requests were sent: %d", requests.Load())
	}
}
