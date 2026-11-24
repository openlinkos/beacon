package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/version"
)

// Slack delivers incident messages to a Slack app's incoming webhook.
type Slack struct {
	URL string

	mu   sync.Mutex
	next time.Time
}

// SlackRateLimited is the retry interval from a Slack rate-limit response.
type SlackRateLimited struct{ After time.Duration }

func (SlackRateLimited) Error() string { return "Slack webhook: HTTP 429" }

var slackWebhookPath = regexp.MustCompile(`^/services/[A-Za-z0-9_-]+/[A-Za-z0-9_-]+/[A-Za-z0-9_-]+$`)

// ValidSlackWebhookURL accepts the official Slack and GovSlack incoming webhook endpoints.
func ValidSlackWebhookURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || !validWebhookURL(raw, u) || u.Scheme != "https" || u.Port() != "" || u.ForceQuery || u.RawQuery != "" || u.RawPath != "" {
		return false
	}
	switch strings.ToLower(u.Host) {
	case "hooks.slack.com", "hooks.slack-gov.com":
	default:
		return false
	}
	return slackWebhookPath.MatchString(u.Path)
}

type slackText struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Verbatim bool   `json:"verbatim,omitempty"`
}

type slackSection struct {
	Type string    `json:"type"`
	Text slackText `json:"text"`
}

// SlackMessage renders a plain-text incident title with an optional, generated link. Only the
// link section uses Slack markup, and it contains no text from the alert itself.
func SlackMessage(heading, incidentURL, openLabel string) []byte {
	msg := struct {
		Text        string         `json:"text"`
		Markdown    bool           `json:"mrkdwn"`
		UnfurlLinks bool           `json:"unfurl_links"`
		UnfurlMedia bool           `json:"unfurl_media"`
		Blocks      []slackSection `json:"blocks"`
	}{
		Text:   heading,
		Blocks: []slackSection{{Type: "section", Text: slackText{Type: "plain_text", Text: heading}}},
	}
	if ValidWebhookURL(incidentURL) && !strings.ContainsAny(incidentURL, "<>|") {
		msg.Blocks = append(msg.Blocks, slackSection{Type: "section", Text: slackText{
			Type: "mrkdwn", Text: "<" + incidentURL + "|" + openLabel + ">", Verbatim: true,
		}})
	}
	body, _ := json.Marshal(msg)
	return body
}

// Send submits the exact queued message to Slack. A response without Slack's acceptance marker
// can be a proxy page and must not be counted as an accepted alert.
func (s *Slack) Send(ctx context.Context, body []byte) error {
	s.mu.Lock()
	now := time.Now()
	wait := max(s.next.Sub(now), 0)
	s.next = now.Add(wait + time.Second)
	s.mu.Unlock()
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return s.send(ctx, body, webhookHTTP)
}

func (s *Slack) send(ctx context.Context, body []byte, hc *http.Client) error {
	if !ValidSlackWebhookURL(s.URL) {
		return errors.New("Slack webhook: invalid endpoint")
	}
	if len(body) > webhookRequestLimit {
		return errors.New("Slack webhook: request body exceeds 1 MiB")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("Slack webhook: invalid request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := hc.Do(req)
	if err != nil {
		return errors.New("Slack webhook: request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests {
		seconds, err := strconv.ParseUint(resp.Header.Get("Retry-After"), 10, 32)
		if err != nil {
			seconds = 0
		}
		return SlackRateLimited{After: time.Duration(seconds) * time.Second}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Slack webhook: HTTP %d", resp.StatusCode)
	}
	const acceptLimit = 64
	data, err := io.ReadAll(io.LimitReader(resp.Body, acceptLimit+1))
	if err != nil || len(data) > acceptLimit || string(bytes.TrimSpace(data)) != "ok" {
		return errors.New("Slack webhook: response did not confirm acceptance")
	}
	return nil
}
