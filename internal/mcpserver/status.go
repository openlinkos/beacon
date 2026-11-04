package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/status"
	"github.com/openlinkos/beacon/internal/store"
)

// StatusInstructions follow Instructions when the server has the status pages.
const StatusInstructions = `Beacon serves public status pages (docs/guide/status-pages.md): get_status_page returns one as its public JSON
does, the state of each component now and over 90 days, its incidents and its maintenance windows. An
incident is an alert group on a page's services; its updates for the public are the notes you leave with
add_note that begin with "public:" (or every note, on a page with notes: all), so write those for people
outside, without machine names or anything internal. Planned work goes on the pages ahead of time with
schedule_maintenance, which also silences the services' alerts for the window; end_maintenance ends one
early or calls it off.`

// WithStatus adds the tool that reads the public status pages.
func WithStatus(st *status.Service) Option { return func(o *options) { o.status = st } }

type (
	statusIn struct {
		Name     string `json:"name" jsonschema:"the status page's name, as get_config with kind status_pages lists them"`
		Language string `json:"language,omitempty" jsonschema:"en or zh, when the page has it; the page's first language otherwise"`
	}
	maintenanceOut struct {
		Maintenance []store.Maintenance `json:"maintenance"`
	}
	scheduleIn struct {
		Services    []string `json:"services" jsonschema:"the services the work touches, as their probe targets' service label (a status page component's services)"`
		StartsAt    string   `json:"starts_at,omitempty" jsonschema:"when the work begins, RFC 3339 such as 2026-11-03T02:00:00Z, within 90 days; now when left out"`
		EndsAt      string   `json:"ends_at" jsonschema:"when it ends, RFC 3339; at most 7 days after it begins"`
		Title       string   `json:"title" jsonschema:"what is done, for the public status pages; also the silences' comment"`
		Description string   `json:"description,omitempty" jsonschema:"more for the public: what people will notice"`
	}
	maintenanceIDIn struct {
		ID string `json:"id" jsonschema:"the maintenance window's id, as list_maintenance gives it"`
	}
)

func parseTime(field, s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, fmt.Errorf("%s is a time in RFC 3339, such as 2026-11-03T02:00:00Z", field)
	}
	return t, nil
}

func addStatusTools(s *mcp.Server, b *bridge.Bridge, st *status.Service) {
	read := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: t(false)}

	if st != nil {
		mcp.AddTool(s, &mcp.Tool{Name: "get_status_page", Title: "Get a status page", Annotations: read,
			Description: "A public status page as its JSON shows it (GET /status/<name>.json): the overall state, each component's state now and its 90 days of history, the incidents (active, then the last 30 days) with their public updates, and the maintenance windows."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in statusIn) (*mcp.CallToolResult, *status.Page, error) {
				if _, err := need(ctx, "status:read"); err != nil {
					return nil, nil, err
				}
				p, err := st.Page(ctx, in.Name, in.Language)
				if err != nil {
					return nil, nil, fmt.Errorf("no status page %q; get_config with kind status_pages lists them", in.Name)
				}
				return nil, p, nil
			})
	}

	mcp.AddTool(s, &mcp.Tool{Name: "list_maintenance", Title: "List maintenance windows", Annotations: read,
		Description: "The maintenance windows planned through Beacon, the latest to start first, ended ones included for 90 days: services, when, title, who planned them, and their silences."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, maintenanceOut, error) {
			if _, err := need(ctx, "status:read"); err != nil {
				return nil, maintenanceOut{}, err
			}
			return nil, maintenanceOut{Maintenance: b.MaintenanceWindows()}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "schedule_maintenance", Title: "Plan a maintenance window",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(false), OpenWorldHint: t(false)},
		Description: "Plan work on some services: the public status pages show it ahead of time and while it lasts, and the services' alerts are silenced for the window (at most 7 days, starting within 90). It cannot start in the past."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in scheduleIn) (*mcp.CallToolResult, store.Maintenance, error) {
			who, err := need(ctx, "maintenance:write")
			if err != nil {
				return nil, store.Maintenance{}, err
			}
			start, err := parseTime("starts_at", in.StartsAt)
			if err != nil {
				return nil, store.Maintenance{}, err
			}
			end, err := parseTime("ends_at", in.EndsAt)
			if err != nil {
				return nil, store.Maintenance{}, err
			}
			m, err := b.ScheduleMaintenance(ctx, who, bridge.MaintenanceInput{Services: in.Services, StartsAt: start, EndsAt: end,
				Title: in.Title, Description: in.Description})
			return nil, m, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "end_maintenance", Title: "End a maintenance window",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(false), IdempotentHint: true, OpenWorldHint: t(false)},
		Description: "End a maintenance window now, or call it off before it begins: its silences end and the status pages say so."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in maintenanceIDIn) (*mcp.CallToolResult, store.Maintenance, error) {
			who, err := need(ctx, "maintenance:write")
			if err != nil {
				return nil, store.Maintenance{}, err
			}
			m, err := b.EndMaintenance(ctx, in.ID, who)
			return nil, m, err
		})
}
