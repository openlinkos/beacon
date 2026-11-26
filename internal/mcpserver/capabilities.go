package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
)

// registerTool registers an MCP tool and enriches it with canonical domain, access,
// REST parity and required scope metadata from the shared capability catalogue.
func registerTool[In, Out any](s *mcp.Server, tool *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	if meta := api.ToolMeta(tool.Name); meta != nil {
		if tool.Meta == nil {
			tool.Meta = mcp.Meta(meta)
		} else {
			for k, v := range meta {
				if _, exists := tool.Meta[k]; !exists {
					tool.Meta[k] = v
				}
			}
		}
	}
	mcp.AddTool(s, tool, h)
}

func serverFeatures(b *bridge.Bridge, g SSH, o options) api.Features {
	return api.Features{
		SSH:             g.Gate != nil,
		SSHPaused:       g.Gate != nil && g.Gate.Paused(),
		Config:          o.config != nil,
		Status:          o.status != nil,
		ProbeNow:        b != nil && b.CanProbeNow(),
		TeamCredentials: o.credentials != nil || o.credentialsConfigured,
		ShareLinks:      b != nil && b.AlertPage("probe") != "",
		MCPEndpoint:     "/mcp",
		OpenAPIURL:      "/api/v1/openapi.yaml",
		DocsURL:         api.DocsURL,
	}
}

type sshStatusOut struct {
	Enabled          bool       `json:"enabled"`
	Paused           bool       `json:"paused"`
	PausedSince      *time.Time `json:"paused_since,omitempty"`
	Address          string     `json:"address,omitempty"`
	HostKey          string     `json:"host_key,omitempty"`
	TrustedUserCAKey string     `json:"trusted_user_ca_key,omitempty"`
}

func sshStatusView(g SSH) sshStatusOut {
	out := sshStatusOut{Enabled: g.Gate != nil}
	if g.Gate != nil {
		out.Paused = g.Gate.Paused()
		if ps := g.Gate.PausedSince(); !ps.IsZero() {
			out.PausedSince = &ps
		}
		out.Address = g.Address
		out.HostKey = g.Gate.HostKey()
		out.TrustedUserCAKey = g.Gate.CAPublicKey()
	}
	return out
}

func addCapabilityTools(s *mcp.Server, b *bridge.Bridge, g SSH, o options) {
	read := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: t(false)}

	registerTool(s, &mcp.Tool{
		Name:        "describe_capabilities",
		Title:       "Discover Beacon domains and caller capabilities",
		Annotations: read,
		Description: "Return Beacon's six operational domains (incidents, monitoring, machines, status, identity, governance), " +
			"enabled instance features, MCP resources and prompts, and every tool/REST operation marked with whether your current " +
			"credential and scopes allow calling it right now. Call this first when connecting to an unfamiliar Beacon deployment.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, api.Capabilities, error) {
		p, err := caller(ctx)
		if err != nil {
			return nil, api.Capabilities{}, err
		}
		return nil, api.BuildCapabilities(p, serverFeatures(b, g, o)), nil
	})

	registerTool(s, &mcp.Tool{
		Name:        "get_ssh_status",
		Title:       "Get SSH channel status",
		Annotations: read,
		Description: "Check whether Beacon's SSH channel is enabled or paused, and retrieve its public address, server host key and certificate authority public key.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, sshStatusOut, error) {
		if _, err := need(ctx, "status:read", "machines:ssh"); err != nil {
			return nil, sshStatusOut{}, err
		}
		return nil, sshStatusView(g), nil
	})
}
