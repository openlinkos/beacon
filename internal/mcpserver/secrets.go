package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/store"
)

// The vault over MCP: the same operations the REST routes and the console offer, through the same
// service. People work the vault; an agent is refused with a plain answer — agents reach items
// through the grants and entitlements a later package brings, and until then the vault answers an
// agent with nothing (the default-deny the design starts from). The tools are here so the surface
// is one: whatever a credential resolves to, the same door.

// SecretsInstructions follow Instructions when the vault is there.
const SecretsInstructions = `The vault is here: collections of items whose values Beacon keeps encrypted, for
people to manage and, later, for agents to reach through grants they ask for. Until the grants arrive,
every vault tool answers an agent with "agents reach items through grants" — nothing about the vault is
visible to an agent yet. People use the console or the REST API with the same operations.`

// WithVault adds the vault's tools.
func WithVault(v *secrets.Service) Option { return func(o *options) { o.vault = v } }

// vaultActor maps the caller to the vault's actor; an agent is refused.
func vaultActor(ctx context.Context) (secrets.Actor, error) {
	p, ok := principal(ctx)
	if !ok {
		return secrets.Actor{}, errors.New("no credential")
	}
	if !p.Person() {
		return secrets.Actor{}, errors.New(api.ScopeProblem(p, ""))
	}
	if p.Agent != "" {
		return secrets.Actor{}, errors.New("agents reach items through grants; until those arrive, the vault answers an agent with nothing")
	}
	return secrets.Actor{Name: p.User, Via: "mcp", Role: roleOf(p)}, nil
}

func roleOf(p store.Principal) string {
	switch {
	case p.Has("accounts:write"):
		return "owner"
	case p.Has("machines:write"):
		return "operator"
	case p.Has("alerts:write"):
		return "oncall"
	case p.Has("status:read"):
		return "viewer"
	}
	return ""
}

