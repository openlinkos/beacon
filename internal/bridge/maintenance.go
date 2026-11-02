package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openlinkos/beacon/internal/store"
)

// Maintenance windows: planned work on some services, announced on the status pages ahead of time and
// shown there while it lasts. A window is a silence per service, from its start to its end, with the
// comment "maintenance: <title>", so that what the work breaks pages nobody; and a record of its own,
// kept as long as the pages' history (silences are forgotten a week after they end). A silence made
// some other way whose comment begins with "maintenance:" is shown on the pages too, while it lasts.

// MaintenancePrefix begins the comment of every silence that is a maintenance window.
const MaintenancePrefix = "maintenance:"

const (
	maxMaintenance      = 7 * 24 * time.Hour  // the longest window
	maxMaintenanceAhead = 90 * 24 * time.Hour // how far ahead a window may be planned
	maxMaintenanceSvcs  = 50
)

// ErrNoMaintenance means there is no maintenance window with that id; ErrMaintenanceEnded that it has
// ended, or was called off, already.
var (
	ErrNoMaintenance    = errors.New("no such maintenance window")
	ErrMaintenanceEnded = errors.New("the maintenance window has ended")
)

// MaintenanceInput is a maintenance window to plan. A start in the past, or none, is now: a window
// cannot cover what already happened.
type MaintenanceInput struct {
	Services    []string  `json:"services"`
	StartsAt    time.Time `json:"starts_at,omitzero"`
	EndsAt      time.Time `json:"ends_at"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
}

// ScheduleMaintenance plans a maintenance window: its silences, its record, an audit entry and an echo
// to the alert chat.
func (b *Bridge) ScheduleMaintenance(ctx context.Context, who Actor, in MaintenanceInput) (store.Maintenance, error) {
	now := b.now()
	title, desc := cleanLine(strings.TrimSpace(in.Title)), clean(strings.TrimSpace(in.Description), maxLong, true)
	start, end := in.StartsAt, in.EndsAt
	if start.Before(now) {
		start = now
	}
	var svcs []string
	for _, s := range in.Services {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(svcs, s) {
			svcs = append(svcs, s)
		}
	}
	slices.Sort(svcs)
	switch {
	case title == "":
		return store.Maintenance{}, fmt.Errorf("%w: give the window a title: what is done, for the status pages", ErrBadInput)
	case utf8.RuneCountInString(title) > 200:
		return store.Maintenance{}, fmt.Errorf("%w: the title is at most 200 characters", ErrBadInput)
	case len(svcs) == 0 || len(svcs) > maxMaintenanceSvcs:
		return store.Maintenance{}, fmt.Errorf("%w: name the services the work touches (1 to %d, as their probe targets' service label)", ErrBadInput, maxMaintenanceSvcs)
	case slices.ContainsFunc(svcs, func(s string) bool { return !safeValue(s) }):
		return store.Maintenance{}, fmt.Errorf("%w: a service is a service label's value, without quotes, backslashes or control characters", ErrBadInput)
	case end.IsZero() || !end.After(start):
		return store.Maintenance{}, fmt.Errorf("%w: ends_at must be after starts_at and in the future", ErrBadInput)
	case end.Sub(start) > maxMaintenance:
		return store.Maintenance{}, fmt.Errorf("%w: a window lasts at most 7 days", ErrBadInput)
	case start.Sub(now) > maxMaintenanceAhead:
		return store.Maintenance{}, fmt.Errorf("%w: a window starts within 90 days", ErrBadInput)
	}
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return store.Maintenance{}, err
	}
	m := store.Maintenance{ID: "m-" + hex.EncodeToString(raw), Title: title, Description: desc, Services: svcs, StartsAt: start, EndsAt: end,
		CreatedBy: who.Name, Via: who.Via, Created: now}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range svcs {
		id, err := b.d.AM.CreateSilence(ctx, map[string]string{"service": s}, start, end, who.Name, MaintenancePrefix+" "+title)
		if err != nil {
			for _, made := range m.Silences {
				_ = b.d.AM.ExpireSilence(ctx, made)
			}
			return store.Maintenance{}, err
		}
		m.Silences = append(m.Silences, id)
	}
	if err := b.d.Store.PutMaintenance(m, now.Add(-store.KeepStatus)); err != nil {
		return store.Maintenance{}, err
	}
	what := strings.Join(svcs, ", ")
	_ = b.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "maintenance", Target: m.ID,
		Detail: fmt.Sprintf("%s from %s to %s: %s", what, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), title)})
	b.echo(ctx, b.t("%s planned maintenance \"%s\" of %s from %s to %s via %s", who.Name, title, what, b.when(start), b.when(end), b.viaName(who.Via)))
	return m, nil
}

// EndMaintenance ends a maintenance window now, or calls it off before it began: its silences end, and
// the pages say so.
func (b *Bridge) EndMaintenance(ctx context.Context, id string, who Actor) (store.Maintenance, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, ok := b.d.Store.MaintenanceWindow(id)
	if !ok {
		return store.Maintenance{}, ErrNoMaintenance
	}
	now := b.now()
	if m.Cancelled || !m.End().After(now) {
		return m, fmt.Errorf("%w: maintenance window %s ended already", ErrMaintenanceEnded, id)
	}
	for _, s := range m.Silences {
		_ = b.d.AM.ExpireSilence(ctx, s) // one ended some other way stays ended
	}
	m.Ended = now
	m.Cancelled = now.Before(m.StartsAt)
	if err := b.d.Store.PutMaintenance(m, now.Add(-store.KeepStatus)); err != nil {
		return store.Maintenance{}, err
	}
	_ = b.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "end_maintenance", Target: m.ID, Detail: m.Title})
	if m.Cancelled {
		b.echo(ctx, b.t("%s called off maintenance \"%s\" via %s", who.Name, m.Title, b.viaName(who.Via)))
	} else {
		b.echo(ctx, b.t("%s ended maintenance \"%s\" via %s", who.Name, m.Title, b.viaName(who.Via)))
	}
	return m, nil
}

// MaintenanceWindows lists the maintenance windows kept, the latest to start first.
func (b *Bridge) MaintenanceWindows() []store.Maintenance {
	out := b.d.Store.MaintenanceWindows()
	if out == nil {
		out = []store.Maintenance{}
	}
	return out
}

// when is a time as the chat reads it, where the configuration says Beacon is.
func (b *Bridge) when(t time.Time) string {
	loc := b.d.Config.Location
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04 MST")
}
