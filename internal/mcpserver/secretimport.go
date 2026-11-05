package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/secrets/importer"
)

// The import's tool: the same operation the REST route, the console's import wizard and
// `beacon secret import` offer, through the same service. A person imports (an agent is refused
// with the vault's plain answer); a dry-run writes nothing, and no report ever carries a value.

// registerSecretImportTool adds the import's tool.
func registerSecretImportTool(s *mcp.Server, v *secrets.Service) {
	mcp.AddTool(s, &mcp.Tool{Name: "import_secrets", Title: "Import a batch of secrets",
		Description: "Import a parsed batch of secrets into the vault (what `beacon secret import` and the console's import wizard send). " +
			"The batch holds collections of items with their fields and values; dry_run answers the report and writes nothing. " +
			"The report counts items, fields, attachments and one-time-passcode fields per collection, names the categories and the levels " +
			"they start at, and names the conflicts — never a value. Conflicts resolve by on_conflict (ask holds them back, new_version, " +
			"skip, keep_both). A person only; an agent is refused."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in importIn) (*mcp.CallToolResult, secrets.ImportReport, error) {
			who, err := vaultActor(ctx)
			if err != nil {
				return nil, secrets.ImportReport{}, err
			}
			if in.Batch.Source == "" || len(in.Batch.Collections) == 0 {
				return nil, secrets.ImportReport{}, fmt.Errorf("a batch is a source with collections of items")
			}
			report, err := v.Import(who, in.Batch, secrets.ImportOptions{
				DryRun: in.DryRun, OnConflict: in.OnConflict, Levels: in.Levels, Collection: in.Collection, Reason: in.Reason,
			})
			if err != nil {
				return nil, secrets.ImportReport{}, err
			}
			return nil, report, nil
		})
}

// importIn is the tool's input: the parsed batch and how it runs.
type importIn struct {
	Batch      importer.Batch    `json:"batch" jsonschema:"the parsed batch: a source with collections of items"`
	DryRun     bool              `json:"dry_run,omitempty" jsonschema:"answer the report and write nothing"`
	OnConflict string            `json:"on_conflict,omitempty" jsonschema:"ask (the default), new_version, skip or keep_both"`
	Levels     map[string]string `json:"levels,omitempty" jsonschema:"a level to start a collection at, by collection name"`
	Collection string            `json:"collection,omitempty" jsonschema:"one target collection for the whole batch"`
	Reason     string            `json:"reason,omitempty"`
}