// registerSecretsTools adds the vault's tools to the server.
func registerSecretsTools(s *mcp.Server, v *secrets.Service) {
	read := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: t(false)}

	mcp.AddTool(s, &mcp.Tool{Name: "vault_state", Title: "Vault state", Annotations: read,
		Description: "Whether the vault is sealed or open, its unseal mode, and the policy rows that failed their signature (served as absent and reported). An agent is refused: agents reach items through grants, which arrive with a later package."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, vaultStateOut, error) {
			if _, err := vaultActor(ctx); err != nil {
				return nil, vaultStateOut{}, err
			}
			bad := v.Bad()
			if bad == nil {
				bad = []string{}
			}
			return nil, vaultStateOut{Sealed: v.Sealed(), Mode: v.SealModeName(), BadRows: bad}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_collections", Title: "Collections", Annotations: read,
		Description: "The vault's collections, with their default approval level. People only; an agent is refused (grants come with a later package)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, collectionsOut, error) {
			who, err := vaultActor(ctx)
			if err != nil {
				return nil, collectionsOut{}, err
			}
			cols := v.CollectionsFor(who)
			if cols == nil {
				cols = []secrets.Collection{}
			}
			return nil, collectionsOut{Collections: cols}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_items", Title: "Items", Annotations: read,
		Description: "One collection's items — overviews only (titles, categories, tags, URLs, field names), never a value. The caller must hold describe over the collection. An agent is refused."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in itemsIn) (*mcp.CallToolResult, itemsOut, error) {
			who, err := vaultActor(ctx)
			if err != nil {
				return nil, itemsOut{}, err
			}
			col, ok := v.Collection(in.Collection)
			if !ok {
				return nil, itemsOut{}, fmt.Errorf("no collection %s", in.Collection)
			}
			if !v.May(who, col.ID, secrets.PermDescribe) {
				return nil, itemsOut{}, fmt.Errorf("no collection %s", in.Collection)
			}
			items := v.Items(col.Name, in.State)
			if items == nil {
				items = []secrets.Item{}
			}
			return nil, itemsOut{Items: items}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_item", Title: "Item", Annotations: read,
		Description: "One item by its ID or collection/title: the overview, the versions with their fields (names, types, purposes — never values), who can get it, and the item's beacon:// reference. The caller must hold describe. An agent is refused."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in refIn) (*mcp.CallToolResult, itemOut, error) {
			who, err := vaultActor(ctx)
			if err != nil {
				return nil, itemOut{}, err
			}
			item, col, ok := v.Item(in.Ref)
			if !ok || !v.May(who, col.ID, secrets.PermDescribe) {
				return nil, itemOut{}, fmt.Errorf("no item %s", in.Ref)
			}
			versions := v.Versions(item.ID)
			for i := range versions {
				versions[i].CT = nil
			}
			if versions == nil {
				versions = []secrets.ItemVersion{}
			}
			return nil, itemOut{Item: item, Collection: col, Level: v.EffectiveLevel(item), Versions: versions,
				WhoCanGet: v.WhoCanGet(col.ID), Ref: secrets.RefOfItem(col.Name, item.Title)}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "put_item", Title: "Put an item",
		Description: "Make an item or write its next version. The caller must hold edit; changing the tags or the level needs manage. Values are given as fields with values; what is not named keeps its value. Every change is audited and echoed to the chat — what changed, by whom and why, never a value. An agent is refused."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in putItemIn) (*mcp.CallToolResult, secrets.Item, error) {
			who, err := vaultActor(ctx)
			if err != nil {
				return nil, secrets.Item{}, err
			}
			set := in.itemIn()
			if in.Ref != "" {
				item, col, ok := v.Item(in.Ref)
				if !ok {
					return nil, secrets.Item{}, fmt.Errorf("no item %s", in.Ref)
				}
				set.ID, set.Collection = item.ID, col.Name
			}
			item, err := v.SetItem(who, set)
			if err != nil {
				return nil, secrets.Item{}, err
			}
			return nil, item, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "reveal_item", Title: "Reveal an item",
		Description: "View an item's values. A person holding read, with the account password re-entered within the window the settings give; the view is recorded before the values are returned. An agent is refused: agents take values through grants, which arrive with a later package."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in revealIn) (*mcp.CallToolResult, revealOut, error) {
			who, err := vaultActor(ctx)
			if err != nil {
				return nil, revealOut{}, err
			}
			values, fields, err := v.Reveal(who, in.Ref, in.Fields, in.Password)
			if err != nil {
				return nil, revealOut{}, err
			}
			if fields == nil {
				fields = []secrets.Field{}
			}
			return nil, revealOut{Values: values, Fields: fields}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "search_secrets", Title: "Search the vault", Annotations: read,
		Description: "Items the caller may describe, by a word in the title, category, URLs or tags, narrowed by tags, inside one collection when it names one. Answers references and overviews, never values. An agent is refused until grants arrive."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
			who, err := vaultActor(ctx)
			if err != nil {
				return nil, searchOut{}, err
			}
			out := v.Search(who, in.Query, in.Collection, in.Tags)
			if out == nil {
				out = []secrets.SearchResult{}
			}
			return nil, searchOut{Results: out}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "generate_item_value", Title: "Generate a value",
		Description: "Make a new value for a field on the server (length and charset by the spec), stored as the item's next version. The answer says the version, never the value — a secret can be turned without anyone seeing it. The caller must hold edit; an agent is refused until grants arrive."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in generateIn) (*mcp.CallToolResult, secrets.Item, error) {
			who, err := vaultActor(ctx)
			if err != nil {
				return nil, secrets.Item{}, err
			}
			item, err := v.Generate(who, in.Ref, in.Section, in.Field, in.Reason, secrets.GenSpec{Length: in.Length, Charset: in.Charset})
			if err != nil {
				return nil, secrets.Item{}, err
			}
			return nil, item, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "archive_item", Title: "Archive an item",
		Description: "Move an item out of the search; its versions stay. The caller must hold edit. An agent is refused."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in refIn) (*mcp.CallToolResult, secrets.Item, error) {
			who, err := vaultActor(ctx)
			if err != nil {
				return nil, secrets.Item{}, err
			}
			item, err := v.ArchiveItem(who, in.Ref)
			if err != nil {
				return nil, secrets.Item{}, err
			}
			return nil, item, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "remove_item", Title: "Remove an item",
		Description: "Move an item to the trash, where it stays for the retention and can be restored. The caller must hold edit. An agent is refused."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in refIn) (*mcp.CallToolResult, secrets.Item, error) {
			who, err := vaultActor(ctx)
			if err != nil {
				return nil, secrets.Item{}, err
			}
			item, err := v.TrashItem(who, in.Ref)
			if err != nil {
				return nil, secrets.Item{}, err
			}
			return nil, item, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "restore_item", Title: "Restore an item",
		Description: "Bring an item back from the archive or the trash. The caller must hold edit. An agent is refused."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in refIn) (*mcp.CallToolResult, secrets.Item, error) {
			who, err := vaultActor(ctx)
			if err != nil {
				return nil, secrets.Item{}, err
			}
			item, err := v.RestoreItem(who, in.Ref)
			if err != nil {
				return nil, secrets.Item{}, err
			}
			return nil, item, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "rollback_item", Title: "Roll an item back",
		Description: "Make an earlier version's details the item's next version; nothing is lost and the history shows what happened. The caller must hold edit. An agent is refused."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in rollbackIn) (*mcp.CallToolResult, secrets.Item, error) {
			who, err := vaultActor(ctx)
			if err != nil {
				return nil, secrets.Item{}, err
			}
			item, err := v.RollbackItem(who, in.Ref, in.Version, in.Reason)
			if err != nil {
				return nil, secrets.Item{}, err
			}
			return nil, item, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_tags", Title: "Tags", Annotations: read,
		Description: "The vault's tags with the approval levels they carry. An agent is refused."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, tagsOut, error) {
			if _, err := vaultActor(ctx); err != nil {
				return nil, tagsOut{}, err
			}
			tags := v.Tags()
			if tags == nil {
				tags = []secrets.Tag{}
			}
			return nil, tagsOut{Tags: tags}, nil
		})
}

