package bridge

import (
	"slices"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

// incidentMonitorMachines captures a designation at opening only when every firing alert names a
// recognised scope and all those scopes designate the same enrolment.
func (b *Bridge) incidentMonitorMachines(g *store.Group, truncated int) []store.MonitorMachine {
	if b.d.MonitorMachine == nil || truncated != 0 || len(labelValues(g, "machine")) > 0 {
		return nil
	}
	var machines []store.MonitorMachine
	for _, member := range g.Alerts {
		if member.Status != "firing" {
			continue
		}
		scope, ok := outageScope(member.Labels)
		if !ok {
			return nil
		}
		m, ok := b.d.MonitorMachine(scope, member.StartsAt)
		if !ok || len(machines) > 0 && (machines[0].Name != m.Name || machines[0].Registration != m.Registration) {
			return nil
		}
		if !slices.Contains(machines, m) {
			machines = append(machines, m)
		}
	}
	return machines
}

func outageScope(labels map[string]string) (targets.OutageScope, bool) {
	scope := targets.OutageScope{Service: labels["service"], Severity: labels["severity"],
		FailureDomain: labels["failure_domain"], Page: labels["page"], Visibility: labels["visibility"]}
	// Instance and location rules have a different input shape from the aggregated ServiceDown
	// supported here. Ignore them instead of assuming their alert has the aggregate's scope.
	return scope, labels["alertname"] == "ServiceDown" && scope.Service != "" && scope.Severity != "" &&
		labels["instance"] == "" && labels["module"] == "" && labels["location"] == "" && labels["region"] == ""
}

// An existing designation can survive a subset of the original firing members. A new member or
// explicit machine cannot inherit the snapshot, even if it has the same alert name and service.
func sameMonitorMembers(g store.Group, w alertmanager.Webhook) bool {
	if w.TruncatedAlerts != 0 || w.GroupLabels["machine"] != "" || w.CommonLabels["machine"] != "" {
		return false
	}
	firing := false
	for _, member := range w.Alerts {
		if member.Labels["machine"] != "" {
			return false
		}
		if member.Status != "firing" {
			continue
		}
		firing = true
		scope, ok := outageScope(member.Labels)
		if !ok || member.Fingerprint == "" || !slices.ContainsFunc(g.Alerts, func(previous store.Alert) bool {
			old, ok := outageScope(previous.Labels)
			return ok && previous.Status == "firing" && previous.Fingerprint == member.Fingerprint &&
				previous.StartsAt.Equal(member.StartsAt) && old == scope
		}) {
			return false
		}
	}
	return firing
}
