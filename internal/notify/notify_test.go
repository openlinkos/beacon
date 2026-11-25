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
	g := &GroupBot{URL: srv.URL, Keyword: "Beacon", Secret: "s", now: func() time.Time { return time.Unix(1793080000, 0) }}
	if err := g.Send(context.Background(), "watchdog stopped"); err != nil {
		t.Fatal(err)
	}
	text := got["content"].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "[Beacon] ") || got["timestamp"] != "1793080000" || got["sign"] != Sign("1793080000", "s") {
		t.Errorf("payload %v", got)
	}
	code = 1
	if err := g.Send(context.Background(), "Beacon x"); err == nil {
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
		_, _ = io.WriteString(w, `{"code":200,"message":"success"}`)
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

func TestProviderAcceptanceRequiresTheProviderResponse(t *testing.T) {
	for _, tc := range []struct {
		name, provider, reply string
		ok                    bool
	}{
		{"current Feishu reply", "feishu", `{"code":0}`, true},
		{"legacy Feishu reply", "feishu", `{"StatusCode":0}`, true},
		{"legacy Feishu rejection", "feishu", `{"StatusCode":19021}`, false},
		{"contradictory Feishu reply", "feishu", `{"code":0,"StatusCode":19021}`, false},
		{"empty Feishu object", "feishu", `{}`, false},
		{"Feishu proxy page", "feishu", "<html>OK</html>", false},
		{"Bark push accepted", "bark", `{"code":200}`, true},
		{"Bark rejected push", "bark", `{"code":400}`, false},
		{"Bark proxy page", "bark", "<html>OK</html>", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.reply)
			}))
			defer srv.Close()
			ctx := context.Background()
			var err error
			if tc.provider == "feishu" {
				err = (&GroupBot{URL: srv.URL}).Send(ctx, "fixture alert")
			} else {
				err = (&Bark{URL: srv.URL}).Send(ctx, "fixture alert", "test", "")
			}
			if tc.ok != (err == nil) {
				t.Fatalf("accepted = %v, error %v", err == nil, err)
			}
		})
	}
}

func TestProviderErrorsKeepCredentialsAndRepliesPrivate(t *testing.T) {
	const credential = "fixture-provider-credential"
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, credential, http.StatusForbidden)
	}))
	defer endpoint.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, endpoint.URL+"/wrong", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	const generic = "request failed"
	for _, tc := range []struct {
		name, url, want string
	}{
		{"body", endpoint.URL, "HTTP 403"},
		{"redirect", redirect.URL, "HTTP 307"},
		{"transport", "http://127.0.0.1:0", generic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (&GroupBot{URL: tc.url + "/" + credential}).Send(context.Background(), "fixture alert")
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), credential) {
				t.Fatalf("wanted a safe provider failure (%s), got %v", tc.want, err)
			}
		})
	}
}
