package configdb

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

// ImportedHTTPGroup identifies a selectable target group within a particular configuration version.
type ImportedHTTPGroup struct {
	Set   string
	Index int
	Group targets.Group
}

// ImportedHTTPGroups lists a service's eligible HTTP target groups outside the routine editor.
func (st *State) ImportedHTTPGroups(service string) []ImportedHTTPGroup {
	if st == nil || st.problem != nil || service == "" {
		return nil
	}
	var out []ImportedHTTPGroup
	for _, file := range st.Targets.Files() {
		set := strings.TrimSuffix(file, ".yaml")
		if st.ConsoleTarget(set) {
			continue
		}
		for i, group := range st.Targets.Groups(file) {
			if group.Labels["service"] != service || !st.httpGroup(group) {
				continue
			}
			group.Targets, group.Labels = slices.Clone(group.Targets), maps.Clone(group.Labels)
			if group.Machine != nil {
				binding := *group.Machine
				group.Machine = &binding
			}
			out = append(out, ImportedHTTPGroup{Set: set, Index: i, Group: group})
		}
	}
	return out
}

func (st *State) httpGroup(group targets.Group) bool {
	if st.probeModules == nil || len(group.Targets) == 0 {
		return false
	}
	m := st.probeModules.Modules[group.Labels["module"]]
	if m == nil || m.Prober != "http" {
		return false
	}
	for _, target := range group.Targets {
		u, err := url.Parse(target)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return false
		}
	}
	return true
}

