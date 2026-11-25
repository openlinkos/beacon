package bridge

import (
	"context"
	"maps"
	"strconv"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/exercise"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/vm"
)

// The stock ServiceDown rule reads this exact window at every evaluation. Its first evaluation
// timestamp comes from the rule engine, which is the only credential allowed to set the annotation.
const exerciseRuleWindow = 3 * time.Minute

type exerciseQueries interface {
	QuerySamplesAt(context.Context, string, time.Time) ([]vm.Series, error)
}

func (b *Bridge) exerciseOrigin(ctx context.Context, id string, w alertmanager.Webhook) string {
	if w.Status != "firing" || b.d.Config.Gateway.ExerciseTarget == "" {
		return ""
	}
	member, ok := singleFiringAlert(w)
	if !ok || member.Fingerprint == "" || member.Labels["alertname"] != "ServiceDown" || member.Labels["service"] != "drill" {
		return ""
	}
	at, err := strconv.ParseInt(member.Annotations[exercise.ActiveAtAnnotation], 10, 64)
	if err != nil || at <= 0 {
		return ""
	}
	active := time.UnixMilli(at)
	severity, domain := member.Labels["severity"], member.Labels["failure_domain"]
	if active.After(b.now()) || member.StartsAt.Before(active) || severity == "" || domain == "" {
		return ""
	}
	if group, exists := b.d.Store.Group(id); exists && group.Firing() {
		return ""
	}
	vmq, ok := b.d.VM.(exerciseQueries)
	if !ok {
		return ""
	}
	run, observations, err := b.d.Store.DrillEvidence()
	if err != nil || run.ID == "" || active.Before(run.Armed) || len(observations) == 0 {
		return ""
	}
	verifyCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	selector := `probe_success{job="probe",service="drill",severity=` + strconv.Quote(severity) +
		`,failure_domain=` + strconv.Quote(domain) + `}[3m]`
	samples, err := vmq.QuerySamplesAt(verifyCtx, selector, active)
	if err != nil || !exerciseQuorum(run, observations, samples, active) {
		return ""
	}
	return run.ID
}

func singleFiringAlert(w alertmanager.Webhook) (alertmanager.Alert, bool) {
	var match alertmanager.Alert
	count := 0
	for _, member := range w.Alerts {
		if member.Status == "firing" {
			match = member
			count++
		}
	}
	return match, w.TruncatedAlerts == 0 && count == 1
}

// A continuing episode remains an exercise only while the same alert is firing. A second member
// or a replacement firing within the group may be a separate outage.
func sameExerciseAlert(g store.Group, w alertmanager.Webhook) bool {
	member, ok := singleFiringAlert(w)
	if !ok || member.Fingerprint == "" {
		return false
	}
	for _, old := range g.Alerts {
		anchor := old.Annotations[exercise.ActiveAtAnnotation]
		if old.Status == "firing" && old.Fingerprint == member.Fingerprint && old.StartsAt.Equal(member.StartsAt) &&
			anchor != "" && anchor == member.Annotations[exercise.ActiveAtAnnotation] {
			return true
		}
	}
	return false
}

type exercisePoint struct {
	location string
	target   exercise.Target
	sampled  int64
}

type exerciseLocation struct {
	region  string
	healthy bool
	points  []exercisePoint
}

func exerciseQuorum(run exercise.Run, observations []exercise.Observation, samples []vm.Series, active time.Time) bool {
	from := active.Add(-exerciseRuleWindow)
	verified := make(map[exercisePoint]bool)
	for _, o := range observations {
		if o.Run == run.ID && o.Sampled.After(from) && !o.Sampled.Before(run.Armed) &&
			!o.Recorded.IsZero() && !o.Recorded.After(active) {
			verified[exercisePoint{o.Location, o.Target, o.Sampled.UnixMilli()}] = true
		}
	}
	locations := make(map[string]*exerciseLocation)
	for _, series := range samples {
		id, region := series.Labels["location"], series.Labels["region"]
		if id == "" || region == "" || id != series.Labels["host"] || len(series.Points) == 0 {
			return false
		}
		group := locations[id]
		if group == nil {
			group = &exerciseLocation{region: region}
			locations[id] = group
		}
		if group.region != region {
			return false
		}
		labels := maps.Clone(series.Labels)
		for _, added := range []string{"__name__", "job", "instance", "location", "region", "network", "host"} {
			delete(labels, added)
		}
		target := exercise.Target{URL: series.Labels["instance"], Module: labels["module"], Assignment: exercise.Assignment(labels)}
		for _, point := range series.Points {
			if !point.T.After(from) || point.T.After(active) || (point.V != 0 && point.V != 1) {
				return false
			}
			if point.V == 1 {
				group.healthy = true
			}
			group.points = append(group.points, exercisePoint{id, target, point.T.UnixMilli()})
		}
	}
	failing := 0
	for _, group := range locations {
		if group.healthy {
			continue
		}
		for _, point := range group.points {
			if !verified[point] {
				return false
			}
		}
		failing++
	}
	return failing >= 2 && failing*2 >= len(locations)
}
