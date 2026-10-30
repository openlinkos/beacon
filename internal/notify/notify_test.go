package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPushover(t *testing.T) {
	var sent url.Values
	cancelled := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/1/messages.json":
			_ = r.ParseForm()
			sent = r.PostForm
			_, _ = w.Write([]byte(`{"status":1,"request":"x","receipt":"rcpt1"}`))
		case r.URL.Path == "/1/receipts/rcpt1.json":
			if r.URL.Query().Get("token") != "app" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"status":1,"acknowledged":1,"acknowledged_at":1793080000,"acknowledged_by_device":"iphone","expired":0}`))
		case strings.HasPrefix(r.URL.Path, "/1/receipts/cancel_by_tag/"):
			cancelled = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/1/receipts/cancel_by_tag/"), ".json")
			_, _ = w.Write([]byte(`{"status":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := &Pushover{API: srv.URL, Token: "app", User: "usr", Retry: time.Minute, Expire: 3 * time.Hour}
	ctx := context.Background()
	rc, err := p.Send(ctx, Emergency{Title: "git.linkos.io is down", Message: "since 10:00", URL: "https://example.com", URLTitle: "Runbook", Tag: "g123"})
	if err != nil || rc != "rcpt1" {
		t.Fatalf("Send: %q %v", rc, err)
	}
	if sent.Get("priority") != "2" || sent.Get("retry") != "60" || sent.Get("expire") != "10800" || sent.Get("tags") != "g123" || sent.Get("user") != "usr" {
		t.Errorf("form: %v", sent)
	}
	r, err := p.Receipt(ctx, "rcpt1")
	if err != nil || !r.Acknowledged || r.Device != "iphone" || r.At.Unix() != 1793080000 {
		t.Fatalf("Receipt: %+v %v", r, err)
	}
	if err := p.CancelByTag(ctx, "g123"); err != nil || cancelled != "g123" {
		t.Fatalf("CancelByTag: %v %q", err, cancelled)
	}
	if _, err := p.Receipt(ctx, "missing"); err == nil {
		t.Error("an unknown receipt should fail")
	}
}

func TestGroupBot(t *testing.T) {
	var got map[string]any
	code := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"code":` + map[bool]string{true: "19024", false: "0"}[code != 0] + `,"msg":"x"}`))
	}))
	defer srv.Close()
	g := &GroupBot{URL: srv.URL, Keyword: "LinkOS", Secret: "s", now: func() time.Time { return time.Unix(1793080000, 0) }}
	if err := g.Send(context.Background(), "watchdog stopped"); err != nil {
		t.Fatal(err)
	}
	text := got["content"].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "[LinkOS] ") || got["timestamp"] != "1793080000" || got["sign"] != Sign("1793080000", "s") {
		t.Errorf("payload %v", got)
	}
	code = 1
	if err := g.Send(context.Background(), "LinkOS x"); err == nil {
		t.Error("a non-zero code should be an error")
	}
}

func TestBarkAndPing(t *testing.T) {
	var path string
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
	}))
	defer srv.Close()
	if err := (&Bark{URL: srv.URL + "/key"}).Send(context.Background(), "t", "b", "https://x"); err != nil {
		t.Fatal(err)
	}
	if path != "/key" || body["level"] != "critical" || body["call"] != "1" || body["url"] != "https://x" {
		t.Errorf("bark: %s %v", path, body)
	}
	if err := Ping(context.Background(), nil, srv.URL+"/uuid", false); err != nil || path != "/uuid/fail" {
		t.Errorf("ping fail: %v %s", err, path)
	}
	if err := Ping(context.Background(), nil, srv.URL+"/uuid", true); err != nil || path != "/uuid" {
		t.Errorf("ping ok: %v %s", err, path)
	}
}

func TestTrunc(t *testing.T) {
	if trunc("ÄÖÜß", 3) != "ÄÖ…" || trunc("ab", 3) != "ab" {
		t.Error("trunc")
	}
}
