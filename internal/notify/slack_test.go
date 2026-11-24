package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const slackFixtureURL = "https://hooks.slack.com/services/T_FIXTURE/B_FIXTURE/webhook-secret-marker"

func TestSlackWebhookURL(t *testing.T) {
	for _, raw := range []string{slackFixtureURL, strings.Replace(slackFixtureURL, "slack.com", "slack-gov.com", 1)} {
		if !ValidSlackWebhookURL(raw) {
			t.Error("a standard Slack webhook was rejected")
		}
	}
	for name, raw := range map[string]string{
		"cleartext": strings.Replace(slackFixtureURL, "https:", "http:", 1),
		"userinfo":  strings.Replace(slackFixtureURL, "hooks.", "alice@hooks.", 1),
		"lookalike": strings.Replace(slackFixtureURL, "slack.com", "slack.com.example.com", 1),
		"port":      strings.Replace(slackFixtureURL, "slack.com", "slack.com:8443", 1),
		"query":     slackFixtureURL + "?unexpected=1",
		"fragment":  slackFixtureURL + "#unexpected",
		"encoded":   strings.Replace(slackFixtureURL, "/T_", "/T%2f_", 1),
		"extra":     slackFixtureURL + "/extra",
	} {
		t.Run(name, func(t *testing.T) {
			if ValidSlackWebhookURL(raw) {
				t.Error("invalid Slack endpoint was accepted")
			}
		})
	}
}

func TestSlackMessageSeparatesAlertTextFromLinkMarkup(t *testing.T) {
	const title = "Firing · Critical\nrelay <@TECH> <!channel> & API"
	const incident = "https://beacon.example.com/console/sign-in?next=%2Fconsole%2Falerts%2Fdemo"
	for _, tc := range []struct {
		url   string
		count int
	}{{incident, 2}, {incident + "|<!channel", 1}, {"", 1}} {
		var msg struct {
			Text   string         `json:"text"`
			Blocks []slackSection `json:"blocks"`
			MD     bool           `json:"mrkdwn"`
			Unfurl bool           `json:"unfurl_links"`
		}
		if err := json.Unmarshal(SlackMessage(title, tc.url, "Open alert"), &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Text != title || msg.MD || msg.Unfurl || len(msg.Blocks) != tc.count || msg.Blocks[0].Text != (slackText{Type: "plain_text", Text: title}) {
			t.Fatalf("alert title or fallback is not plain text: %+v", msg)
		}
		if tc.count == 2 && msg.Blocks[1].Text != (slackText{Type: "mrkdwn", Text: "<" + incident + "|Open alert>", Verbatim: true}) {
			t.Fatalf("wrong incident link: %+v", msg.Blocks[1])
		}
	}
}

type slackRoundTripper func(*http.Request) (*http.Response, error)

func (f slackRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSlackPostsOnlyToItsEndpointAndChecksAcceptance(t *testing.T) {
	const marker = "webhook-secret-marker"
	body := SlackMessage("Resolved · Critical\nrelay", "", "Open alert")
	for _, tc := range []struct {
		name, reply, want string
		code              int
	}{
		{"accepted", "ok\n", "", http.StatusOK},
		{"unrelated response", "html " + marker, "did not confirm", http.StatusOK},
		{"truncated response", "ok" + strings.Repeat(" ", 62) + marker, "did not confirm", http.StatusOK},
		{"refused", marker, "HTTP 503", http.StatusServiceUnavailable},
		{"rate limited", marker, "HTTP 429", http.StatusTooManyRequests},
		{"redirect", "", "HTTP 302", http.StatusFound},
		{"transport failed", "", "request failed", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			hc := &http.Client{CheckRedirect: webhookHTTP.CheckRedirect, Transport: slackRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != slackFixtureURL || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Beacon-Signature") != "" {
					t.Fatal("unexpected Slack request")
				}
				posted, err := io.ReadAll(r.Body)
				_ = r.Body.Close()
				if err != nil || !bytes.Equal(posted, body) {
					t.Fatal("Slack did not receive the queued body")
				}
				if tc.code == 0 {
					return nil, fmt.Errorf("the transport included %s in its error", marker)
				}
				return &http.Response{StatusCode: tc.code, Header: http.Header{"Location": {"https://example.com/"}, "Retry-After": {"90"}}, Body: io.NopCloser(strings.NewReader(tc.reply))}, nil
			})}
			err := (&Slack{URL: slackFixtureURL}).send(context.Background(), body, hc)
			if calls != 1 {
				t.Fatalf("sent %d requests, want one", calls)
			}
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), marker)) {
				t.Fatalf("wrong result for %s: %v", tc.name, err)
			}
			if tc.code == http.StatusTooManyRequests {
				limited, ok := errors.AsType[SlackRateLimited](err)
				if !ok || limited.After != 90*time.Second {
					t.Fatalf("Slack requested a 90-second pause, got %+v", err)
				}
			}
		})
	}
}
