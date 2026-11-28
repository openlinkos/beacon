package configdb

import (
	"fmt"
	"slices"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

// A target group's response machine (targets.Group.Machine) says which machine answers for a service in
// its incidents. Whatever path changes it (the dedicated route, a put of the whole target set, a revert,
// a delete, an import), a caller held to a principal (Actor.Principal) needs what the dedicated route
// needs: machines:write, and SSH access to the machine designated before and to the one designated after,
// each at its current registration.

// touchedBindings lists, per target file, the machines whose designation changes from cur to next: set,
// cleared, moved to another group or another registration. Groups that move compare as changes, so it
// errs towards checking more.
func touchedBindings(cur, next *targets.Set) (before, after []targets.MachineBinding) {
	var files []string
	for _, s := range []*targets.Set{cur, next} {
		if s != nil {
			files = append(files, s.Files()...)
		}
	}
	slices.Sort(files)
	for _, f := range slices.Compact(files) {
		var a, b []targets.Group
		if cur != nil {
			a = cur.Groups(f)
		}
		if next != nil {
			b = next.Groups(f)
		}
		for i := range max(len(a), len(b)) {
			var was, is *targets.MachineBinding
			if i < len(a) {
				was = a[i].Machine
			}
			if i < len(b) {
				is = b[i].Machine
			}
			if was == nil && is == nil || was != nil && is != nil && *was == *is {
				continue
			}
			if was != nil {
				before = append(before, *was)
			}
			if is != nil {
				after = append(after, *is)
			}
		}
	}
	return before, after
}

// checkBindings refuses a change from cur to next that changes a response machine p may not change.
func (s *Service) checkBindings(p store.Principal, cur, next *targets.Set) error {
	before, after := touchedBindings(cur, next)
	if len(before) == 0 && len(after) == 0 {
		return nil
	}
	if !p.Has("config:write") || !p.Has("machines:write") {
		return ErrTargetMachineScope
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
