package configdb

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

// A target group's response machine (targets.Group.Machine) says which machine answers for a service in
// its incidents: the service and the probes its group's labels and targets name. Whatever path changes
// that (the dedicated route, a put of the whole target set, a revert, a delete, an import), a caller held
// to a principal (Actor.Principal) needs what the dedicated route needs: config:write, machines:write
// and access to all machines, SSH access to the machine designated before and to the one designated
// after, and the latter at its current registration.

// boundGroup is a designation by what it answers for: the target file, the group's targets and labels,
// and the machine. Two groups that differ in any of them are different designations, wherever they are
// in their file.
func boundGroup(file string, g targets.Group) string {
	b, _ := json.Marshal(struct {
		File    string                  `json:"file"`
		Targets []string                `json:"targets"`
		Labels  map[string]string       `json:"labels"`
		Machine *targets.MachineBinding `json:"machine"`
	}{file, g.Targets, g.Labels, g.Machine})
	return string(b)
}

// touchedBindings lists the designations that change from cur to next: a machine designated for a group
// that was not, or no longer, or for a group whose service, labels or targets changed, or in another file.
// Moving a group within its file, unchanged, changes no designation.
func touchedBindings(cur, next *targets.Set) (before, after []targets.MachineBinding) {
	count := func(s *targets.Set) map[string]int {
		out := map[string]int{}
		if s == nil {
			return out
		}
		for _, f := range s.Files() {
			for _, g := range s.Groups(f) {
				if g.Machine != nil {
					out[boundGroup(f, g)]++
				}
			}
		}
		return out
	}
	was, is := count(cur), count(next)
	walk := func(s *targets.Set, have, other map[string]int) []targets.MachineBinding {
		var out []targets.MachineBinding
		if s == nil {
			return nil
		}
		left := map[string]int{}
		for k, n := range have {
			left[k] = n - other[k]
		}
		for _, f := range s.Files() {
			for _, g := range s.Groups(f) {
				if k := boundGroup(f, g); g.Machine != nil && left[k] > 0 {
					left[k]--
					out = append(out, *g.Machine)
				}
			}
		}
		return out
	}
	return walk(cur, was, is), walk(next, is, was)
}

// checkBindings refuses a change from cur to next that changes a response machine p may not change.
func (s *Service) checkBindings(p store.Principal, cur, next *targets.Set) error {
	before, after := touchedBindings(cur, next)
	if len(before) == 0 && len(after) == 0 {
		return nil
	}
	if err := bindingScope(p); err != nil {
		return err
	}
	for _, b := range append(slices.Clone(before), after...) {
		if !p.Reaches(b.Name) {
			return fmt.Errorf("%w: the change designates or clears machine %s", ErrTargetMachineScope, b.Name)
		}
	}
	for _, b := range after {
		if m, ok := s.d.Store.Machine(b.Name); !ok || m.Registration() != b.Registration {
			return fmt.Errorf("%w: %w (%s)", ErrConflict, store.ErrConfigMachineChanged, b.Name)
		}
	}
	return nil
}

// bindingScope is what every change of a response machine takes besides access to the machines: the
// scopes, and access to all machines, as every change of a machine does.
func bindingScope(p store.Principal) error {
	if !p.Has("config:write") || !p.Has("machines:write") {
		return ErrTargetMachineScope
	}
	if !p.IsAll() {
		return fmt.Errorf("%w: this credential reaches only the machines it names; changing a response machine takes access to all machines", ErrTargetMachineScope)
	}
	return nil
}
