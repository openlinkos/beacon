// Package notify sends through the channels that are not the Feishu app: Pushover (iOS emergency
// alerts that repeat until acknowledged), Bark (an iOS push that rings), the Feishu group's custom
// bot (plain messages that need no app and spend no API quota), and healthchecks.io pings.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/version"
)

func client(hc *http.Client) *http.Client {
	if hc != nil {
		return hc
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func send(ctx context.Context, hc *http.Client, req *http.Request) ([]byte, error) {
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := client(hc).Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return b, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return b, nil
}

// ---- Pushover

// Pushover sends emergency-priority messages to one user.
type Pushover struct {
	API     string // https://api.pushover.net
	Token   string // application token
	User    string // user key
	Retry   time.Duration
	Expire  time.Duration
	HTTP    *http.Client
	Enabled bool
}

// Emergency is an emergency message. Tag lets every message about one alert group be cancelled at once.
type Emergency struct {
	Title, Message, URL, URLTitle, Tag string
}

// Send sends an emergency (priority 2) message and returns its receipt.
func (p *Pushover) Send(ctx context.Context, m Emergency) (string, error) {
	form := url.Values{
		"token": {p.Token}, "user": {p.User}, "title": {trunc(m.Title, 250)}, "message": {trunc(m.Message, 1024)},
		"priority": {"2"}, "retry": {strconv.Itoa(int(p.Retry.Seconds()))}, "expire": {strconv.Itoa(int(p.Expire.Seconds()))},
	}
	if m.URL != "" {
		form.Set("url", m.URL)
		form.Set("url_title", trunc(m.URLTitle, 100))
	}
	if m.Tag != "" {
		form.Set("tags", m.Tag)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.API+"/1/messages.json", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	b, err := send(ctx, p.HTTP, req)
	if err != nil {
		return "", fmt.Errorf("pushover: %w", err)
	}
	var out struct {
		Status  int    `json:"status"`
		Receipt string `json:"receipt"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Status != 1 || out.Receipt == "" {
		return "", fmt.Errorf("pushover: unexpected answer %s", strings.TrimSpace(string(b)))
	}
	return out.Receipt, nil
}

// Receipt is the state of an emergency message.
type Receipt struct {
	Acknowledged bool
	At           time.Time
	Device       string
	Expired      bool
}

// Receipt looks up an emergency message's receipt.
func (p *Pushover) Receipt(ctx context.Context, receipt string) (Receipt, error) {
	u := p.API + "/1/receipts/" + url.PathEscape(receipt) + ".json?token=" + url.QueryEscape(p.Token)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	b, err := send(ctx, p.HTTP, req)
	if err != nil {
		return Receipt{}, fmt.Errorf("pushover receipt: %w", err)
	}
	var out struct {
		Status               int    `json:"status"`
		Acknowledged         int    `json:"acknowledged"`
		AcknowledgedAt       int64  `json:"acknowledged_at"`
		AcknowledgedByDevice string `json:"acknowledged_by_device"`
		Expired              int    `json:"expired"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Status != 1 {
		return Receipt{}, fmt.Errorf("pushover receipt: unexpected answer %s", strings.TrimSpace(string(b)))
	}
	r := Receipt{Acknowledged: out.Acknowledged == 1, Device: out.AcknowledgedByDevice, Expired: out.Expired == 1}
	if out.AcknowledgedAt > 0 {
		r.At = time.Unix(out.AcknowledgedAt, 0)
	}
	return r, nil
}

// CancelByTag stops the retries of every emergency message carrying tag.
func (p *Pushover) CancelByTag(ctx context.Context, tag string) error {
	form := url.Values{"token": {p.Token}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.API+"/1/receipts/cancel_by_tag/"+url.PathEscape(tag)+".json", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := send(ctx, p.HTTP, req); err != nil {
		return fmt.Errorf("pushover cancel: %w", err)
	}
	return nil
}

// ---- Bark

// Bark sends an iOS push through a Bark server; critical pushes ring through silent mode.
type Bark struct {
	URL  string // https://api.day.app/<key>
	HTTP *http.Client
}

// Send sends a critical, ringing push.
func (b *Bark) Send(ctx context.Context, title, body, link string) error {
	in := map[string]string{"title": trunc(title, 200), "body": trunc(body, 1000), "level": "critical", "call": "1", "group": "linkos-beacon", "volume": "8"}
	if link != "" {
		in["url"] = link
	}
	buf, _ := json.Marshal(in)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, b.URL, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if _, err := send(ctx, b.HTTP, req); err != nil {
		return fmt.Errorf("bark: %w", err)
	}
	return nil
}

// ---- Feishu group custom bot

// GroupBot posts plain text to a Feishu group through its custom bot. The bot's security setting is a
// keyword (every message must contain it) and optionally a signing secret.
type GroupBot struct {
	URL     string
	Secret  string
	Keyword string
	HTTP    *http.Client
	now     func() time.Time
}

// Send posts text, adding the keyword when the text lacks it.
func (g *GroupBot) Send(ctx context.Context, text string) error {
	if g.Keyword != "" && !strings.Contains(text, g.Keyword) {
		text = "[" + g.Keyword + "] " + text
	}
	in := map[string]any{"msg_type": "text", "content": map[string]string{"text": trunc(text, 4000)}}
	if g.Secret != "" {
		now := time.Now
		if g.now != nil {
			now = g.now
		}
		ts := strconv.FormatInt(now().Unix(), 10)
		in["timestamp"] = ts
		in["sign"] = Sign(ts, g.Secret)
	}
	buf, _ := json.Marshal(in)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, g.URL, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	b, err := send(ctx, g.HTTP, req)
	if err != nil {
		return fmt.Errorf("feishu group bot: %w", err)
	}
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if json.Unmarshal(b, &out) == nil && out.Code != 0 {
		return fmt.Errorf("feishu group bot: code %d: %s", out.Code, out.Msg)
	}
	return nil
}

// Sign is the custom bot's signature: base64(HMAC-SHA256 with key "timestamp\nsecret" over nothing).
func Sign(timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(timestamp+"\n"+secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// ---- healthchecks.io

// Ping reports to a healthchecks.io check: success, or failure (the /fail endpoint).
func Ping(ctx context.Context, hc *http.Client, checkURL string, ok bool) error {
	if checkURL == "" {
		return errors.New("no check URL")
	}
	u := strings.TrimSuffix(checkURL, "/")
	if !ok {
		u += "/fail"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	_, err := send(ctx, hc, req)
	return err
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
