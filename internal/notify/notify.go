// Package notify sends through the channels that are not the Feishu app: Pushover (iOS emergency
// alerts that repeat until acknowledged), Bark (an iOS push that rings), the Feishu group's custom
// bot (plain messages that need no app and spend no API quota), signed team webhooks, and healthchecks.io pings.
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

var directHTTP = &http.Client{Timeout: 15 * time.Second, CheckRedirect: noRedirect}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func client(hc *http.Client) *http.Client {
	if hc != nil {
		direct := *hc
		direct.CheckRedirect = noRedirect
		return &direct
	}
	return directHTTP
}

func send(ctx context.Context, hc *http.Client, method, endpoint, contentType string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, errors.New("invalid provider request")
	}
	req.Header.Set("User-Agent", version.UserAgent())
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client(hc).Do(req)
	switch {
	case errors.Is(err, context.Canceled):
		return nil, context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return nil, context.DeadlineExceeded
	case err != nil:
		// Callback endpoints can carry credentials in their paths. Transport errors may echo them.
		return nil, errors.New("provider request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	const replyLimit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, replyLimit+1))
	if err != nil || len(data) > replyLimit {
		return nil, errors.New("provider response was unreadable or too large")
	}
	return data, nil
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

// Emergency is an emergency-priority message.
type Emergency struct {
	Title, Message, URL, URLTitle string
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
	b, err := send(ctx, p.HTTP, http.MethodPost, p.API+"/1/messages.json", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("pushover: %w", err)
	}
	var out struct {
		Status  int    `json:"status"`
		Receipt string `json:"receipt"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Status != 1 || out.Receipt == "" {
		return "", errors.New("pushover: response did not confirm acceptance")
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
	b, err := send(ctx, p.HTTP, http.MethodGet, u, "", nil)
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
		return Receipt{}, errors.New("pushover receipt: invalid response")
	}
	r := Receipt{Acknowledged: out.Acknowledged == 1, Device: out.AcknowledgedByDevice, Expired: out.Expired == 1}
	if out.AcknowledgedAt > 0 {
		r.At = time.Unix(out.AcknowledgedAt, 0)
	}
	return r, nil
}

// Cancel stops retries for a single emergency message identified by its receipt.
func (p *Pushover) Cancel(ctx context.Context, receipt string) error {
	if receipt == "" || len(receipt) > 64 || strings.ContainsFunc(receipt, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	}) {
		return errors.New("pushover cancel: invalid receipt")
	}
	form := url.Values{"token": {p.Token}}
	u := p.API + "/1/receipts/" + receipt + "/cancel.json"
	body, err := send(ctx, p.HTTP, http.MethodPost, u, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("pushover cancel: %w", err)
	}
	var reply struct {
		Status int `json:"status"`
	}
	if json.Unmarshal(body, &reply) != nil || reply.Status != 1 {
		return errors.New("pushover cancel: response did not confirm acceptance")
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
	in := map[string]string{"title": trunc(title, 200), "body": trunc(body, 1000), "level": "critical", "call": "1", "group": "beacon", "volume": "8"}
	if link != "" {
		in["url"] = link
	}
	buf, _ := json.Marshal(in)
	reply, err := send(ctx, b.HTTP, http.MethodPost, b.URL, "application/json; charset=utf-8", bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("bark: %w", err)
	}
	var out struct {
		Code *int `json:"code"`
	}
	if json.Unmarshal(reply, &out) != nil || out.Code == nil {
		return errors.New("bark: response did not confirm acceptance")
	}
	if *out.Code != http.StatusOK {
		return fmt.Errorf("bark: code %d", *out.Code)
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

// noMentions keeps a plain message from mentioning anyone (<at user_id="all">), whatever it quotes:
// angle brackets become single angle quotation marks.
var noMentions = strings.NewReplacer("<", string(rune(0x2039)), ">", string(rune(0x203A)))

// Send posts text, adding the keyword when the text lacks it. The text mentions nobody: what it quotes
// (an alert's title, a command run on a machine) comes from outside Beacon.
func (g *GroupBot) Send(ctx context.Context, text string) error {
	text = noMentions.Replace(text)
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
	b, err := send(ctx, g.HTTP, http.MethodPost, g.URL, "application/json; charset=utf-8", bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("feishu group bot: %w", err)
	}
	var out struct {
		Code       *int `json:"code"`
		StatusCode *int `json:"StatusCode"`
	}
	if json.Unmarshal(b, &out) != nil || (out.Code == nil && out.StatusCode == nil) {
		return errors.New("feishu group bot: response did not confirm acceptance")
	}
	for _, code := range []*int{out.Code, out.StatusCode} {
		if code != nil && *code != 0 {
			return fmt.Errorf("feishu group bot: code %d", *code)
		}
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
	_, err := send(ctx, hc, http.MethodPost, u, "", nil)
	return err
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