// The tools' inputs and outputs.
type (
	vaultStateOut struct {
		Sealed  bool     `json:"sealed"`
		Mode    string   `json:"mode"`
		BadRows []string `json:"rows_failed_signature"`
	}
	collectionsOut struct {
		Collections []secrets.Collection `json:"collections"`
	}
	itemsIn struct {
		Collection string `json:"collection" jsonschema:"the collection's name or ID"`
		State      string `json:"state,omitempty" jsonschema:"normal (default), archived or trash"`
	}
	itemsOut struct {
		Items []secrets.Item `json:"items"`
	}
	refIn struct {
		Ref string `json:"ref" jsonschema:"the item's ID or collection/title"`
	}
	itemOut struct {
		Item       secrets.Item          `json:"item"`
		Collection secrets.Collection    `json:"collection"`
		Level      string                `json:"level"`
		Versions   []secrets.ItemVersion `json:"versions"`
		WhoCanGet  []string              `json:"who_can_get"`
		Ref        string                `json:"ref"`
	}
	putItemIn struct {
		Collection string            `json:"collection" jsonschema:"the collection's name or ID, for a new item"`
		Ref        string            `json:"ref,omitempty" jsonschema:"the item's ID or collection/title, to change one"`
		Title      string            `json:"title,omitempty"`
		Category   string            `json:"category,omitempty"`
		URLs       []string          `json:"urls,omitempty"`
		Tags       []string          `json:"tags,omitempty"`
		Level      string            `json:"level,omitempty" jsonschema:"auto, review, person or never; - clears the item's own override"`
		Fields     []secrets.FieldIn `json:"fields,omitempty"`
		Notes      string            `json:"notes,omitempty"`
		HasNotes   bool              `json:"has_notes,omitempty"`
		Reason     string            `json:"reason,omitempty"`
	}
	revealIn struct {
		Ref      string   `json:"ref" jsonschema:"the item's ID or collection/title"`
		Fields   []string `json:"fields,omitempty" jsonschema:"the fields to view; empty is all of them"`
		Password string   `json:"password,omitempty" jsonschema:"the account password, when the window has passed"`
	}
	revealOut struct {
		Values map[string]string `json:"values"`
		Fields []secrets.Field   `json:"fields"`
	}
	searchIn struct {
		Query      string   `json:"query,omitempty"`
		Collection string   `json:"collection,omitempty"`
		Tags       []string `json:"tags,omitempty"`
	}
	searchOut struct {
		Results []secrets.SearchResult `json:"results"`
	}
	generateIn struct {
		Ref     string `json:"ref" jsonschema:"the item's ID or collection/title"`
		Section string `json:"section,omitempty"`
		Field   string `json:"field" jsonschema:"the field the new value is for"`
		Length  int    `json:"length,omitempty" jsonschema:"how long the value is (24 by default)"`
		Charset string `json:"charset,omitempty" jsonschema:"letters (default), alnum or all"`
		Reason  string `json:"reason,omitempty"`
	}
	rollbackIn struct {
		Ref     string `json:"ref" jsonschema:"the item's ID or collection/title"`
		Version int64  `json:"version" jsonschema:"the version to go back to"`
		Reason  string `json:"reason,omitempty"`
	}
	tagsOut struct {
		Tags []secrets.Tag `json:"tags"`
	}
)

// itemIn turns the tool's input into the service's.
func (in putItemIn) itemIn() secrets.ItemIn {
	return secrets.ItemIn{Collection: in.Collection, Title: in.Title, Category: in.Category, URLs: in.URLs,
		Tags: in.Tags, Level: in.Level, Fields: in.Fields, Notes: in.Notes, HasNotes: in.HasNotes, Reason: in.Reason}
}
