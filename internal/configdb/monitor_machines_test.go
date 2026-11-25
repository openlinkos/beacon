package configdb

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

func TestImportedHTTPMachineBindingMatchesOnlyItsAlertScope(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	e.s.d.Now = func() time.Time { return now }
	files := testFiles()
	files["targets/all-checkout.yaml"] = `
- targets: [https://example.com/api, https://example.com/login]
  labels: {service: checkout, module: http_head, severity: critical, failure_domain: east, page: customer, visibility: public}
- targets: [https://example.com/pay]
  labels: {service: checkout, module: http_head, failure_domain: east, page: customer, visibility: public}
- targets: [https://example.net/api]
  labels: {service: checkout, module: http_head, severity: critical, failure_domain: west, page: customer, visibility: public}
- targets: [https://example.com/admin]
  labels: {service: checkout, module: tcp_443, severity: critical, failure_domain: east, page: staff, visibility: internal}
`
	if _, err := e.s.Import(ctx, api, files, "", false); err != nil {
		t.Fatal(err)
	}
	if err := e.st.PutMachine(store.Machine{Name: "app-1", Added: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	machine, _ := e.st.Machine("app-1")
	binding := targets.MachineBinding{Name: machine.Name, Registration: machine.Registration()}
	version := e.s.Version()
	edit := func(index int, b *targets.MachineBinding, v int64) (store.ConfigChange, error) {
		return e.s.SetTargetMachineIf(ctx, api, "all-checkout", index, b, "Designate the response machine", v)
	}
	groups := e.s.State().ImportedHTTPGroups("checkout")
	if len(groups) != 3 || groups[0].Set != "all-checkout" || groups[1].Index != 1 ||
		!slices.Equal(groups[0].Group.Targets, []string{"https://example.com/api", "https://example.com/login"}) {
		t.Fatalf("selectable groups: %+v", groups)
	}
	if _, err := edit(3, &binding, version); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the non-HTTP profile is ineligible: %v", err)
	}
	ch, err := edit(0, &binding, version)
	if err != nil || ch.Version != version+1 || len(ch.Items) != 1 || ch.Items[0].ConsoleAfter || e.s.State().ConsoleTarget("all-checkout") {
		t.Fatalf("binding changed ownership or missed configuration history: %+v %v", ch, err)
	}
	if len(e.st.AuditLog(1)) != 1 || e.st.AuditLog(1)[0].Target != "targets all-checkout" {
		t.Fatal("binding bypassed the configuration audit")
	}
	now = now.Add(time.Second)
	scope := targets.OutageScope{Service: "checkout", Severity: "critical", FailureDomain: "east", Page: "customer", Visibility: "public"}
	if _, ok := e.s.MonitorMachineForScope(scope, now); ok {
		t.Fatal("linked with an unbound group whose empty severity also emits critical")
	}
	if _, err := edit(1, &binding, version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale ordinal: %v", err)
	}
	if _, err := edit(1, &binding, ch.Version); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.s.MonitorMachineForScope(scope, now); ok {
		t.Fatal("same-time configuration change was attributed to the alert")
	}
	now = now.Add(time.Second)
	validStart := now
	check := func(other targets.OutageScope, at time.Time, want bool) {
		t.Helper()
		got, ok := e.s.MonitorMachineForScope(other, at)
		if ok != want || (ok && (got.Name != machine.Name || got.Registration != binding.Registration || got.Service != "checkout")) {
			t.Fatalf("scope %+v: got %+v (%v), wanted association=%v", other, got, ok, want)
		}
	}
	check(scope, validStart, true)
	other := scope
	other.FailureDomain = ""
	check(other, validStart, false) // an omitted dimension includes the unbound west group
	other = scope
	other.Page, other.Visibility = "", ""
	check(other, validStart, false) // the matching non-HTTP staff group is not a valid contributor
	other = scope
	other.FailureDomain = "absent"
	check(other, validStart, false)
	other = scope
	other.Severity = "warning"
	check(other, validStart, false)

	// Assigning the unrelated west group in the same file after the alert began leaves the exact
	// east group unchanged. A second enrolment there still prevents any domain-collapsed link.
	now = now.Add(time.Second)
	if err := e.st.PutMachine(store.Machine{Name: "app-2", Added: now}); err != nil {
		t.Fatal(err)
	}
	west, _ := e.st.Machine("app-2")
	if _, err := edit(2, &targets.MachineBinding{Name: west.Name, Registration: west.Registration()}, e.s.Version()); err != nil {
		t.Fatal(err)
	}
	check(scope, validStart, true)
	now = now.Add(time.Second)
	other = scope
	other.FailureDomain = ""
	check(other, now, false)
	exported, _, err := e.s.Export()
	if body := exported["targets/all-checkout.yaml"]; err != nil || !strings.Contains(body, binding.Registration) ||
		!strings.Contains(body, "https://example.com/login") || !strings.Contains(body, "failure_domain: east") {
		t.Fatalf("binding did not round-trip alongside the existing target configuration: %v\n%s", err, body)
	}
	wire := e.s.Targets().For("cdn-pre-us1", "overseas", []string{"cdn-pre-us1"})
	for _, group := range wire {
		if group.Machine != nil {
			t.Fatal("the probe payload exposed the response machine")
		}
	}
	if err := e.st.DeleteMachine(machine.Name); err != nil {
		t.Fatal(err)
	}
	if err := e.st.PutMachine(store.Machine{Name: machine.Name, Added: now}); err != nil {
		t.Fatal(err)
	}
	check(scope, validStart, false)
	version = e.s.Version()
	if _, err := edit(0, &binding, version); !errors.Is(err, store.ErrConfigMachineChanged) || e.s.Version() != version {
		t.Fatalf("re-enrolment accepted a stale selection: %v", err)
	}
	if _, err := edit(0, nil, version); err != nil || e.s.State().ImportedHTTPGroups("checkout")[0].Group.Machine != nil {
		t.Fatalf("clearing the obsolete designation: %v", err)
	}
}

func TestMonitorBindingChecksRemovedHistoricalContributors(t *testing.T) {
	for _, test := range []struct {
		name   string
		before time.Duration
		want   bool
	}{
		{"removed after alert started", -time.Minute, false},
		{"removed within the stock observation window", time.Minute, false},
		{"removed before any sample could contribute", 10 * time.Minute, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := newEnv(t)
			start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
			now := start.Add(-time.Hour)
			e.s.d.Now = func() time.Time { return now }
			if err := e.st.PutMachine(store.Machine{Name: "app-1", Added: now.Add(-time.Hour)}); err != nil {
				t.Fatal(err)
			}
			m, _ := e.st.Machine("app-1")
			files := testFiles()
			files["targets/all-still.yaml"] = `
- targets: [https://example.com/live]
  labels: {service: checkout, module: http_head, severity: critical, failure_domain: edge, page: customer}
  machine: {name: app-1, registration: REGISTRATION}
`
			files["targets/all-still.yaml"] = strings.Replace(files["targets/all-still.yaml"], "REGISTRATION", m.Registration(), 1)
			files["targets/overseas-old.yaml"] = `
- targets: [https://example.net/old]
  labels: {service: checkout, module: http_head, severity: critical, failure_domain: edge, page: customer}
`
			if _, err := e.s.Import(ctx, api, files, "", false); err != nil {
				t.Fatal(err)
			}
			now = start.Add(-test.before)
			if _, err := e.s.Delete(ctx, api, KindTargets, "overseas-old", "Remove the old endpoint"); err != nil {
				t.Fatal(err)
			}
			now = start.Add(2 * time.Minute)
			scope := targets.OutageScope{Service: "checkout", Severity: "critical", FailureDomain: "edge", Page: "customer"}
			got, ok := e.s.MonitorMachineForScope(scope, start)
			if ok != test.want || ok && got.Registration != m.Registration() {
				t.Fatalf("retired contributor yielded %+v (%v), wanted %v", got, ok, test.want)
			}
		})
	}
}
