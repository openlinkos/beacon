package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/bridge"
)

type incidentEpisodesIn struct {
	Since    string `json:"since,omitempty" jsonschema:"inclusive RFC 3339 start-time bound, defaults to 24 hours before until"`
	Until    string `json:"until,omitempty" jsonschema:"exclusive RFC 3339 start-time bound, defaults to the current time"`
	Before   string `json:"before,omitempty" jsonschema:"cursor from the preceding page, with its returned since and until and the same filters"`
	Group    string `json:"group,omitempty" jsonschema:"only this alert group ID"`
	Severity string `json:"severity,omitempty" jsonschema:"only critical, warning or info, using the opening severity where recorded"`
	Origin   string `json:"origin,omitempty" jsonschema:"exercise for verified drills or unknown for unclassified episodes"`
	Limit    int    `json:"limit,omitempty" jsonschema:"page size (default 50, maximum 200)"`
}

func addIncidentEpisodeTool(s *mcp.Server, b *bridge.Bridge) {
	registerTool(s, &mcp.Tool{Name: "list_incident_episodes", Title: "List incidents by start time",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: t(false)},
		Description: "List retained alert episodes by start time, including those still open. Hook acceptance, programme reports, human participation and verification are separate observations. Unclassified records remain unknown."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in incidentEpisodesIn) (*mcp.CallToolResult, bridge.IncidentEpisodePage, error) {
			if _, err := need(ctx, "status:read"); err != nil {
				return nil, bridge.IncidentEpisodePage{}, err
			}
			page, err := b.IncidentEpisodes(ctx, bridge.IncidentEpisodeQuery{
				Since: in.Since, Until: in.Until, Before: in.Before,
				Group: in.Group, Severity: in.Severity, Origin: in.Origin, Limit: in.Limit,
			})
			return nil, page, err
		})
}
