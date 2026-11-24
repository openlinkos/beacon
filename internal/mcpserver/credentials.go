package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/secrets"
)

func WithTeamCredentials(c *secrets.Service, configured bool) Option {
	return func(o *options) { o.credentials, o.credentialsConfigured = c, configured }
}

type credentialsOut struct {
	Credentials []secrets.Credential `json:"credentials"`
}

type credentialID struct {
	ID string `json:"id" jsonschema:"the credential id from list_team_credentials"`
}

func addCredentialTools(s *mcp.Server, c *secrets.Service) {
	read := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: t(false)}
	outbound := &mcp.ToolAnnotations{DestructiveHint: t(false), OpenWorldHint: t(true)}
	mcp.AddTool(s, &mcp.Tool{Name: "list_team_credentials", Title: "List team credentials",
		Description: "List credential metadata. An agent sees only its live grants.", Annotations: read},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, credentialsOut, error) {
			if c == nil {
				return nil, credentialsOut{}, secrets.ErrState
			}
			p, _ := principal(ctx)
			list, err := c.List(p)
			return nil, credentialsOut{Credentials: list}, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "use_team_credential", Title: "Use an authorised team credential",
		Description: "Make the owner's configured HTTPS request. Returns its status code without disclosing the value.", Annotations: outbound},
		func(ctx context.Context, _ *mcp.CallToolRequest, in credentialID) (*mcp.CallToolResult, secrets.Result, error) {
			if c == nil {
				return nil, secrets.Result{}, secrets.ErrState
			}
			p, _ := principal(ctx)
			out, err := c.Use(ctx, p, "mcp", in.ID)
			return nil, out, err
		})
}
