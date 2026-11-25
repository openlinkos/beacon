package gateway

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/exercise"
	"github.com/openlinkos/beacon/internal/store"
)

// exerciseAssignment accepts one unambiguous configured target in this location's assignment.
// Labels and credentials from a sample or an alert cannot select a target.
func (g *gateway) exerciseAssignment(cat *catalog.Catalog, location catalog.Location) exercise.Target {
	if g.ExerciseTarget == "" || g.Targets == nil {
		return exercise.Target{}
	}
	locations := make([]string, 0, len(cat.Locations))
	for _, l := range cat.Locations {
		locations = append(locations, l.ID)
	}
	var target exercise.Target
	for _, group := range g.Targets().For(location.ID, location.Region, locations) {
		for _, instance := range group.Targets {
			if instance != g.ExerciseTarget {
				continue
			}
			if target.URL != "" {
				return exercise.Target{}
			}
			target = exercise.Target{URL: instance, Module: group.Labels["module"], Assignment: exercise.Assignment(group.Labels)}
		}
	}
	return target
}

func (g *gateway) observeExercise(w http.ResponseWriter, r *http.Request, t store.Token) {
	if g.refuseStandby(w) {
		return
	}
	cat, l, ok := g.active(w, t)
	if !ok {
		return
	}
	var in struct {
		Receipt string `json:"receipt"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "exercise.invalid_receipt", "a drill response receipt is required")
		return
	}
	run := g.Auth.Store.DrillRun()
	target := g.exerciseAssignment(cat, l)
	observation, ok := g.Exercise.Accept(run, in.Receipt, target, l.ID, t.Hash, g.Now())
	if !ok {
		fail(w, http.StatusBadRequest, "exercise.invalid_receipt", "the drill response receipt is invalid or expired")
		return
	}
	if err := g.Auth.Store.RecordDrillObservation(observation); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(w, http.StatusConflict, "exercise.not_current", "the drill changed or reached its observation limit")
			return
		}
		g.Log.Error("store drill observation failed", "err", err)
		fail(w, http.StatusBadGateway, "upstream", "could not store the drill observation")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
