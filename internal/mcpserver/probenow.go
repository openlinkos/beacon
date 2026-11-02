package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/bridge"
)

// The tool for probing on demand: after a fix, every location that probes the target probes it again at
// once, and the result holds each location's answer. It calls the same bridge method as POST /probe-now.

type probeNowIn struct {
	AlertID string `json:"alert_id,omitempty" jsonschema:"probe the targets of this alert group's alerts (their instance and module labels, or every target of their service); one of alert_id, target and service"`
	Target  string `json:"target,omitempty" jsonschema:"probe this target, exactly as the target sets name it (such as https://example.com/)"`
	Module  string `json:"module,omitempty" jsonschema:"with target: only this module; every module the target has otherwise"`
	Service string `json:"service,omitempty" jsonschema:"probe every target of this service (at most 20)"`
	Wait    int    `json:"wait,omitempty" jsonschema:"seconds to wait for the locations; default 45, at most 90"`
}

func addProbeNowTools(s *mcp.Server, b *bridge.Bridge) {
	mcp.AddTool(s, &mcp.Tool{Name: "probe_now", Title: "Probe a target again, now",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(false), OpenWorldHint: t(true)},
		Description: "Have every location that probes a target probe it again at once, and see each location's answer within the wait: " +
			"ok, the failure reason (dns, connect, timeout, tls, http_status, …), duration, HTTP status, attempts and certificate expiry; " +
			"missing lists the locations that did not answer in time, and summary says it in one line. Use it after a fix to verify, instead of " +
			"waiting for the next probe cycle; the probes also push the fresh samples, so the alert resolves sooner. Give one of alert_id, " +
			"target (with an optional module) or service. A target is probed on demand at most every 15 seconds, and a token has at most three " +
			"requests in flight."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in probeNowIn) (*mcp.CallToolResult, bridge.ProbeNow, error) {
			who, err := need(ctx, "alerts:write")
			if err != nil {
				return nil, bridge.ProbeNow{}, err
			}
			out, err := b.ProbeNow(ctx, who, bridge.ProbeNowInput{AlertID: in.AlertID, Target: in.Target, Module: in.Module, Service: in.Service, Wait: in.Wait})
			return nil, out, err
		})
}