// SetTargetMachineIf changes only one versioned HTTP group's response-machine designation.
// A nil binding clears the designation. Neither operation changes target-set ownership.
func (s *Service) SetTargetMachineIf(ctx context.Context, who Actor, set string, group int, binding *targets.MachineBinding,
	note string, version int64,
) (store.ConfigChange, error) {
	if set == "" || group < 0 || version <= 0 || (binding != nil && (!store.MachineName.MatchString(binding.Name) || binding.Registration == "")) {
		return store.ConfigChange{}, ErrBadInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.state.Load()
	if err := expect(cur, version); err != nil {
		return store.ConfigChange{}, err
	}
	committed, err := s.d.Store.ConfigVersion()
	if err != nil {
		return store.ConfigChange{}, err
	}
	if committed != version {
		return store.ConfigChange{}, ErrConflict
	}
	groups := cur.Targets.Groups(set + ".yaml")
	if group >= len(groups) || !cur.httpGroup(groups[group]) {
		return store.ConfigChange{}, ErrNotFound
	}
	if binding != nil {
		m, ok := s.d.Store.MachineAtConfigVersion(binding.Name, version)
		if !ok || m.Registration() != binding.Registration {
			return store.ConfigChange{}, fmt.Errorf("%w: %w", ErrConflict, store.ErrConfigMachineChanged)
		}
	}
	groups = slices.Clone(groups)
	groups[group].Machine = binding
	raw, err := json.Marshal(groups)
	if err != nil {
		return store.ConfigChange{}, err
	}
	next := maps.Clone(cur.entries)
	next[entryKey(KindTargets, set)] = raw
	owned := map[string]bool{set: cur.ConsoleTarget(set)}
	ch, err := s.commitChecked(ctx, who, "put", note, 0, next, false, owned, version, nil, binding)
	if errors.Is(err, store.ErrConfigMachineChanged) {
		err = fmt.Errorf("%w: %w", ErrConflict, err)
	}
	return ch, err
}

// MonitorMachineForScope matches every current target group that could have contributed to an
// aggregated outage. A designation is usable only when every such group agrees on one enrolment.
func (s *Service) MonitorMachineForScope(scope targets.OutageScope, started time.Time) (store.MonitorMachine, bool) {
	var resolved store.MonitorMachine
	if scope.Service == "" || scope.Severity == "" || started.IsZero() || started.After(s.d.Now()) {
		return resolved, false
	}
	st := s.State()
	if st.problem != nil {
		return resolved, false
	}
	sets, modules := map[string]bool{}, map[string]bool{}
	var binding targets.MachineBinding
	for _, file := range st.Targets.Files() {
		for _, group := range st.Targets.Groups(file) {
			if !matchesOutageScope(scope, group.Labels) {
				continue
			}
			if !st.httpGroup(group) || group.Machine == nil || group.Machine.Name == "" || group.Machine.Registration == "" ||
				(binding != (targets.MachineBinding{}) && binding != *group.Machine) {
				return resolved, false
			}
			binding = *group.Machine
			sets[strings.TrimSuffix(file, ".yaml")] = true
			modules[group.Labels["module"]] = true
		}
	}
	if len(sets) == 0 || !s.monitorHistory(st, scope, sets, modules, binding, started) {
		return resolved, false
	}
	m, ok := s.d.Store.MachineAtConfigVersion(binding.Name, st.Version)
	if !ok || m.Registration() != binding.Registration || !m.Added.Before(started) {
		return resolved, false
	}
	return store.MonitorMachine{Service: scope.Service, Name: binding.Name, Registration: binding.Registration}, true
}

func matchesOutageScope(scope targets.OutageScope, labels map[string]string) bool {
	return labels["service"] == scope.Service && cmp.Or(labels["severity"], "critical") == scope.Severity &&
		(scope.FailureDomain == "" || labels["failure_domain"] == scope.FailureDomain) &&
		(scope.Page == "" || labels["page"] == scope.Page) &&
		(scope.Visibility == "" || labels["visibility"] == scope.Visibility)
}

// A removed contributor's samples may still be part of a stock outage's first evaluation.
// Cover the three-minute sample range, rule hold and evaluation delay with this conservative bound.
const monitorHistoryWindow = 5 * time.Minute

func (s *Service) monitorHistory(st *State, scope targets.OutageScope, pending, modules map[string]bool,
	binding targets.MachineBinding, started time.Time,
) bool {
	cutoff := started.Add(-monitorHistoryWindow)
	version, latest, proven := st.Version, time.Time{}, false
	s.d.Store.ConfigChanges(st.Version+1, 1, func(ch store.ConfigChange) bool {
		if ch.Version != version || ch.Time.IsZero() || (!latest.IsZero() && ch.Time.After(latest)) {
			return true // a gap or clock reversal leaves the historical scope uncertain
		}
		if ch.Time.Before(cutoff) && len(pending) == 0 {
			proven = true
			return true
		}
		version, latest = version-1, ch.Time
		for _, item := range ch.Items {
			switch item.Kind {
			case KindModule:
				if modules[item.Key] && (!ch.Time.Before(started) ||
					(!ch.Time.Before(cutoff) && len(item.Before) > 0 && !wasHTTPModule(item.Before))) {
					return true
				}
			case KindTargets:
				before, okBefore := outageGroups(item.Before, scope)
				after, okAfter := outageGroups(item.After, scope)
				if !okBefore || !okAfter {
					return true
				}
				if len(after) > 0 && ch.Time.Before(started) {
					delete(pending, item.Key)
				}
				if !ch.Time.Before(started) && !slices.EqualFunc(before, after, sameBoundGroup) {
					return true // the current designation cannot speak for the earlier alert
				}
				if !ch.Time.Before(cutoff) && slices.ContainsFunc(before, func(g targets.Group) bool {
					return (g.Machine == nil || *g.Machine != binding) &&
						!slices.ContainsFunc(after, func(keep targets.Group) bool { return sameProbeGroup(g, keep) })
				}) {
					return true // prior samples could belong to an unbound or differently bound contributor
				}
			}
		}
		proven = version == 0 && len(pending) == 0
		return proven
	})
	return proven
}

func outageGroups(raw json.RawMessage, scope targets.OutageScope) ([]targets.Group, bool) {
	var groups []targets.Group
	if len(raw) != 0 && json.Unmarshal(raw, &groups) != nil {
		return nil, false
	}
	return slices.DeleteFunc(groups, func(g targets.Group) bool { return !matchesOutageScope(scope, g.Labels) }), true
}

func sameProbeGroup(a, b targets.Group) bool {
	return slices.Equal(a.Targets, b.Targets) && maps.Equal(a.Labels, b.Labels)
}

func wasHTTPModule(raw json.RawMessage) bool {
	var old struct {
		Prober string `json:"prober"`
	}
	return json.Unmarshal(raw, &old) == nil && old.Prober == "http"
}

func sameBoundGroup(a, b targets.Group) bool {
	return sameProbeGroup(a, b) && ((a.Machine == nil && b.Machine == nil) ||
		(a.Machine != nil && b.Machine != nil && *a.Machine == *b.Machine))
}
