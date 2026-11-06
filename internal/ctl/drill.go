package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// DrillStore keeps the drill's end and the audit log.
type DrillStore interface {
	SetTime(key string, t time.Time) error
	Time(key string) time.Time
	Audit(e store.AuditEntry) error
}

// DrillHandler serves /v1/drill, beside Handler. record, when not nil, records a drill starting (until is
// when it ends) or a running drill ending (until is zero) as a change event, so that the evidence of the
// alert a drill causes says it is one, and until when.
func DrillHandler(s DrillStore, record func(ctx context.Context, until time.Time, by string) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/drill", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, DrillState{Until: s.Time(DrillKey)})
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
		running := s.Time(DrillKey).After(now)
		if err := s.SetTime(DrillKey, until); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		by := cliActor(r)
		_ = s.Audit(store.AuditEntry{Actor: by, Via: "cli", Action: "drill", Detail: in.Duration})
		if record != nil && (!until.IsZero() || running) {
			_ = record(r.Context(), until, by)
		}
		writeJSON(w, http.StatusOK, DrillState{Until: until})
	})
	return mux
}
