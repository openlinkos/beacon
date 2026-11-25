package configdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

var (
	ErrConsoleTargetExists  = errors.New("the destination target set exists")
	ErrConsoleServiceExists = errors.New("a target set already monitors this service")
)

// ConsoleTarget reports durable or historically verified console ownership of a target set.
func (st *State) ConsoleTarget(key string) bool { return st != nil && st.consoleTargets[key] }

// ConsoleTargetName gives a console monitor its own target set. The dot before the service name
// prevents its scope from matching a longer location id.
func ConsoleTargetName(scope, service string) string { return scope + "-http." + service + ".monitor" }

// legacyMonitorChange distinguishes console operations from imported files and API writes. Old
// editors allowed any target of a certain shape, so an edit alone cannot establish provenance.
func legacyMonitorChange(ch store.ConfigChange, item store.ConfigItem, notePrefix string) bool {
	_, suffix, found := strings.Cut(item.Key, "-http.")
	service, named := strings.CutSuffix(suffix, ".monitor")
	return found && named && service != "" && item.Kind == KindTargets && !item.ConsoleTracked &&
		ch.Action == "put" && ch.Via == "console" && ch.Note == notePrefix+service
}

func (s *Service) loadConsoleTargets(st *State, tracked map[string]bool) {
	st.consoleTargets = tracked
	pending := map[string][]byte{}
	for _, file := range st.Targets.Files() {
		key := strings.TrimSuffix(file, ".yaml")
		if !strings.HasSuffix(key, ".monitor") || !strings.Contains(key, "-http.") {
			continue
		}
		if !tracked[key] {
			pending[key] = st.entries[entryKey(KindTargets, key)]
		}
	}
	if len(pending) == 0 {
		return
	}
	// Recover target sets from before ownership was stored. Work backwards across consecutive
	// console edits to a witnessed add; if the trail is missing or leads to an import, leave it out.
	// ConfigChanges runs the predicate newest first and stops at its first match here.
	s.d.Store.ConfigChanges(st.Version+1, 1, func(ch store.ConfigChange) bool {
		for _, it := range ch.Items {
			want, exists := pending[it.Key]
			if it.Kind != KindTargets || !exists {
				continue
			}
			switch {
			case !bytes.Equal(want, it.After):
				delete(pending, it.Key)
			case it.ConsoleAfter || (it.Before == nil && legacyMonitorChange(ch, it, "Add HTTP monitor ")):
				tracked[it.Key] = true
				delete(pending, it.Key)
			case it.Before != nil && legacyMonitorChange(ch, it, "Edit HTTP monitor "):
				pending[it.Key] = it.Before
			default:
				delete(pending, it.Key)
			}
		}
		return len(pending) == 0
	})
}

// RoutineMonitorGroup reports whether the group has the console's routine HTTP monitor shape.
func RoutineMonitorGroup(g targets.Group) bool {
	if len(g.Targets) != 1 || len(g.Labels) != 4 || g.Labels["service"] == "" ||
		g.Labels["module"] == "" || g.Labels["severity"] == "" || g.Labels["visibility"] != "internal" {
		return false
	}
	return RoutineMonitorURL(g.Targets[0])
}

// RoutineMonitorURL accepts an HTTP endpoint without credentials or URL parts the form may not display.
func RoutineMonitorURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.User == nil && u.RawQuery == "" && !u.ForceQuery &&
		(u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" &&
		len(raw) <= 2048 && !strings.Contains(raw, "#")
}

// SetConsoleTargetIf creates a console-managed monitor or updates one. Moving its probe scope
// writes the old and new target sets as one version, so probes never see a duplicated or absent
// target in between. The monitor keeps its service name.
func (s *Service) SetConsoleTargetIf(ctx context.Context, who Actor, previous, scope string, group targets.Group,
	note string, version int64,
) (store.ConfigChange, error) {
	service := group.Labels["service"]
	name := ConsoleTargetName(scope, service)
	if scope == "" || version <= 0 || !RoutineMonitorGroup(group) {
		return store.ConfigChange{}, ErrBadInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.state.Load()
	if err := expect(cur, version); err != nil {
		return store.ConfigChange{}, err
	}
	if !slices.ContainsFunc(cur.Catalog.Locations, func(l catalog.Location) bool {
		return scope == "all" || scope == l.Region || scope == l.ID
	}) {
		return store.ConfigChange{}, ErrBadInput
	}
	if previous != "" {
		if !cur.ConsoleTarget(previous) {
			return store.ConfigChange{}, ErrNotFound
		}
		gs := cur.Targets.Groups(previous + ".yaml")
		if len(gs) != 1 || gs[0].Labels["service"] != service {
			return store.ConfigChange{}, ErrBadInput
		}
	}
	if name != previous && cur.entries[entryKey(KindTargets, name)] != nil {
		return store.ConfigChange{}, fmt.Errorf("%w: %w", ErrConflict, ErrConsoleTargetExists)
	}
	for _, file := range cur.Targets.Files() {
		if file == previous+".yaml" {
			continue
		}
		for _, existing := range cur.Targets.Groups(file) {
			if existing.Labels["service"] == service {
				return store.ConfigChange{}, fmt.Errorf("%w: %w", ErrConflict, ErrConsoleServiceExists)
			}
		}
	}
	raw, _ := json.Marshal([]targets.Group{group}) // groups contain only strings and slices
	next := maps.Clone(cur.entries)
	if previous != "" && previous != name {
		delete(next, entryKey(KindTargets, previous))
	}
	if err := set(next, KindTargets, name, raw); err != nil {
		return store.ConfigChange{}, err
	}
	return s.commitWithConsoleTargets(ctx, who, "put", note, 0, next, false, map[string]bool{name: true}, version, nil)
}

// DeleteConsoleTargetIf removes a target set still owned by the console at the expected version.
func (s *Service) DeleteConsoleTargetIf(ctx context.Context, who Actor, name, note string, version int64) (store.ConfigChange, error) {
	if version <= 0 {
		return store.ConfigChange{}, ErrBadInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.state.Load()
	if err := expect(cur, version); err != nil {
		return store.ConfigChange{}, err
	}
	if !cur.ConsoleTarget(name) {
		return store.ConfigChange{}, ErrNotFound
	}
	next := maps.Clone(cur.entries)
	delete(next, entryKey(KindTargets, name))
	return s.commitWithConsoleTargets(ctx, who, "delete", note, 0, next, false, nil, version, nil)
}
