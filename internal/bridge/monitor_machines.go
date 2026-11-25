package bridge

import (
	"slices"

	"github.com/openlinkos/beacon/internal/store"
)

// incidentMonitorMachines captures designations when an episode opens. The reference ServiceDown
// rule preserves service and severity after counting locations. A routine HTTP monitor has no
// failure domain, so a result for an explicit domain does not identify that monitor.
func (b *Bridge) incidentMonitorMachines(g *store.Group) []store.MonitorMachine {
	if b.d.MonitorMachine == nil || len(labelValues(g, "machine")) > 0 {
		return nil
	}
	var machines []store.MonitorMachine
	for _, a := range g.Alerts {
		labels := a.Labels
		if a.Status != "firing" || labels["alertname"] != "ServiceDown" || labels["failure_domain"] != "" ||
			slices.ContainsFunc(machines, func(m store.MonitorMachine) bool { return m.Service == labels["service"] }) {
			continue
		}
		if m, ok := b.d.MonitorMachine(labels["service"], labels["severity"], a.StartsAt); ok {
			machines = append(machines, m)
		}
	}
	return machines
}
