package bridge

import (
	"maps"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

func TestIncidentMonitorMachineRequiresFullFiringScope(t *testing.T) {
	started := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	var seen []targets.OutageScope
	b := Bridge{d: Deps{MonitorMachine: func(scope targets.OutageScope, at time.Time) (store.MonitorMachine, bool) {
		seen = append(seen, scope)
		if scope.Service != "api" || scope.Severity != "critical" || scope.Page != "customer" ||
			scope.Visibility != "public" || !at.Equal(started) {
			t.Fatalf("lookup used the wrong alert scope: %+v %v", scope, at)
		}
		switch scope.FailureDomain {
		case "east", "north":
			return store.MonitorMachine{Service: scope.Service, Name: "app-1", Registration: "original"}, true
		case "west":
			return store.MonitorMachine{Service: scope.Service, Name: "app-1", Registration: "replacement"}, true
		default:
			return store.MonitorMachine{}, false
		}
	}}}
	labels := map[string]string{"alertname": "ServiceDown", "service": "api", "severity": "critical", "page": "customer", "visibility": "public"}
	member := func(status, domain string) store.Alert {
		a := store.Alert{Status: status, StartsAt: started, Labels: maps.Clone(labels)}
		a.Labels["failure_domain"] = domain
		return a
	}
	g := store.Group{Common: labels, Alerts: []store.Alert{member("resolved", "not-mapped"), member("firing", "east"), member("firing", "north")}}
	if links := b.incidentMonitorMachines(&g, 0); len(links) != 1 || links[0].Registration != "original" || len(seen) != 2 {
		t.Fatalf("not all firing members confirmed the same enrolment: %+v, %+v", links, seen)
	}
	if links := b.incidentMonitorMachines(&g, 1); len(links) != 0 || len(seen) != 2 {
		t.Fatalf("truncated group linked a machine: %+v, %+v", links, seen)
	}
	g.Alerts[2].Labels["failure_domain"] = "west"
	if links := b.incidentMonitorMachines(&g, 0); len(links) != 0 {
		t.Fatalf("deduplicated by service before checking a different registration: %+v", links)
	}
	g.Alerts[2].Labels["failure_domain"] = "missing"
	if links := b.incidentMonitorMachines(&g, 0); len(links) != 0 {
		t.Fatalf("linked with a missing member: %+v", links)
	}
	g.Alerts[2].Labels["failure_domain"] = "north"
	g.Alerts[2].Labels["machine"] = "explicit-machine"
	if links := b.incidentMonitorMachines(&g, 0); len(links) != 0 {
		t.Fatalf("a designation competed with an explicit machine: %+v", links)
	}
	delete(g.Alerts[2].Labels, "machine")
	delete(g.Alerts[2].Labels, "service")
	if links := b.incidentMonitorMachines(&g, 0); len(links) != 0 {
		t.Fatalf("an incomplete member borrowed its scope from common labels: %+v", links)
	}
}

func TestMonitorMachineSnapshotRejectsNewMembersAndExplicitMachines(t *testing.T) {
	for _, test := range []struct{ label, value string }{{"failure_domain", "west"}, {"machine", "actual-member"}} {
		t.Run(test.label, func(t *testing.T) {
			e := newEnv(t)
			calls := 0
			e.b.d.MonitorMachine = func(scope targets.OutageScope, _ time.Time) (store.MonitorMachine, bool) {
				calls++
				return store.MonitorMachine{Service: scope.Service, Name: "app-1", Registration: "original"}, true
			}
			w := hook("firing", "critical", "f1")
			w.Alerts[0].Labels = map[string]string{"alertname": "ServiceDown", "service": "api", "severity": "critical", "failure_domain": "east"}
			id := GroupID(w.GroupKey)
			post := func() store.Group {
				t.Helper()
				if err := e.b.HandleWebhook(t.Context(), w); err != nil {
					t.Fatal(err)
				}
				g, _ := e.st.Group(id)
				return g
			}
			initial := post()
			if calls != 1 || len(initial.MonitorMachines) != 1 {
				t.Fatalf("no opening snapshot: %+v", initial.MonitorMachines)
			}
			if unchanged := post(); calls != 1 || len(unchanged.MonitorMachines) != 1 {
				t.Fatalf("same firing member dropped or refreshed the snapshot: %+v", unchanged.MonitorMachines)
			}
			original := w.Alerts[0]
			if test.label == "machine" {
				w.Alerts[0].Labels = maps.Clone(original.Labels)
				w.Alerts[0].Labels[test.label] = test.value
			} else {
				added := alertmanager.Alert{Status: "firing", StartsAt: original.StartsAt.Add(time.Minute),
					Labels: maps.Clone(original.Labels), Fingerprint: "f2"}
				added.Labels[test.label] = test.value
				w.Alerts = append(w.Alerts, added)
			}
			if changed := post(); calls != 1 || changed.Episode != initial.Episode || len(changed.MonitorMachines) != 0 {
				t.Fatalf("the continuing episode kept a questionable snapshot: %+v", changed.MonitorMachines)
			}
			w.Alerts = []alertmanager.Alert{original}
			if restored := post(); calls != 1 || len(restored.MonitorMachines) != 0 {
				t.Fatalf("reinstated a designation midway through the episode: %+v", restored.MonitorMachines)
			}
		})
	}
}
