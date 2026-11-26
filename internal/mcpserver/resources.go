package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/status"
)

func jsonResource(uri string, payload any) (*mcp.ReadResourceResult, error) {
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(b),
		}},
	}, nil
}

func parseTemplateSegment(uri, prefix, suffix string) (string, bool) {
	if !strings.HasPrefix(uri, prefix) || !strings.HasSuffix(uri, suffix) {
		return "", false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(uri, prefix), suffix)
	if raw == "" || strings.Contains(raw, "/") {
		return "", false
	}
	val, err := url.PathUnescape(raw)
	if err != nil || val == "" || strings.Contains(val, "/") {
		return "", false
	}
	return val, true
}

func addResources(s *mcp.Server, b *bridge.Bridge, g SSH, o options) {
	// Static live operational resources
	s.AddResource(&mcp.Resource{
		URI:         "beacon://capabilities",
		Name:        "capabilities",
		Title:       "Beacon capabilities & domain catalog",
		MIMEType:    "application/json",
		Description: "Structured six-domain operation matrix, enabled features, MCP resources/prompts and caller-scoped permissions.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		p, err := caller(ctx)
		if err != nil {
			return nil, err
		}
		return jsonResource(req.Params.URI, api.BuildCapabilities(p, serverFeatures(b, g, o)))
	})

	s.AddResource(&mcp.Resource{
		URI:         "beacon://overview",
		Name:        "overview",
		Title:       "Operational health overview",
		MIMEType:    "application/json",
		Description: "Live alerting summary, configured human notification routes, firing counts and SSH channel posture.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if _, err := need(ctx, "status:read"); err != nil {
			return nil, err
		}
		return jsonResource(req.Params.URI, map[string]any{
			"alerting": b.Summarize(),
			"ssh":      sshStatusView(g),
			"features": serverFeatures(b, g, o),
		})
	})

	s.AddResource(&mcp.Resource{
		URI:         "beacon://alerts/firing",
		Name:        "firing_alerts",
		Title:       "Currently firing alerts",
		MIMEType:    "application/json",
		Description: "Firing alert groups, newest first, with acknowledgement, silence and escalation state.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if _, err := need(ctx, "status:read"); err != nil {
			return nil, err
		}
		alerts := b.Groups(true)
		if alerts == nil {
			alerts = []bridge.GroupView{}
		}
		return jsonResource(req.Params.URI, alertsOut{Alerts: alerts})
	})

	s.AddResource(&mcp.Resource{
		URI:         "beacon://machines",
		Name:        "machines",
		Title:       "Enrolled machines & SSH config",
		MIMEType:    "application/json",
		Description: "Enrolled machines reachable through Beacon's SSH channel, reachability and ssh_config snippet.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		who, err := gate(ctx, "status:read", "machines:ssh")
		if err != nil {
			return nil, err
		}
		if g.Gate == nil {
			return nil, fmt.Errorf("ssh.disabled: %w", sshgate.ErrDisabled)
		}
		list, err := g.Gate.List(who, g.Address)
		if err != nil {
			return nil, err
		}
		return jsonResource(req.Params.URI, list)
	})

	s.AddResource(&mcp.Resource{
		URI:         "beacon://silences",
		Name:        "silences",
		Title:       "Active alert silences",
		MIMEType:    "application/json",
		Description: "Active and pending silences, ending soonest first.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if _, err := need(ctx, "status:read"); err != nil {
			return nil, err
		}
		ss, err := b.Silences(ctx)
		if err != nil {
			return nil, err
		}
		if ss == nil {
			ss = []alertmanager.Silence{}
		}
		return jsonResource(req.Params.URI, silencesOut{Silences: ss})
	})

	s.AddResource(&mcp.Resource{
		URI:         "beacon://maintenance",
		Name:        "maintenance",
		Title:       "Maintenance windows",
		MIMEType:    "application/json",
		Description: "Planned, ongoing and recently completed maintenance windows.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if _, err := need(ctx, "status:read"); err != nil {
			return nil, err
		}
		return jsonResource(req.Params.URI, maintenanceOut{Maintenance: b.MaintenanceWindows()})
	})

	// Parameterised resource templates
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "beacon://alerts/{id}/evidence",
		Name:        "alert_evidence",
		Title:       "Alert evidence bundle",
		MIMEType:    "application/json",
		Description: "Complete diagnostic evidence for one alert group ID: per-location probe failures, service machines, related alerts, changes and sessions.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		p, err := needPrincipal(ctx, "status:read")
		if err != nil {
			return nil, err
		}
		id, ok := parseTemplateSegment(req.Params.URI, "beacon://alerts/", "/evidence")
		if !ok {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		ev, err := b.EvidenceFor(ctx, p, id, "")
		if err != nil {
			if errors.Is(err, bridge.ErrNotFound) {
				return nil, mcp.ResourceNotFoundError(req.Params.URI)
			}
			return nil, err
		}
		return jsonResource(req.Params.URI, ev)
	})

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "beacon://alerts/{id}/timeline",
		Name:        "alert_timeline",
		Title:       "Alert timeline",
		MIMEType:    "application/json",
		Description: "Chronological incident story for one alert group ID.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		p, err := needPrincipal(ctx, "status:read")
		if err != nil {
			return nil, err
		}
		id, ok := parseTemplateSegment(req.Params.URI, "beacon://alerts/", "/timeline")
		if !ok {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		tl, err := b.TimelineFor(ctx, p, id, "")
		if err != nil {
			if errors.Is(err, bridge.ErrNotFound) {
				return nil, mcp.ResourceNotFoundError(req.Params.URI)
			}
			return nil, err
		}
		return jsonResource(req.Params.URI, tl)
	})

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "beacon://sessions/{id}",
		Name:        "session_transcript",
		Title:       "SSH session transcript",
		MIMEType:    "application/json",
		Description: "Retained evidence, transcript and indexed commands for one SSH session ID.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		p, err := needPrincipal(ctx, "sessions:read", "machines:ssh")
		if err != nil {
			return nil, err
		}
		if g.Gate == nil {
			return nil, fmt.Errorf("ssh.disabled: %w", sshgate.ErrDisabled)
		}
		id, ok := parseTemplateSegment(req.Params.URI, "beacon://sessions/", "")
		if !ok {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		tr, err := g.Gate.SessionDetailFor(ctx, p, id)
		if err != nil {
			if errors.Is(err, sshgate.ErrNoSession) {
				return nil, mcp.ResourceNotFoundError(req.Params.URI)
			}
			return nil, err
		}
		return jsonResource(req.Params.URI, tr)
	})

	if o.status != nil {
		s.AddResourceTemplate(&mcp.ResourceTemplate{
			URITemplate: "beacon://status/{name}",
			Name:        "status_page",
			Title:       "Public status page",
			MIMEType:    "application/json",
			Description: "Computed public status page state, 90-day component history, incidents and maintenance.",
		}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			if _, err := need(ctx, "status:read"); err != nil {
				return nil, err
			}
			name, ok := parseTemplateSegment(req.Params.URI, "beacon://status/", "")
			if !ok {
				return nil, mcp.ResourceNotFoundError(req.Params.URI)
			}
			page, err := o.status.Page(ctx, name, "")
			if err != nil {
				if errors.Is(err, status.ErrNotFound) {
					return nil, mcp.ResourceNotFoundError(req.Params.URI)
				}
				return nil, err
			}
			return jsonResource(req.Params.URI, page)
		})
	}

	if o.config != nil {
		s.AddResourceTemplate(&mcp.ResourceTemplate{
			URITemplate: "beacon://config/{kind}",
			Name:        "config_kind",
			Title:       "Configuration section",
			MIMEType:    "application/json",
			Description: "Current configuration items for one kind: locations, heartbeats, resources, targets, modules or status_pages.",
		}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			if _, err := need(ctx, "status:read", "config:write"); err != nil {
				return nil, err
			}
			kind, ok := parseTemplateSegment(req.Params.URI, "beacon://config/", "")
			if !ok {
				return nil, mcp.ResourceNotFoundError(req.Params.URI)
			}
			view, err := o.config.Get(kind, "")
			if err != nil {
				if errors.Is(err, configdb.ErrBadInput) || errors.Is(err, configdb.ErrNotFound) {
					return nil, mcp.ResourceNotFoundError(req.Params.URI)
				}
				return nil, err
			}
			return jsonResource(req.Params.URI, view)
		})
	}
}
