package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/store"
)

// The grants' tools: the agent's door (search_secrets, request_secret, get_request, get_secret,
// list_grants, renew_grant, release_grant) and the people's and reviewers' (list_requests,
// decide_request, pass_request, the entitlements, the revocation, an agent's scope). Both call
// the same vault methods the REST routes call; the agent's tools take the agent from the
// credential itself.

// agentPrincipal is the calling agent; ok false when the caller is not one.
func agentPrincipal(ctx context.Context) (store.Principal, error) {
	p, ok := principal(ctx)
	if !ok {
		return store.Principal{}, errors.New("no credential")
	}
	if p.Agent == "" {
		return store.Principal{}, errors.New("these tools are an agent's; a person works the vault in the console")
	}
	return p, nil
}

// grantPrincipal is the calling agent or person, with the scope named.
func grantPrincipal(ctx context.Context, scope string) (store.Principal, error) {
	p, ok := principal(ctx)
	if !ok {
		return store.Principal{}, errors.New("no credential")
	}
	if !p.Has(scope) {
		return store.Principal{}, errors.New(api.ScopeProblem(p, scope))
	}
	return p, nil
}

// grantIDIn names a grant, with the reason an action may carry.
type grantIDIn struct {
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty"`
}

type requestSecretIn struct {
	Items       []string `json:"items,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Collection  string   `json:"collection,omitempty"`
	Fields      []string `json:"fields,omitempty"`
	Action      string   `json:"action,omitempty"`
	Reason      string   `json:"reason"`
	Uses        *int64   `json:"uses,omitempty"`
	Duration    *int64   `json:"duration,omitempty"` // seconds; 0: no expiry
	Alert       string   `json:"alert,omitempty"`
	Context     string   `json:"context,omitempty"`
	Entitlement string   `json:"entitlement,omitempty"`
	WaitSeconds int64    `json:"wait,omitempty"`
}

type decideRequestIn struct {
	ID       string `json:"id"`
	Approve  bool   `json:"approve"`
	Note     string `json:"note"`
	Uses     *int64 `json:"uses,omitempty"`
	Duration *int64 `json:"duration,omitempty"`
}

type passRequestIn struct {
	ID   string `json:"id"`
	Note string `json:"note"`
}

// seesSecretRequest is who may read an ask: the agent that made it, a review agent working the
// queue, or a person.
func seesSecretRequest(p store.Principal, req secrets.Request) bool {
	if p.Person() || p.Has("secrets:review") {
		return true
	}
	return p.Agent != "" && req.Agent == p.Agent
}

type refReasonIn struct {
	Ref     string `json:"ref"`
	Field   string `json:"field,omitempty"`
	Reason  string `json:"reason"`
	Alert   string `json:"alert,omitempty"`
	Context string `json:"context,omitempty"`
}

// registerGrantsTools adds the grants' tools; v is the vault the REST routes share.
func registerGrantsTools(s *mcp.Server, v *secrets.Service) {
	mcp.AddTool(s, &mcp.Tool{Name: "request_secret", Title: "Request secret access",
		Description: "Ask for items (by reference, or picked by tags), with a reason. The answer splits by the approval each item needs: " +
			"the auto part is granted at once, the rest waits for a decision (get_request follows it). The uses and duration are clamped by the settings.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in requestSecretIn) (*mcp.CallToolResult, secrets.RequestOutcome, error) {
			p, err := agentPrincipal(ctx)
			if err != nil {
				return nil, secrets.RequestOutcome{}, err
			}
			if !p.Has("secrets:use") {
				return nil, secrets.RequestOutcome{}, errors.New(api.ScopeProblem(p, "secrets:use"))
			}
			var wait time.Duration
			if in.WaitSeconds > 0 {
				wait = time.Duration(in.WaitSeconds) * time.Second
			}
			out, err := v.RequestSecret(p.Agent, "mcp", secrets.RequestIn{
				Items: in.Items, Tags: in.Tags, Collection: in.Collection, Fields: in.Fields,
				Action: in.Action, Reason: in.Reason, Uses: in.Uses, Duration: in.Duration,
				Alert: in.Alert, Context: in.Context, Entitlement: in.Entitlement, Wait: wait,
			})
			if out.Granted == nil {
				out.Granted = []secrets.Grant{}
			}
			return nil, out, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_request", Title: "Secret request",
		Description: "One request by its id: the items, the reason, the decisions so far, and the review context (the chain, the alert it hangs on, the asker's recent record). The agent sees its own; a person and a review agent see the queue.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, secrets.RequestWithContext, error) {
			p, err := grantPrincipal(ctx, "secrets:describe")
			if err != nil {
				return nil, secrets.RequestWithContext{}, err
			}
			req, ok := v.Request(in.ID)
			if !ok || !seesSecretRequest(p, req) {
				return nil, secrets.RequestWithContext{}, errors.New("no request " + in.ID)
			}
			ctxv := v.ReviewContextOf(req)
			return nil, secrets.RequestWithContext{Request: req, Context: &ctxv}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_secret", Title: "Get a secret",
		Description: "Take the value of a field the agent holds a grant for, by its beacon:// reference (or collection/title, with an optional /field). " +
			"Every use carries a reason; the use is recorded before the value is given, so a use that cannot be recorded gives nothing. An otp field answers with the current code and the seconds it has left.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in refReasonIn) (*mcp.CallToolResult, secrets.UseResult, error) {
			p, err := agentPrincipal(ctx)
			if err != nil {
				return nil, secrets.UseResult{}, err
			}
			if !p.Has("secrets:read") {
				return nil, secrets.UseResult{}, errors.New(api.ScopeProblem(p, "secrets:read"))
			}
			out, err := v.GetSecret(p.Agent, "mcp", in.Ref, in.Field, in.Reason, in.Alert, in.Context, "", "mcp")
			return nil, out, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_grants", Title: "My grants",
		Description: "The grants the calling agent holds: what items, which fields, how many uses left, until when, bound to which alert.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, grantsOut, error) {
			p, err := agentPrincipal(ctx)
			if err != nil {
				return nil, grantsOut{}, err
			}
			out := v.GrantsOf(p.Agent)
			if out == nil {
				out = []secrets.Grant{}
			}
			return nil, grantsOut{Grants: out}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "renew_grant", Title: "Renew a grant",
		Description: "Renew one of the calling agent's grants. Auto-level grants renew at once; the rest ask again, and the answer says which request waits.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in grantIDIn) (*mcp.CallToolResult, renewOut, error) {
			p, err := agentPrincipal(ctx)
			if err != nil {
				return nil, renewOut{}, err
			}
			if !p.Has("secrets:use") {
				return nil, renewOut{}, errors.New(api.ScopeProblem(p, "secrets:use"))
			}
			grant, pending, err := v.RenewGrant(p.Agent, in.ID, in.Reason, "mcp")
			return nil, renewOut{Grant: grant, Pending: pending}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "release_grant", Title: "Release a grant",
		Description: "Give one of the calling agent's grants back early.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in grantIDIn) (*mcp.CallToolResult, releasedOut, error) {
			p, err := agentPrincipal(ctx)
			if err != nil {
				return nil, releasedOut{}, err
			}
			if !p.Has("secrets:use") {
				return nil, releasedOut{}, errors.New(api.ScopeProblem(p, "secrets:use"))
			}
			return nil, releasedOut{}, v.ReleaseGrant(p.Agent, in.ID, "mcp")
		})

	// ---- the people's and reviewers' tools

	mcp.AddTool(s, &mcp.Tool{Name: "list_requests", Title: "Secret requests",
		Description: "The secret requests by state (pending by default), for people deciding them and review agents working the queue. An agent sees its own.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			State string `json:"state,omitempty"`
		}) (*mcp.CallToolResult, requestsOut, error) {
			p, err := grantPrincipal(ctx, "secrets:describe")
			if err != nil {
				return nil, requestsOut{}, err
			}
			state := in.State
			if state == "" {
				state = secrets.ReqPending
			}
			var out []secrets.Request
			for _, r := range v.Requests(state) {
				if seesSecretRequest(p, r) {
					out = append(out, r)
				}
			}
			if out == nil {
				out = []secrets.Request{}
			}
			return nil, requestsOut{Requests: out}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "decide_request", Title: "Decide a secret request",
		Description: "Approve or deny a waiting request, with the reason a decision must carry; approving may set the uses and duration, inside the ceilings. " +
			"The requester itself never decides its own request. People of the operator and owner roles and the reviewers an entitlement names may decide; a review agent may for the levels it is allowed.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in decideRequestIn) (*mcp.CallToolResult, decidedOut, error) {
			p, ok := principal(ctx)
			if !ok {
				return nil, decidedOut{}, errors.New("no credential")
			}
			who := secrets.DecideActor{Via: "mcp"}
			switch {
			case p.Agent != "":
				who.Name, who.Kind = p.Agent, "agent"
			case p.Person():
				who.Name, who.Kind = p.User, "person"
				if p.Has("accounts:write") {
					who.Role = "owner"
				} else if p.Has("machines:write") {
					who.Role = "operator"
				}
			default:
				return nil, decidedOut{}, errors.New("a credential that is neither a person nor an agent cannot decide")
			}
			req, grant, err := v.Decide(who, in.ID, in.Approve, in.Note, in.Uses, in.Duration)
			return nil, decidedOut{Request: req, Grant: grant}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "pass_request", Title: "Pass a secret request on",
		Description: "Hand a waiting request to the next layer of its review chain without deciding it. Only a review agent at the agents layer may, and every pass carries a reason.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in passRequestIn) (*mcp.CallToolResult, passedOut, error) {
			p, ok := principal(ctx)
			if !ok {
				return nil, passedOut{}, errors.New("no credential")
			}
			if p.Agent == "" {
				return nil, passedOut{}, errors.New("only a review agent passes a request on")
			}
			req, err := v.Pass(secrets.DecideActor{Name: p.Agent, Kind: "agent", Via: "mcp"}, in.ID, in.Note)
			return nil, passedOut{Request: req}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_entitlements", Title: "Entitlements",
		Description: "The entitlements: who may ask for what, with which actions and approval floor. Needs secrets:admin.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, entitlementsOut, error) {
			if _, err := vaultAdmin(ctx); err != nil {
				return nil, entitlementsOut{}, err
			}
			out := v.Entitlements()
			if out == nil {
				out = []secrets.Entitlement{}
			}
			return nil, entitlementsOut{Entitlements: out}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "put_entitlement", Title: "Put an entitlement",
		Description: "Add or change an entitlement: principals (agents, agent tags, agents of someone), resources (collections, tag patterns, items, fields), " +
			"actions, the approval floor, the reviewers, whether it is a template for onboarding, and its own expiry. Needs secrets:admin.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in secrets.Entitlement) (*mcp.CallToolResult, secrets.Entitlement, error) {
			who, err := vaultAdmin(ctx)
			if err != nil {
				return nil, secrets.Entitlement{}, err
			}
			out, err := v.SetEntitlement(who, in)
			return nil, out, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "delete_entitlement", Title: "Delete an entitlement",
		Description: "Remove an entitlement; the grants that reached through it are re-assessed. Needs secrets:admin.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, deletedOut, error) {
			who, err := vaultAdmin(ctx)
			if err != nil {
				return nil, deletedOut{}, err
			}
			return nil, deletedOut{}, v.DeleteEntitlement(who, in.ID)
		})

	mcp.AddTool(s, &mcp.Tool{Name: "set_agent_tags", Title: "Set an agent's tags",
		Description: "Set an agent's grouping tags (role/on-call and the like), which entitlements reach agents by; none clears them. Needs secrets:admin.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			Agent string   `json:"agent"`
			Tags  []string `json:"tags"`
		}) (*mcp.CallToolResult, tagsSetOut, error) {
			who, err := vaultAdmin(ctx)
			if err != nil {
				return nil, tagsSetOut{}, err
			}
			if err := v.SetAgentTags(who, in.Agent, in.Tags); err != nil {
				return nil, tagsSetOut{}, err
			}
			return nil, tagsSetOut{Tags: v.TagsOfAgent(in.Agent)}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "revoke_grant", Title: "Revoke a grant",
		Description: "End a grant by hand, with the reason. Needs secrets:admin.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in grantIDIn) (*mcp.CallToolResult, revokedOut, error) {
			who, err := vaultAdmin(ctx)
			if err != nil {
				return nil, revokedOut{}, err
			}
			return nil, revokedOut{}, v.RevokeGrant(who, in.ID, in.Reason)
		})

	mcp.AddTool(s, &mcp.Tool{Name: "agent_secret_scope", Title: "An agent's secret scope",
		Description: "What an agent can reach: the entitlements that take it in, the items they cover by the approval each needs, its grants and its tags, " +
			"and the templates an owner may hand it. The owner or an admin.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			Agent string `json:"agent"`
		}) (*mcp.CallToolResult, scopeOut, error) {
			p, ok := principal(ctx)
			if !ok {
				return nil, scopeOut{}, errors.New("no credential")
			}
			if !p.Person() {
				return nil, scopeOut{}, errors.New("agents reach items through grants, not this door: request_secret asks, get_secret takes")
			}
			owner, ok := v.AgentOwner(in.Agent)
			if !ok {
				return nil, scopeOut{}, errors.New("no agent " + in.Agent)
			}
			if p.User != owner && !p.Has("secrets:admin") && !p.Has("accounts:write") {
				return nil, scopeOut{}, errors.New("the owner or an admin sees an agent's scope")
			}
			return nil, scopeOut{Agent: in.Agent, Entitlements: v.EntitlementsOfAgent(in.Agent),
				Preview: v.PreviewForAgent(in.Agent), Grants: v.GrantsOf(in.Agent), Tags: v.TagsOfAgent(in.Agent),
				Templates: v.TemplateEntitlements()}, nil
		})
}

// the tools' answer shapes
type grantsOut struct {
	Grants []secrets.Grant `json:"grants"`
}

type renewOut struct {
	Grant   secrets.Grant    `json:"grant"`
	Pending *secrets.Request `json:"pending,omitempty"`
}

type releasedOut struct {
	Released bool `json:"released"`
}

type requestsOut struct {
	Requests []secrets.Request `json:"requests"`
}

type decidedOut struct {
	Request secrets.Request `json:"request"`
	Grant   secrets.Grant   `json:"grant"`
}

type passedOut struct {
	Request secrets.Request `json:"request"`
}

type entitlementsOut struct {
	Entitlements []secrets.Entitlement `json:"entitlements"`
}

type deletedOut struct {
	Deleted bool `json:"deleted"`
}

type tagsSetOut struct {
	Tags []string `json:"tags"`
}

type revokedOut struct {
	Revoked bool `json:"revoked"`
}

type scopeOut struct {
	Agent        string                `json:"agent"`
	Entitlements []secrets.Entitlement `json:"entitlements"`
	Preview      secrets.ScopePreview  `json:"preview"`
	Grants       []secrets.Grant       `json:"grants"`
	Tags         []string              `json:"tags"`
	Templates    []secrets.Entitlement `json:"templates"`
}
