package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/store"
)

// ConfigInstructions follow Instructions when the server has the configuration tools.
const ConfigInstructions = `Beacon also holds what it monitors: the catalogue (probe locations, heartbeats, expiring resources),
the probe target sets and the probe modules, in the shapes of their files (docs/catalog.md). get_config
shows them with the version in force. With the config:write scope, put_config and delete_config change
one item at a time; the whole configuration is checked as the files always were, and the gateway hands
probes the result at once, so a mistake reaches every probe within a minute: change only what you were
asked to, and say why in the note. Every change is a version with who made it and the values before and
after: list_config_changes and get_config_change show them, and revert_config_change undoes one.`

// Option changes what the server offers.
type Option func(*options)

type options struct {
	config *configdb.Service
}

// WithConfig adds the tools that read and change the configuration Beacon holds.
func WithConfig(c *configdb.Service) Option { return func(o *options) { o.config = c } }

// object is the output schema of the configuration tools: their results hold items in the shapes of
// their files, which a schema inferred from the Go types would not describe.
var object = map[string]any{"type": "object"}

type (
	configIn struct {
		Kind string `json:"kind,omitempty" jsonschema:"locations, heartbeats, resources, targets or modules; everything when empty"`
		Key  string `json:"key,omitempty" jsonschema:"with kind, one item: a location id, a heartbeat or resource name, a target set's name (all-web for the file all-web.yaml) or a module name"`
	}
	putConfigIn struct {
		Kind  string `json:"kind" jsonschema:"locations, heartbeats, resources, targets or modules"`
		Key   string `json:"key" jsonschema:"the item's id or name; a target set's name starts with all-, overseas-, mainland- or a location id and a dash, which says who probes it"`
		Value any    `json:"value" jsonschema:"the item in the shape of its file: a location {name, region, network, ...}; a heartbeat {max_age, severity, description}; a resource {kind, expires, note}; a target set, a list of {targets: [...], labels: {module, service, ...}}; a module {prober, timeout, http: {...}}"`
		Note  string `json:"note,omitempty" jsonschema:"why; kept with the change and shown in the alert chat"`
	}
	deleteConfigIn struct {
		Kind string `json:"kind" jsonschema:"locations, heartbeats, resources, targets or modules"`
		Key  string `json:"key" jsonschema:"the item's id or name"`
		Note string `json:"note,omitempty" jsonschema:"why"`
	}
	configChangesIn struct {
		Kind   string `json:"kind,omitempty" jsonschema:"only changes to this kind"`
		Key    string `json:"key,omitempty" jsonschema:"with kind, only changes to this item"`
		Before int64  `json:"before,omitempty" jsonschema:"only changes older than this version, for the next page"`
		Limit  int    `json:"limit,omitempty" jsonschema:"how many, newest first; default 20, at most 200"`
	}
	configChangesOut struct {
		Changes []store.ConfigChange `json:"changes"`
	}
	versionIn struct {
		Version int64 `json:"version" jsonschema:"the change's version, as list_config_changes gives it"`
	}
	revertIn struct {
		Version int64  `json:"version" jsonschema:"the version of the change to undo"`
		Note    string `json:"note,omitempty" jsonschema:"why"`
	}
)

func configActor(ctx context.Context) (configdb.Actor, error) {
	who, err := need(ctx, "config:write")
	return configdb.Actor{Name: who.Name, Via: who.Via}, err
}

func addConfigTools(s *mcp.Server, c *configdb.Service) {
	read := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: t(false)}

	mcp.AddTool(s, &mcp.Tool{Name: "get_config", Title: "Get the configuration", Annotations: read, OutputSchema: object,
		Description: "What Beacon monitors, in the shapes of its files: probe locations, heartbeats, expiring resources, probe target sets (with who probes each) and probe modules, with the version in force. kind narrows it to one list, kind and key to one item."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in configIn) (*mcp.CallToolResult, configdb.View, error) {
			if _, err := need(ctx, "status:read", "config:write"); err != nil {
				return nil, configdb.View{}, err
			}
			v, err := c.Get(in.Kind, in.Key)
			return nil, v, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "put_config", Title: "Add or replace a configuration item", OutputSchema: object,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(true), IdempotentHint: true, OpenWorldHint: t(false)},
		Description: "Add or replace one location, heartbeat, resource, target set or module. The whole configuration is checked as its files always were and the change takes effect at once; the result is the change, with its version and the values before and after."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in putConfigIn) (*mcp.CallToolResult, store.ConfigChange, error) {
			who, err := configActor(ctx)
			if err != nil {
				return nil, store.ConfigChange{}, err
			}
			raw, err := json.Marshal(in.Value)
			if err != nil {
				return nil, store.ConfigChange{}, fmt.Errorf("value: %w", err)
			}
			ch, err := c.Put(ctx, who, in.Kind, in.Key, raw, in.Note)
			return nil, ch, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "delete_config", Title: "Remove a configuration item", OutputSchema: object,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(true), IdempotentHint: true, OpenWorldHint: t(false)},
		Description: "Remove one location, heartbeat, resource, target set or module. A location cannot go while a target set is named after it alone."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in deleteConfigIn) (*mcp.CallToolResult, store.ConfigChange, error) {
			who, err := configActor(ctx)
			if err != nil {
				return nil, store.ConfigChange{}, err
			}
			ch, err := c.Delete(ctx, who, in.Kind, in.Key, in.Note)
			return nil, ch, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_config_changes", Title: "List configuration changes", Annotations: read, OutputSchema: object,
		Description: "Changes to the configuration, newest first: version, when, who, through what, why, and which items they added, changed or removed. get_config_change has the values."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in configChangesIn) (*mcp.CallToolResult, configChangesOut, error) {
			if _, err := need(ctx, "status:read", "config:write"); err != nil {
				return nil, configChangesOut{}, err
			}
			if in.Limit <= 0 {
				in.Limit = 20
			}
			list, err := c.History(configdb.HistoryFilter{Kind: in.Kind, Key: in.Key, Before: in.Before, Limit: min(in.Limit, 200)})
			return nil, configChangesOut{Changes: list}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_config_change", Title: "Get a configuration change", Annotations: read, OutputSchema: object,
		Description: "One change to the configuration, with every item's value before and after."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in versionIn) (*mcp.CallToolResult, store.ConfigChange, error) {
			if _, err := need(ctx, "status:read", "config:write"); err != nil {
				return nil, store.ConfigChange{}, err
			}
			ch, err := c.Change(in.Version)
			return nil, ch, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "revert_config_change", Title: "Revert a configuration change", OutputSchema: object,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(true), OpenWorldHint: t(false)},
		Description: "Undo one change, as a new change: every item it touched gets its value from before. Refused when a later change touched one of them; revert that first."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in revertIn) (*mcp.CallToolResult, store.ConfigChange, error) {
			who, err := configActor(ctx)
			if err != nil {
				return nil, store.ConfigChange{}, err
			}
			ch, err := c.Revert(ctx, who, in.Version, in.Note)
			return nil, ch, err
		})
}
