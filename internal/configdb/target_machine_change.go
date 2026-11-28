package configdb

import (
	"context"
	"errors"
	"fmt"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

// ErrTargetMachineScope reports that the caller cannot change the designation with this credential.
var ErrTargetMachineScope = errors.New("changing a response machine requires config:write, machines:write and access to the machine")

// TargetMachineChange sets or clears the machine for one target group in a reviewed configuration.
type TargetMachineChange struct {
	Machine   *targets.MachineBinding `json:"machine,omitempty" jsonschema:"the name and registration from list_machines; set this or clear"`
	Clear     bool                    `json:"clear,omitempty" jsonschema:"remove the designation instead of selecting a machine"`
	IfVersion int64                   `json:"if_version" jsonschema:"the version from get_config when you reviewed the target group"`
	Note      string                  `json:"note,omitempty" jsonschema:"the reason for this change, shown in the configuration history"`
}

// ChangeTargetMachine applies the same binding-only change for API and MCP credentials.
func (s *Service) ChangeTargetMachine(ctx context.Context, p store.Principal, via, set string, group int, in TargetMachineChange) (store.ConfigChange, error) {
	if !p.Has("config:write") || !p.Has("machines:write") {
		return store.ConfigChange{}, ErrTargetMachineScope
	}
	if (in.Machine != nil) == in.Clear || in.IfVersion <= 0 || group < 0 {
		return store.ConfigChange{}, fmt.Errorf("%w: choose a machine or clear, with the current configuration version and group index", ErrBadInput)
	}
	st := s.State()
	if err := expect(st, in.IfVersion); err != nil {
		return store.ConfigChange{}, err
	}
	set = normalKey(KindTargets, set)
	groups := st.Targets.Groups(set + ".yaml")
	if group >= len(groups) {
		return store.ConfigChange{}, ErrNotFound
	}
	for _, binding := range []*targets.MachineBinding{groups[group].Machine, in.Machine} {
		if binding != nil && !p.Reaches(binding.Name) {
			return store.ConfigChange{}, ErrTargetMachineScope
		}
	}
	return s.SetTargetMachineIf(ctx, Actor{Name: p.Name(), Via: via, Principal: &p}, set, group, in.Machine, in.Note, in.IfVersion)
}
