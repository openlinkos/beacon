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

// Point is one value of a series over a range.
type Point struct {
	T time.Time
	V float64
}

// Series is one series of a range query, its points in time order.
type Series struct {
	Labels map[string]string
	Points []Point
}

func unix(t time.Time) string { return strconv.FormatFloat(float64(t.UnixMilli())/1000, 'f', 3, 64) }

// get asks path with the query parameters and returns the result of the expected type.
func (c *Client) get(ctx context.Context, path string, q url.Values, resultType string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+q.Encode(), nil)
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
			ResultType string          `json:"resultType"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	if out.Status != "success" || out.Data.ResultType != resultType {
		return nil, fmt.Errorf("query: status %q, result type %q", out.Status, out.Data.ResultType)
	}
	return out.Data.Result, nil
}

// point reads a [time, "value"] pair.
func point(p [2]any) (Point, bool) {
	ts, ok1 := p[0].(float64)
	s, ok2 := p[1].(string)
	v, err := strconv.ParseFloat(s, 64)
	if !ok1 || !ok2 || err != nil {
		return Point{}, false
	}
	return Point{T: time.UnixMilli(int64(ts * 1000)).UTC(), V: v}, true
}

// QueryAt evaluates an instant query at t; the zero t is the server's now.
func (c *Client) QueryAt(ctx context.Context, query string, t time.Time) ([]Sample, error) {
	if c == nil || c.base == "" {
		return nil, nil
	}
	q := url.Values{"query": {query}}
	if !t.IsZero() {
		q.Set("time", unix(t))
	}
	raw, err := c.get(ctx, "/api/v1/query", q, "vector")
	if err != nil {
		return nil, err
	}
	var result []struct {
		Metric map[string]string `json:"metric"`
		Value  [2]any            `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	out := make([]Sample, 0, len(result))
	for _, r := range result {
		if p, ok := point(r.Value); ok {
			out = append(out, Sample{Labels: r.Metric, Value: p.V})
		}
	}
	return out, nil
}

// QueryRange evaluates a range query from start to end, every step.
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]Series, error) {
	if c == nil || c.base == "" {
		return nil, nil
	}
	q := url.Values{"query": {query}, "start": {unix(start)}, "end": {unix(end)}, "step": {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)}}
	raw, err := c.get(ctx, "/api/v1/query_range", q, "matrix")
	if err != nil {
		return nil, err
	}
	var result []struct {
		Metric map[string]string `json:"metric"`
		Values [][2]any          `json:"values"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	out := make([]Series, 0, len(result))
	for _, r := range result {
		s := Series{Labels: r.Metric}
		for _, v := range r.Values {
			if p, ok := point(v); ok {
				s.Points = append(s.Points, p)
			}
		}
		out = append(out, s)
	}
	return out, nil
}
