package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/bridge"
)

// The caller's own token: whoami and renew_token, open to any token, as the REST routes under
// /api/v1/token are (docs/agents.md, "Tokens that renew themselves"); and revoke_token, the pause
// switch for one agent (machines:write).

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

type revokeIn struct {
	Name string `json:"name" jsonschema:"the token to revoke"`
}

func addRevokeTool(s *mcp.Server, b *bridge.Bridge) {
	mcp.AddTool(s, &mcp.Tool{Name: "revoke_token", Title: "Revoke an agent's token",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(true), IdempotentHint: true, OpenWorldHint: t(false)},
		Description: "Revoke a token that reaches machines (machines:ssh): its sessions end and its requests are refused from now on. " +
			"The pause switch for one agent. Needs machines:write. The tokens of probes, hosts, heartbeats, the rule engine, change senders and " +
			"metrics readers cannot be revoked here (they are revoked on the host), so that no alert is lost."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in revokeIn) (*mcp.CallToolResult, okOut, error) {
			who, err := gate(ctx, "machines:write")
			if err != nil {
				return nil, okOut{}, err
			}
			if err := b.RevokeToken(ctx, bridge.Actor{Name: who.Name, Via: who.Via}, in.Name); err != nil {
				return nil, okOut{}, err
			}
			return nil, okOut{OK: true}, nil
		})
}
