package bridge

import (
	"maps"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

func TestIncidentMonitorMachineRequiresReferenceOutage(t *testing.T) {
	started := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	var lookedUp []string
	b := Bridge{d: Deps{MonitorMachine: func(service, severity string, at time.Time) (store.MonitorMachine, bool) {
		lookedUp = append(lookedUp, service)
		if severity != "critical" || !at.Equal(started) {
			t.Fatalf("lookup used the wrong alert scope: %s %s %s", service, severity, at)
		}
		return store.MonitorMachine{Service: service, Name: "app-1", Registration: "original"}, true
	}}}
	labels := map[string]string{"alertname": "ServiceDown", "service": "api", "severity": "critical"}
	member := func(status string, more map[string]string) store.Alert {
		a := store.Alert{Status: status, StartsAt: started, Labels: maps.Clone(labels)}
		maps.Copy(a.Labels, more)
		return a
	}
	g := store.Group{Common: labels, Alerts: []store.Alert{
		member("resolved", nil),
		member("firing", map[string]string{"failure_domain": "edge-1"}),
		member("firing", map[string]string{"alertname": "ServiceDegraded", "service": "second"}),
	}}
	if links := b.incidentMonitorMachines(&g); len(links) != 0 || len(lookedUp) != 0 {
		t.Fatalf("unrelated group members matched via common labels: %+v, %v", links, lookedUp)
	}
	g.Alerts = append(g.Alerts, member("firing", nil))
	if links := b.incidentMonitorMachines(&g); len(links) != 1 || links[0].Service != "api" || len(lookedUp) != 1 {
		t.Fatalf("reference outage did not match its own scope: %+v, %v", links, lookedUp)
	}
	g.Alerts[3].Labels["machine"] = "explicit-machine"
	if links := b.incidentMonitorMachines(&g); len(links) != 0 || len(lookedUp) != 1 {
		t.Fatalf("a designation competed with an explicit machine: %+v, %v", links, lookedUp)
	}
}
