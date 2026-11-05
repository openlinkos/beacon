package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/secrets"
)

// The hygiene's tools: the health report for people (weak, repeated and old passwords, the leak
// corpus only when the deployment turned it on, the items whose rotation ends soon, the grants that
// sat unused), and the agent's rotation — the server makes the next value by the spec, stores it as
// the item's next version and answers with the version number, so an agent turns a secret without
// ever seeing it and then works the new value into the target system through the use action.

type rotateSecretIn struct {
	Ref     string `json:"ref" jsonschema:"the item's beacon:// reference (or collection/title)"`
	Reason  string `json:"reason" jsonschema:"why the value is turned; required"`
	Length  int    `json:"length,omitempty" jsonschema:"how long the new value is (24 by default)"`
	Charset string `json:"charset,omitempty" jsonschema:"letters (default), alnum or all"`
}

// registerHygieneTools adds the hygiene's tools; v is the vault the REST routes share.
func registerHygieneTools(s *mcp.Server, v *secrets.Service) {
	mcp.AddTool(s, &mcp.Tool{Name: "health_report", Title: "Password health and hygiene",
		Description: "The vault's hygiene in one report: password fields judged weak, repeated, old, or in the leak corpus " +
			"(only when the deployment turned the corpus on, for it sends the first five characters of each hash outward); the items whose rotation period ends soon or has passed; " +
			"and the grants that have sat unused longer than the setting allows. People only; the report carries judgements and references, never values.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, hygieneOut, error) {
			who, err := vaultActor(ctx)
			if err != nil {
				return nil, hygieneOut{}, err
			}
			health, err := v.HealthReport(who)
			if err != nil {
				return nil, hygieneOut{}, err
			}
			rotation := v.RotationDue()
			if rotation == nil {
				rotation = []secrets.RotationRow{}
			}
			unused := v.UnusedGrants()
			if unused == nil {
				unused = []secrets.Grant{}
			}
			return nil, hygieneOut{Health: health, Rotation: rotation, Unused: unused}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "rotate_secret", Title: "Turn a secret",
		Description: "Turn an item's value: the server makes the new one by the spec (length, charset), stores it as the item's next version " +
			"and answers with the version number — the value is never given out. The caller must hold a live use grant over the item; work the new value into the target system " +
			"through the use action (run_command with secrets, http_request, file delivery).",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in rotateSecretIn) (*mcp.CallToolResult, secrets.Item, error) {
			p, err := agentPrincipal(ctx)
			if err != nil {
				return nil, secrets.Item{}, err
			}
			if !p.Has("secrets:use") {
				return nil, secrets.Item{}, errors.New(api.ScopeProblem(p, "secrets:use"))
			}
			item, err := v.RotateItem(p.Agent, "mcp", in.Ref, in.Reason, secrets.GenSpec{Length: in.Length, Charset: in.Charset})
			return nil, item, err // the overview with its version number, never the value
		})
}

// hygieneOut is the health report tool's answer.
type hygieneOut struct {
	Health   secrets.HealthReport  `json:"health"`
	Rotation []secrets.RotationRow `json:"rotation"`
	Unused   []secrets.Grant       `json:"unused_grants"`
}
