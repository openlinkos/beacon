// Package alertmanager receives Alertmanager's webhook and talks to its API v2 (silences and alerts).
package alertmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openlinkos/status/internal/version"
)

// Webhook is the payload Alertmanager posts to a webhook receiver (version 4).
type Webhook struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	Status            string            `json:"status"` // firing or resolved
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []Alert           `json:"alerts"`
}

// Alert is one alert in a webhook payload.
type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// Decode reads and checks a webhook payload (at most 1 MiB).
func Decode(r io.Reader) (Webhook, error) {
	var w Webhook
	dec := json.NewDecoder(io.LimitReader(r, 1<<20))
	if err := dec.Decode(&w); err != nil {
		return w, fmt.Errorf("decode webhook: %w", err)
	}
	if w.Version != "4" {
		return w, fmt.Errorf("webhook version %q, want 4", w.Version)
	}
	if w.GroupKey == "" || (w.Status != "firing" && w.Status != "resolved") {
		return w, errors.New("webhook without a group key or status")
	}
	return w, nil
}

// Matcher is an Alertmanager label matcher.
type Matcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

// Silence is a silence as the API returns it.
type Silence struct {
	ID        string    `json:"id"`
	Matchers  []Matcher `json:"matchers"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	CreatedBy string    `json:"createdBy"`
	Comment   string    `json:"comment"`
	Status    struct {
		State string `json:"state"` // active, pending, expired
	} `json:"status"`
}

// Client calls the Alertmanager API v2.
type Client struct {
	base string
	hc   *http.Client
}

// New creates a client for the Alertmanager at baseURL.
func New(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{base: strings.TrimSuffix(baseURL, "/"), hc: hc}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("alertmanager: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("alertmanager %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("alertmanager %s %s: %w", method, path, err)
		}
	}
	return nil
}

// CreateSilence silences alerts matching every label in labels (exact matches) until end.
func (c *Client) CreateSilence(ctx context.Context, labels map[string]string, start, end time.Time, createdBy, comment string) (string, error) {
	if len(labels) == 0 {
		return "", errors.New("a silence needs at least one label to match")
	}
	ms := make([]Matcher, 0, len(labels))
	for k, v := range labels {
		ms = append(ms, Matcher{Name: k, Value: v, IsEqual: true})
	}
	in := map[string]any{"matchers": ms, "startsAt": start.UTC(), "endsAt": end.UTC(), "createdBy": createdBy, "comment": comment}
	var out struct {
		SilenceID string `json:"silenceID"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v2/silences", in, &out); err != nil {
		return "", err
	}
	return out.SilenceID, nil
}

// ExpireSilence ends a silence now.
func (c *Client) ExpireSilence(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v2/silence/"+url.PathEscape(id), nil, nil)
}

// Silences lists the silences that are active or pending.
func (c *Client) Silences(ctx context.Context) ([]Silence, error) {
	var all []Silence
	if err := c.do(ctx, http.MethodGet, "/api/v2/silences", nil, &all); err != nil {
		return nil, err
	}
	out := all[:0]
	for _, s := range all {
		if s.Status.State != "expired" {
			out = append(out, s)
		}
	}
	return out, nil
}

// Ready reports whether Alertmanager answers its readiness check.
func (c *Client) Ready(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/-/ready", nil, nil)
}
