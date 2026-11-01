package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/store"
)

// Config is the configuration Beacon holds, as the socket serves it for `linkos-beacon config …`.
type Config interface {
	Import(ctx context.Context, who configdb.Actor, f configdb.Files, note string, dryRun bool) (store.ConfigChange, error)
	Export() (configdb.Files, int64, error)
	History(f configdb.HistoryFilter) ([]store.ConfigChange, error)
	Revert(ctx context.Context, who configdb.Actor, version int64, note string) (store.ConfigChange, error)
	Check() error
	Version() int64
	Summary() string
}

// ImportRequest imports files (paths as configdb.Files has them), or with DryRun says what it would change.
type ImportRequest struct {
	Files  configdb.Files `json:"files"`
	Note   string         `json:"note,omitempty"`
	DryRun bool           `json:"dry_run,omitempty"`
}

// Export is the configuration in force as files.
type Export struct {
	Version int64          `json:"version"`
	Files   configdb.Files `json:"files"`
}

// RevertRequest undoes the change that made Version.
type RevertRequest struct {
	Version int64  `json:"version"`
	Note    string `json:"note,omitempty"`
}

// ConfigState is what check-config reports of the running service's configuration.
type ConfigState struct {
	Version int64  `json:"version"`
	Summary string `json:"summary"`
	Problem string `json:"problem,omitempty"`
}

// MaxImport is the largest import the socket takes.
const MaxImport = 16 << 20

// ConfigHandler serves the configuration's routes under /v1/config/, beside Handler.
func ConfigHandler(c Config) http.Handler {
	mux := http.NewServeMux()
	who := func(r *http.Request) configdb.Actor { return configdb.Actor{Name: cliActor(r), Via: "cli"} }
	configFail := func(w http.ResponseWriter, err error) {
		code := http.StatusInternalServerError
		switch {
		case errors.Is(err, configdb.ErrBadInput), errors.Is(err, configdb.ErrInvalid):
			code = http.StatusBadRequest
		case errors.Is(err, configdb.ErrNotFound):
			code = http.StatusNotFound
		case errors.Is(err, configdb.ErrConflict):
			code = http.StatusConflict
		}
		fail(w, code, err)
	}
	mux.HandleFunc("POST /v1/config/import", func(w http.ResponseWriter, r *http.Request) {
		var in ImportRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxImport))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		ch, err := c.Import(r.Context(), who(r), in.Files, in.Note, in.DryRun)
		if err != nil {
			configFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ch)
	})
	mux.HandleFunc("GET /v1/config/export", func(w http.ResponseWriter, _ *http.Request) {
		f, v, err := c.Export()
		if err != nil {
			configFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, Export{Version: v, Files: f})
	})
	mux.HandleFunc("GET /v1/config/history", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list, err := c.History(configdb.HistoryFilter{Limit: limit})
		if err != nil {
			configFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("POST /v1/config/revert", func(w http.ResponseWriter, r *http.Request) {
		var in RevertRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		ch, err := c.Revert(r.Context(), who(r), in.Version, in.Note)
		if err != nil {
			configFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ch)
	})
	mux.HandleFunc("GET /v1/config/check", func(w http.ResponseWriter, _ *http.Request) {
		st := ConfigState{Version: c.Version(), Summary: c.Summary()}
		if err := c.Check(); err != nil {
			st.Problem = err.Error()
		}
		writeJSON(w, http.StatusOK, st)
	})
	return mux
}

// ImportConfig imports files into the running service's configuration.
func (c *Client) ImportConfig(ctx context.Context, in ImportRequest) (store.ConfigChange, error) {
	var out store.ConfigChange
	err := c.do(ctx, http.MethodPost, "/v1/config/import", in, &out)
	return out, err
}

// ExportConfig fetches the configuration in force as files.
func (c *Client) ExportConfig(ctx context.Context) (Export, error) {
	var out Export
	err := c.do(ctx, http.MethodGet, "/v1/config/export", nil, &out)
	return out, err
}

// ConfigHistory lists the latest changes to the configuration.
func (c *Client) ConfigHistory(ctx context.Context, limit int) ([]store.ConfigChange, error) {
	var out []store.ConfigChange
	err := c.do(ctx, http.MethodGet, "/v1/config/history?limit="+strconv.Itoa(limit), nil, &out)
	return out, err
}

// RevertConfig undoes one change.
func (c *Client) RevertConfig(ctx context.Context, in RevertRequest) (store.ConfigChange, error) {
	var out store.ConfigChange
	err := c.do(ctx, http.MethodPost, "/v1/config/revert", in, &out)
	return out, err
}

// CheckConfig reports whether the running service's configuration passes its checks.
func (c *Client) CheckConfig(ctx context.Context) (ConfigState, error) {
	var out ConfigState
	err := c.do(ctx, http.MethodGet, "/v1/config/check", nil, &out)
	return out, err
}
