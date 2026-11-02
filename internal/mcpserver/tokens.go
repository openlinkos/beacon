package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/bridge"
)

// The caller's own token: whoami and renew_token, open to any token, as the REST routes under
// /api/v1/token are (docs/agents.md, "Tokens that renew themselves").

// caller is who is calling, whatever its scopes.
func caller(ctx context.Context) (bridge.Actor, error) {
	ti := auth.TokenInfoFromContext(ctx)
	if ti == nil {
		return bridge.Actor{}, errors.New("no token")
	}
	return bridge.Actor{Name: ti.UserID, Via: "mcp"}, nil
}

func addTokenTools(s *mcp.Server, b *bridge.Bridge) {
	mcp.AddTool(s, &mcp.Tool{Name: "whoami", Title: "Your token", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: t(false)},
		Description: "Your own token: its name, scopes, the machines it reaches, when it expires, its lifetime, and whether and when it may renew itself."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, bridge.TokenView, error) {
			who, err := caller(ctx)
			if err != nil {
				return nil, bridge.TokenView{}, err
			}
			v, err := b.Whoami(who)
			return nil, v, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "renew_token", Title: "Renew your token",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(false), OpenWorldHint: t(false)},
		Description: "Give your token a new secret, expiring its lifetime from now, with the same name, scopes, machines and keys. The secret is shown once: store it where your harness reads it. The old secret keeps working for ten minutes. At most once a day; enrolled machines' tokens cannot (enroll -rotate replaces them). Recorded and echoed to the alert chat."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, bridge.Renewed, error) {
			who, err := caller(ctx)
			if err != nil {
				return nil, bridge.Renewed{}, err
			}
			out, err := b.RenewToken(ctx, who)
			return nil, out, err
		})
}
