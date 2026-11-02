package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	if err != nil || len(tools.Tools) != 24 {
		t.Fatalf("tools: %v %v", tools, err)
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || tool.Description == "" {
			t.Errorf("%s needs annotations and a description", tool.Name)
		}
	}
	if _, text := call(t, read, "list_alerts", nil); !strings.Contains(text, testutil.GroupID()) || !strings.Contains(text, "hub.linkos.io is down") {
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
	if _, text := call(t, write, "list_audit", map[string]any{"limit": 5}); !strings.Contains(text, `"via":"mcp"`) {
		t.Errorf("audit: %s", text)
	}
	if res, _ := call(t, write, "expire_silence", map[string]any{"silence_id": "sil-1"}); res.IsError || len(e.AM.Expired) != 1 {
		t.Errorf("expire_silence failed")
	}
}
