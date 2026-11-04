package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/testutil"
)

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

func connect(t *testing.T, url, token string) *mcp.ClientSession {
	t.Helper()
	tr := &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(), tr, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	b, _ := json.Marshal(res.StructuredContent)
	text := string(b)
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += " " + tc.Text
		}
	}
	return res, text
}

// TestLockoutNamesTheWait checks that MCP, which can only answer 401, says the address is locked
// and how long to wait, instead of the bare "invalid token" a bad credential gets.
func TestLockoutNamesTheWait(t *testing.T) {
	e := testutil.New(t)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store, Now: func() time.Time { return now }}, slog.Default()))
	defer srv.Close()
	post := func(token string) (int, string, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header.Get("Retry-After")
	}
	if code, body, retry := post("lbt_wrong"); code != http.StatusUnauthorized || !strings.Contains(body, "invalid token") || strings.Contains(body, "try again in") || retry != "" {
		t.Fatalf("one bad token: %d Retry-After=%q %s", code, retry, body)
	}
	for range 19 {
		post("lbt_wrong")
	}
	code, body, retry := post(e.Token(t, "reader", "status:read"))
	if code != http.StatusUnauthorized || !strings.Contains(body, "too many failed sign-ins from this address; try again in 10 minutes") || retry != "600" {
		t.Fatalf("lockout: %d Retry-After=%q %s", code, retry, body)
	}
}

func TestMCP(t *testing.T) {
	e := testutil.New(t)
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default()))
	defer srv.Close()

	// no token: refused before MCP starts
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a token: %d", resp.StatusCode)
	}

	read := connect(t, srv.URL, e.Token(t, "reader", "status:read"))
	tools, err := read.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 36 { // with the maintenance tools and the people's and keys' tools
		t.Fatalf("tools: %v %v", tools, err)
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || tool.Description == "" {
			t.Errorf("%s needs annotations and a description", tool.Name)
		}
	}
	if _, text := call(t, read, "list_alerts", nil); !strings.Contains(text, testutil.GroupID()) || !strings.Contains(text, "hub.example.com is down") {
		t.Errorf("list_alerts: %s", text)
	}
	if _, text := call(t, read, "status_summary", nil); !strings.Contains(text, `"critical":1`) {
		t.Errorf("status_summary: %s", text)
	}
	if res, text := call(t, read, "ack_alert", map[string]any{"id": testutil.GroupID()}); !res.IsError || !strings.Contains(text, "alerts:write") {
		t.Errorf("a read token cannot acknowledge: %v %s", res.IsError, text)
	}

	write := connect(t, srv.URL, e.Token(t, "claude-code", "status:read", "alerts:write"))
	if res, text := call(t, write, "silence_alert", map[string]any{"id": testutil.GroupID(), "duration": "2h"}); !res.IsError {
		t.Errorf("a silence needs a comment: %s", text)
	}
	res, text := call(t, write, "silence_alert", map[string]any{"id": testutil.GroupID(), "duration": "2h", "comment": "harbor upgrade"})
	if res.IsError || !strings.Contains(text, `"via":"mcp"`) || !strings.Contains(text, `"by":"claude-code"`) {
		t.Fatalf("silence_alert: %s", text)
	}
	if _, text := call(t, write, "list_audit", map[string]any{"limit": 5}); !strings.Contains(text, `"via":"mcp"`) || !strings.Contains(text, `"total":`) {
		t.Errorf("audit: %s", text)
	}
	// the filter the event table makes possible
	if _, text := call(t, write, "list_audit", map[string]any{"action": "ack"}); !strings.Contains(text, `"total":0`) {
		t.Errorf("audit by action: %s", text) // nothing was acknowledged in this test
	}
	if res, _ := call(t, write, "expire_silence", map[string]any{"silence_id": "sil-1"}); res.IsError || len(e.AM.Expired) != 1 {
		t.Errorf("expire_silence failed")
	}
}
