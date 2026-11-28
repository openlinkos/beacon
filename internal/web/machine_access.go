package web

import (
	"strings"

	"github.com/openlinkos/beacon/internal/store"
)

// How the console says which machines a credential reaches (store.MachineAccess): never an empty value
// that could be read as all.

// accessText says which machines a covers.
func (s *Server) accessText(a store.MachineAccess) string {
	switch {
	case a.IsEvery():
		return s.t("Every machine, critical ones included")
	case a.IsAll() && len(a.Machines) == 0:
		return s.t("All machines that are not critical, including ones added later")
	case a.IsAll():
		return s.t("All machines that are not critical, including ones added later; critical: %s", s.namedText(a))
	case len(a.Machines) == 0:
		return s.t("No machines")
	}
	return s.namedText(a)
}

// namedText lists the machines a names, each with the end of its access if it has one.
func (s *Server) namedText(a store.MachineAccess) string {
	names := make([]string, 0, len(a.Machines))
	for _, m := range a.Machines {
		if until := a.Until(m); !until.IsZero() {
			m = s.t("%s (until %s)", m, s.clock(until))
		}
		names = append(names, m)
	}
	return strings.Join(names, ", ")
}
