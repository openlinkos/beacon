package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/openlinkos/beacon/internal/exercise"
)

// DrillStore keeps the drill's end and the audit log.
type DrillStore interface {
	SetDrill(now, until time.Time, by, detail string) (exercise.Run, bool, error)
	DrillRun() exercise.Run
	DrillObservedLocations(run string) []string
}

// DrillHandler serves /v1/drill, beside Handler. record, when not nil, records a drill starting (until is
// when it ends) or a running drill ending (until is zero) as a change event, so that the evidence of the
// alert a drill causes says it is one, and until when.
func DrillHandler(s DrillStore, record func(ctx context.Context, until time.Time, by string) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/drill", func(w http.ResponseWriter, _ *http.Request) {
		run := s.DrillRun()
		out := DrillState{Run: run.ID, Observed: s.DrillObservedLocations(run.ID)}
		if run.Ended.IsZero() {
			out.Until = run.Until
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /v1/drill", func(w http.ResponseWriter, r *http.Request) {
		var in DrillRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		now := time.Now()
		var until time.Time
		if in.Duration != "off" {
			d, err := time.ParseDuration(in.Duration)
			if err != nil || d <= 0 || d > MaxDrill {
				fail(w, http.StatusBadRequest, errors.New("a drill lasts between 1s and 2h, or is off"))
				return
			}
			until = now.Add(d)
		}
		by := cliActor(r)
		run, running, err := s.SetDrill(now, until, by, in.Duration)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		if record != nil && (!until.IsZero() || running) {
			_ = record(r.Context(), until, by)
		}
		writeJSON(w, http.StatusOK, DrillState{Until: until, Run: run.ID})
	})
	return mux
}
