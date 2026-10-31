// Package vm queries VictoriaMetrics (or any Prometheus-compatible API) for instant vectors.
package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/version"
)

// Sample is one series of an instant vector.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Client queries one server.
type Client struct {
	base string
	hc   *http.Client
}

// New creates a client for the server at baseURL.
func New(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	return &Client{base: strings.TrimSuffix(baseURL, "/"), hc: hc}
}

// Query evaluates an instant query now.
func (c *Client) Query(ctx context.Context, query string) ([]Sample, error) {
	if c == nil || c.base == "" {
		return nil, nil
	}
	u := c.base + "/api/v1/query?query=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	if out.Status != "success" || out.Data.ResultType != "vector" {
		return nil, fmt.Errorf("query: status %q, result type %q", out.Status, out.Data.ResultType)
	}
	samples := make([]Sample, 0, len(out.Data.Result))
	for _, r := range out.Data.Result {
		s, _ := r.Value[1].(string)
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		samples = append(samples, Sample{Labels: r.Metric, Value: v})
	}
	return samples, nil
}
