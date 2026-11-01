package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// Change events: deployments, configuration changes and Flux's reconciliations, posted by CI, Flux or
// a script with a changes:write token, so that the evidence for an alert shows what changed around it.
// POST /api/v1/changes takes Beacon's own form; POST /api/v1/changes/flux takes what Flux's
// notification-controller sends to a provider of type generic.

// ChangeSink stores a change event that the token called by posted (the bridge's RecordChange).
type ChangeSink func(ctx context.Context, c store.Change, by string) (store.Change, error)

const maxChange = 64 << 10

// changeIn is a change event in Beacon's form.
type changeIn struct {
	Source   string    `json:"source"`
	Kind     string    `json:"kind"`
	Service  string    `json:"service"`
	Machine  string    `json:"machine"`
	Summary  string    `json:"summary"`
	URL      string    `json:"url"`
	Revision string    `json:"revision"`
	Time     time.Time `json:"time"`
}

// fluxEvent is the event Flux's notification-controller posts to a generic provider
// (github.com/fluxcd/pkg/apis/event/v1beta1). Fields not listed are ignored, so newer versions work.
type fluxEvent struct {
	InvolvedObject struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"involvedObject"`
	Severity            string            `json:"severity"`
	Timestamp           time.Time         `json:"timestamp"`
	Message             string            `json:"message"`
	Reason              string            `json:"reason"`
	Metadata            map[string]string `json:"metadata"`
	ReportingController string            `json:"reportingController"`
}

// meta is a metadata value by its key, or by a key ending in /key (Flux prefixes keys with the API
// group, as in kustomize.toolkit.fluxcd.io/revision).
func meta(m map[string]string, key string) string {
	if v := m[key]; v != "" {
		return v
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if strings.HasSuffix(k, "/"+key) && m[k] != "" {
			return m[k]
		}
	}
	return ""
}

// fluxChangeOf maps Flux's event to a change. The service and machine it concerns come from the event's
// metadata (an Alert's eventMetadata, such as service: hub-linkos-io) or else from the provider's
// address (?service=…&machine=…).
func fluxChangeOf(ev fluxEvent, service, machine string) store.Change {
	kind := ev.Reason
	if kind == "" {
		kind = "event"
	}
	object := ev.InvolvedObject.Kind
	for _, p := range []string{ev.InvolvedObject.Namespace, ev.InvolvedObject.Name} {
		if p != "" {
			object += "/" + p
		}
	}
	summary := ev.Message
	if s := meta(ev.Metadata, "summary"); s != "" {
		summary = s + ": " + summary
	}
	c := store.Change{Time: ev.Timestamp, Source: "flux", Kind: kind, Summary: summary, Object: object, Severity: ev.Severity,
		Revision: meta(ev.Metadata, "revision"), Service: meta(ev.Metadata, "service"), Machine: meta(ev.Metadata, "machine")}
	if c.Service == "" {
		c.Service = service
	}
	if c.Machine == "" {
		c.Machine = machine
	}
	return c
}

func (g *gateway) record(w http.ResponseWriter, r *http.Request, t store.Token, c store.Change) {
	if g.Changes == nil {
		fail(w, http.StatusServiceUnavailable, "upstream", "change events are not taken here")
		return
	}
	out, err := g.Changes(r.Context(), c, t.Name)
	if err != nil {
		if errors.Is(err, bridge.ErrBadInput) {
			fail(w, http.StatusBadRequest, "bad_input", err.Error())
			return
		}
		if g.Log != nil {
			g.Log.Error("recording a change failed", "err", err)
		}
		fail(w, http.StatusBadGateway, "upstream", "the change could not be stored")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(out)
}

// change takes a change event in Beacon's form.
func (g *gateway) change(w http.ResponseWriter, r *http.Request, t store.Token) {
	var in changeIn
	dec := json.NewDecoder(io.LimitReader(r.Body, maxChange))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "bad_input", "the body is not a change event: "+err.Error()+
			"; send JSON with source, kind, summary and optionally service, machine, url, revision and time")
		return
	}
	g.record(w, r, t, store.Change{Time: in.Time, Source: in.Source, Kind: in.Kind, Service: in.Service, Machine: in.Machine,
		Summary: in.Summary, URL: in.URL, Revision: in.Revision})
}

// fluxChange takes an event from Flux's notification-controller (a Provider of type generic).
func (g *gateway) fluxChange(w http.ResponseWriter, r *http.Request, t store.Token) {
	var ev fluxEvent
	if err := json.NewDecoder(io.LimitReader(r.Body, maxChange)).Decode(&ev); err != nil {
		fail(w, http.StatusBadRequest, "bad_input", "the body is not a Flux event: "+err.Error())
		return
	}
	q := r.URL.Query()
	g.record(w, r, t, fluxChangeOf(ev, q.Get("service"), q.Get("machine")))
}
