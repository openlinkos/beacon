package bridge

import (
	"context"
	"maps"
	"strconv"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/exercise"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/vm"
)

type exerciseVM struct {
	at       time.Time
	selector string
	series   []vm.Series
}

func (x *exerciseVM) Query(context.Context, string) ([]vm.Sample, error) { return nil, nil }

func (x *exerciseVM) QuerySamplesAt(_ context.Context, selector string, at time.Time) ([]vm.Series, error) {
	x.selector, x.at = selector, at
	return x.series, nil
}

func TestExerciseOriginRequiresTheRawFirstQuorum(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		alter                   func(*vm.Series, *exercise.Observation, time.Time)
		ordinaryRefresh, proved bool
	}{
		{name: "verified first quorum", proved: true},
		{name: "ordinary failure before arming", alter: func(s *vm.Series, _ *exercise.Observation, at time.Time) {
			s.Points = append([]vm.Point{{T: at.Add(-160 * time.Second), V: 0}}, s.Points...)
		}},
		{name: "proof arrived after the evaluation", alter: func(_ *vm.Series, o *exercise.Observation, at time.Time) {
			o.Recorded = at.Add(time.Millisecond)
		}},
		{name: "proof belongs to another assignment", alter: func(s *vm.Series, _ *exercise.Observation, _ time.Time) {
			s.Labels["module"] = "other"
		}},
		{name: "later submission has no trusted anchor", ordinaryRefresh: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			at := e.now.Add(-40 * time.Second).Truncate(time.Millisecond)
			configured := "https://status.example.com/drill/healthz"
			e.b.d.Config.Gateway.ExerciseTarget = configured
			run, _, err := e.st.SetDrill(at.Add(-2*time.Minute), at.Add(10*time.Minute), "developer", "")
			if err != nil {
				t.Fatal(err)
			}
			labels := map[string]string{"service": "drill", "severity": "critical", "failure_domain": "monitoring", "module": "http_get_200", "page": ""}
			target := exercise.Target{URL: configured, Module: labels["module"], Assignment: exercise.Assignment(labels)}
			results := &exerciseVM{}
			for _, id := range []string{"edge-1", "edge-2"} {
				seriesLabels := maps.Clone(labels)
				delete(seriesLabels, "page") // VictoriaMetrics omits empty labels from the raw series.
				seriesLabels["host"], seriesLabels["location"], seriesLabels["region"] = id, id, "overseas"
				seriesLabels["job"], seriesLabels["instance"], seriesLabels["__name__"] = "probe", configured, "probe_success"
				sample := at.Add(-30 * time.Second)
				observation := exercise.Observation{Run: run.ID, Location: id, Target: target, Sampled: sample,
					Served: sample.Add(time.Millisecond), Recorded: sample.Add(2 * time.Millisecond)}
				series := vm.Series{Labels: seriesLabels, Points: []vm.Point{{T: sample, V: 0}}}
				if id == "edge-2" && tc.alter != nil {
					tc.alter(&series, &observation, at)
				}
				if err := e.st.RecordDrillObservation(observation); err != nil {
					t.Fatal(err)
				}
				results.series = append(results.series, series)
			}
			e.b.d.VM = results
			alertLabels := map[string]string{"alertname": "ServiceDown", "service": "drill", "severity": "critical", "failure_domain": "monitoring"}
			webhook := alertmanager.Webhook{GroupKey: "exercise-rule", Status: "firing", GroupLabels: alertLabels, CommonLabels: alertLabels,
				Alerts: []alertmanager.Alert{{Status: "firing", Labels: alertLabels, StartsAt: at.Add(40 * time.Second), Fingerprint: "exercise-rule-member",
					Annotations: map[string]string{exercise.ActiveAtAnnotation: strconv.FormatInt(at.UnixMilli(), 10)}}}}
			id := GroupID(webhook.GroupKey)
			send := func() {
				t.Helper()
				if err := e.b.HandleWebhook(t.Context(), webhook); err != nil {
					t.Fatal(err)
				}
			}
			send()
			if !results.at.Equal(at) || results.selector != `probe_success{job="probe",service="drill",severity="critical",failure_domain="monitoring"}[3m]` {
				t.Fatalf("wrong rule evaluation query: %q at %s", results.selector, results.at)
			}
			if tc.ordinaryRefresh {
				g, _ := e.st.Group(id)
				if g.ExerciseRun != run.ID {
					t.Fatal("the first verified quorum was not classified")
				}
				delete(webhook.Alerts[0].Annotations, exercise.ActiveAtAnnotation)
				send()
			}
			e.now = e.now.Add(time.Minute)
			webhook.Status, webhook.Alerts[0].Status = "resolved", "resolved"
			send()
			originFilter := "unknown"
			if tc.proved {
				originFilter = store.ExerciseOrigin
			}
			page, err := e.b.incidentOutcomes(OutcomeQuery{Group: id, Origin: originFilter})
			if err != nil || len(page.Outcomes) != 1 {
				t.Fatalf("unexpected stored origin: %+v %v", page, err)
			}
			outcome := page.Outcomes[0]
			if tc.proved && (outcome.Origin != store.ExerciseOrigin || outcome.ExerciseRun != run.ID) {
				t.Fatalf("verified run was not retained: %+v", outcome)
			}
			if !tc.proved && (outcome.Origin != "" || outcome.ExerciseRun != "") {
				t.Fatalf("ambiguous run was classified as an exercise: %+v", outcome)
			}
		})
	}
}
