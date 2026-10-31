// Package mcpserver is the built-in MCP server at /mcp (streamable HTTP, stateless). It offers the
// same operations as the REST API, with the same tokens and scopes, so an agent can see what is
// alerting and act on it. Every write is audited and echoed to the alert chat.
package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/version"
)

// Instructions tell an agent what the server is for and how to use the write tools responsibly.
const Instructions = `Beacon watches services and sends alerts to the people on call through Feishu,
escalating to their phones when nobody acknowledges them. Use these tools to see what is alerting and why
(status_summary, list_alerts, get_alert, list_silences, list_audit) and, with a write token, to act on it.
Acknowledge an alert only when someone is working on it, and say what in the note. Silence only with a
reason, for as short a time as will do (24 hours at most); a silence stops notifications for every alert in
the group. Everything you write is recorded with your token's name and posted to the alert chat.`

func t(b bool) *bool { return &b }

// Handler returns the MCP endpoint, guarded by bearer tokens.
func Handler(b *bridge.Bridge, a *api.Auth, log *slog.Logger) http.Handler {
	srv := New(b)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: log})
	verify := func(_ context.Context, token string, r *http.Request) (*auth.TokenInfo, error) {
		tok, ok := a.Verify(r, token)
		if !ok {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{Scopes: tok.Scopes, Expiration: tok.Expires, UserID: tok.Name}, nil
	}
	return auth.RequireBearerToken(verify, nil)(h)
}

// need checks that the caller's token carries scope and returns who is calling.
func need(ctx context.Context, scope string) (bridge.Actor, error) {
	ti := auth.TokenInfoFromContext(ctx)
	if ti == nil || !slices.Contains(ti.Scopes, scope) {
		return bridge.Actor{}, fmt.Errorf("this token lacks the %s scope", scope)
	}
	return bridge.Actor{Name: ti.UserID, Via: "mcp"}, nil
}

// Tool inputs and outputs.
type (
	empty    struct{}
	alertsIn struct {
		State string `json:"state,omitempty" jsonschema:"firing (default) or all, which adds groups resolved in the last seven days"`
	}
	alertsOut struct {
		Alerts []bridge.GroupView `json:"alerts"`
	}
	idIn struct {
		ID string `json:"id" jsonschema:"the alert group id, as list_alerts returns it"`
	}
	ackIn struct {
		ID   string `json:"id" jsonschema:"the alert group id"`
		Note string `json:"note,omitempty" jsonschema:"what is being done about it"`
	}
	silenceIn struct {
		ID       string `json:"id" jsonschema:"the alert group id"`
		Duration string `json:"duration" jsonschema:"how long, such as 30m or 2h; at most 24h"`
		Comment  string `json:"comment" jsonschema:"why; required"`
	}
	silencesOut struct {
		Silences []alertmanager.Silence `json:"silences"`
	}
	expireIn struct {
		SilenceID string `json:"silence_id" jsonschema:"the silence id"`
	}
	okOut struct {
		OK bool `json:"ok"`
	}
	auditIn struct {
		Limit int `json:"limit,omitempty" jsonschema:"how many entries, 1 to 200; default 50"`
	}
	auditOut struct {
		Entries []store.AuditEntry `json:"entries"`
	}
)

// New builds the MCP server with its tools.
func New(b *bridge.Bridge) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "linkos-beacon", Title: "Beacon", Version: version.Version},
		&mcp.ServerOptions{Instructions: Instructions})
	read := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: t(false)}

	mcp.AddTool(s, &mcp.Tool{Name: "status_summary", Title: "Alerting summary", Annotations: read,
		Description: "Whether alerts can reach people right now (and why not), how many alert groups are firing by severity, how many are acknowledged, and the Feishu API budget used this month."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, bridge.Summary, error) {
			if _, err := need(ctx, "status:read"); err != nil {
				return nil, bridge.Summary{}, err
			}
			return nil, b.Summarize(), nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_alerts", Title: "List alerts", Annotations: read,
		Description: "Alert groups, firing first and newest first, with who acknowledged or silenced them and how far escalation went."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in alertsIn) (*mcp.CallToolResult, alertsOut, error) {
			if _, err := need(ctx, "status:read"); err != nil {
				return nil, alertsOut{}, err
			}
			if in.State != "" && in.State != "firing" && in.State != "all" {
				return nil, alertsOut{}, fmt.Errorf("state is firing or all")
			}
			out := alertsOut{Alerts: b.Groups(in.State != "all")}
			if out.Alerts == nil {
				out.Alerts = []bridge.GroupView{}
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_alert", Title: "Get an alert", Annotations: read,
		Description: "One alert group by id, with its labels, links to the runbook and the dashboard, acknowledgement, silence and escalation."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, bridge.GroupView, error) {
			if _, err := need(ctx, "status:read"); err != nil {
				return nil, bridge.GroupView{}, err
			}
			g, err := b.Group(in.ID)
			return nil, g, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_silences", Title: "List silences", Annotations: read,
		Description: "Silences that are active or pending, ending soonest first."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, silencesOut, error) {
			if _, err := need(ctx, "status:read"); err != nil {
				return nil, silencesOut{}, err
			}
			ss, err := b.Silences(ctx)
			if ss == nil {
				ss = []alertmanager.Silence{}
			}
			return nil, silencesOut{Silences: ss}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_audit", Title: "Audit log", Annotations: read,
		Description: "The latest writes (acknowledgements, silences) with who made them and through what, newest first."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
			if _, err := need(ctx, "status:read"); err != nil {
				return nil, auditOut{}, err
			}
			n := in.Limit
			if n <= 0 {
				n = 50
			}
			n = min(n, 200)
			out := auditOut{Entries: b.Audit(n)}
			if out.Entries == nil {
				out.Entries = []store.AuditEntry{}
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "ack_alert", Title: "Acknowledge an alert",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(false), IdempotentHint: true, OpenWorldHint: t(false)},
		Description: "Acknowledge a firing alert group: escalation to people's phones stops for four hours. Say what is being done in the note."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ackIn) (*mcp.CallToolResult, bridge.GroupView, error) {
			who, err := need(ctx, "alerts:write")
			if err != nil {
				return nil, bridge.GroupView{}, err
			}
			g, err := b.Ack(ctx, in.ID, who, in.Note)
			return nil, g, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "silence_alert", Title: "Silence an alert",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(false), OpenWorldHint: t(false)},
		Description: "Silence a firing alert group for a while (at most 24h) with a required comment. Notifications for every alert in the group stop until it ends; it also acknowledges the group."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in silenceIn) (*mcp.CallToolResult, bridge.GroupView, error) {
			who, err := need(ctx, "alerts:write")
			if err != nil {
				return nil, bridge.GroupView{}, err
			}
			d, err := time.ParseDuration(in.Duration)
			if err != nil {
				return nil, bridge.GroupView{}, fmt.Errorf("duration is like 30m or 2h")
			}
			g, err := b.Silence(ctx, in.ID, d, who, in.Comment)
			return nil, g, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "expire_silence", Title: "End a silence",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(true), IdempotentHint: true, OpenWorldHint: t(false)},
		Description: "End a silence now, so the alerts it covered notify again."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in expireIn) (*mcp.CallToolResult, okOut, error) {
			who, err := need(ctx, "alerts:write")
			if err != nil {
				return nil, okOut{}, err
			}
			if err := b.ExpireSilence(ctx, in.SilenceID, who); err != nil {
				return nil, okOut{}, err
			}
			return nil, okOut{OK: true}, nil
		})
	return s
}
