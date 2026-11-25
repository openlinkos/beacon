package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/bridge"
)

type shareLinkInput struct {
	Kind string `json:"kind" jsonschema:"alert or session"`
	ID   string `json:"id" jsonschema:"the alert group ID or SSH session ID"`
}

type shareLinkRevoked struct {
	OK bool `json:"ok"`
}

func addShareLinkTools(s *mcp.Server, b *bridge.Bridge) {
	mcp.AddTool(s, &mcp.Tool{Name: "create_share_link", Title: "Create a share link",
		Description: "Make a bearer link for the current alert episode or one SSH session. Hand it only to people allowed to read the page. Requires a standalone token with accounts:write. The audit and chat omit the URL.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(false), OpenWorldHint: t(false)}},
		func(ctx context.Context, _ *mcp.CallToolRequest, input shareLinkInput) (*mcp.CallToolResult, bridge.SharedLink, error) {
			who, err := need(ctx, bridge.ShareScope)
			if err != nil {
				return nil, bridge.SharedLink{}, err
			}
			shared, err := b.CreateShareLink(ctx, who, input.Kind, input.ID)
			return nil, shared, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "revoke_share_links", Title: "Revoke share links",
		Description: "Invalidate all outstanding bearer links for one alert group or SSH session. Requires a standalone token with accounts:write.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(true), OpenWorldHint: t(false)}},
		func(ctx context.Context, _ *mcp.CallToolRequest, input shareLinkInput) (*mcp.CallToolResult, shareLinkRevoked, error) {
			who, err := need(ctx, bridge.ShareScope)
			if err != nil {
				return nil, shareLinkRevoked{}, err
			}
			err = b.RevokeShareLinks(ctx, who, input.Kind, input.ID)
			return nil, shareLinkRevoked{OK: err == nil}, err
		})
}
