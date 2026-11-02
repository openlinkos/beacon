package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// Silences is the bridge as the socket serves it for `linkos-beacon silence …`: the methods the API and
// MCP call, so that a silence made on the command line is audited and echoed like theirs.
type Silences interface {
	Silence(ctx context.Context, id string, d time.Duration, who bridge.Actor, comment string) (bridge.GroupView, error)
	SilenceLabels(ctx context.Context, labels map[string]string, d time.Duration, who bridge.Actor, comment string) (alertmanager.Silence, error)
	ExpireSilence(ctx context.Context, id string, who bridge.Actor) error
	Silences(ctx context.Context) ([]alertmanager.Silence, error)
}

// SilenceRequest creates a silence for one firing alert group (Alert, its id) or for every alert whose
// labels equal all of Matchers; one of the two. Duration is like 4h, at most 24h; Comment says why.
type SilenceRequest struct {
	Alert    string            `json:"alert,omitempty"`
	Matchers map[string]string `json:"matchers,omitempty"`
	Duration string            `json:"duration"`
	Comment  string            `json:"comment"`
}

// SilenceResponse is the silence made and, when it was made for one alert group, that group.
type SilenceResponse struct {
	Silence alertmanager.Silence `json:"silence"`
	Group   *bridge.GroupView    `json:"group,omitempty"`
}

// SilenceHandler serves the silences' routes under /v1/silences, beside Handler.
func SilenceHandler(s Silences) http.Handler {
	mux := http.NewServeMux()
	who := func(r *http.Request) bridge.Actor { return bridge.Actor{Name: cliActor(r), Via: "cli"} }
	bridgeFail := func(w http.ResponseWriter, err error) {
		code := http.StatusInternalServerError
		switch {
		case errors.Is(err, bridge.ErrNotFound), errors.Is(err, store.ErrNotFound):
			code = http.StatusNotFound
		case errors.Is(err, bridge.ErrNotFiring):
			code = http.StatusConflict
		case errors.Is(err, bridge.ErrBadInput):
			code = http.StatusBadRequest
		}
		fail(w, code, err)
	}
	mux.HandleFunc("GET /v1/silences", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.Silences(r.Context())
		if err != nil {
			bridgeFail(w, err)
			return
		}
		if list == nil {
			list = []alertmanager.Silence{}
		}
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("POST /v1/silences", func(w http.ResponseWriter, r *http.Request) {
		var in SilenceRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		d, err := time.ParseDuration(in.Duration)
		if err != nil {
			fail(w, http.StatusBadRequest, errors.New("duration is like 30m or 4h"))
			return
		}
		if (in.Alert == "") == (len(in.Matchers) == 0) {
			fail(w, http.StatusBadRequest, errors.New("give either an alert group's id or matchers"))
			return
		}
		if in.Alert == "" {
			sil, err := s.SilenceLabels(r.Context(), in.Matchers, d, who(r), in.Comment)
			if err != nil {
				bridgeFail(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, SilenceResponse{Silence: sil})
			return
		}
		g, err := s.Silence(r.Context(), in.Alert, d, who(r), in.Comment)
		if err != nil {
			bridgeFail(w, err)
			return
		}
		out := SilenceResponse{Group: &g}
		if g.Silence != nil {
			out.Silence = alertmanager.Silence{ID: g.Silence.ID, EndsAt: g.Silence.Until, CreatedBy: g.Silence.By, Comment: in.Comment}
			if list, err := s.Silences(r.Context()); err == nil {
				for _, x := range list {
					if x.ID == g.Silence.ID {
						out.Silence = x
					}
				}
			}
		}
		writeJSON(w, http.StatusCreated, out)
	})
	mux.HandleFunc("DELETE /v1/silences/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.ExpireSilence(r.Context(), r.PathValue("id"), who(r)); err != nil {
			bridgeFail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// CreateSilence silences an alert group or every alert matching the request's matchers.
func (c *Client) CreateSilence(ctx context.Context, in SilenceRequest) (SilenceResponse, error) {
	var out SilenceResponse
	err := c.do(ctx, http.MethodPost, "/v1/silences", in, &out)
	return out, err
}

// ListSilences lists the silences, active and pending first as the bridge sorts them.
func (c *Client) ListSilences(ctx context.Context) ([]alertmanager.Silence, error) {
	var out []alertmanager.Silence
	err := c.do(ctx, http.MethodGet, "/v1/silences", nil, &out)
	return out, err
}

// ExpireSilence ends a silence now.
func (c *Client) ExpireSilence(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/silences/"+urlQuery(id), nil, nil)
}
