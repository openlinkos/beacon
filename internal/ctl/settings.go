package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/openlinkos/beacon/internal/settings"
)

// Settings is the service the socket serves for `beacon settings …`.
type Settings interface {
	List() settings.Listing
	System(key string) (settings.Resolved, error)
	History(key, scope, target string, before int64, limit int) []settings.Change
	ChangeAt(version int64) (settings.Change, bool)
	Set(ctx context.Context, who settings.Actor, scope, target, key string, value json.RawMessage, reason string, ifVersion int64) (settings.Change, error)
	Reset(ctx context.Context, who settings.Actor, scope, target, key, reason string, ifVersion int64) (settings.Change, error)
	Revert(ctx context.Context, who settings.Actor, version int64, reason string) (settings.Change, error)
}

// SetSettingRequest changes one setting. Scope defaults to the system scope; Value is the JSON as
// it is stored.
type SetSettingRequest struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	Scope     string          `json:"scope,omitempty"`
	Target    string          `json:"target,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	IfVersion int64           `json:"if_version,omitempty"`
}

// ResetSettingRequest takes a row away, so the level above (or the default) answers again.
type ResetSettingRequest struct {
	Key       string `json:"key"`
	Scope     string `json:"scope,omitempty"`
	Target    string `json:"target,omitempty"`
	Reason    string `json:"reason,omitempty"`
	IfVersion int64  `json:"if_version,omitempty"`
}

// RevertSettingRequest undoes the change that made Version.
type RevertSettingRequest struct {
	Version int64  `json:"version"`
	Reason  string `json:"reason,omitempty"`
}

// SettingsHandler serves the settings' routes under /v1/settings/, beside Handler.
func SettingsHandler(s Settings) http.Handler {
	mux := http.NewServeMux()
	who := func(r *http.Request) settings.Actor { return settings.Actor{Name: cliActor(r), Via: "cli"} }
	settingsFail := func(w http.ResponseWriter, err error) {
		code := http.StatusInternalServerError
		switch {
		case errors.Is(err, settings.ErrBadInput), errors.Is(err, settings.ErrInvalid):
			code = http.StatusBadRequest
		case errors.Is(err, settings.ErrNotFound), errors.Is(err, settings.ErrUnknown):
			code = http.StatusNotFound
		case errors.Is(err, settings.ErrConflict):
			code = http.StatusConflict
		}
		fail(w, code, err)
	}
	readBody := func(w http.ResponseWriter, r *http.Request, v any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			fail(w, http.StatusBadRequest, err)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /v1/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.List())
	})
	mux.HandleFunc("GET /v1/settings/{key}", func(w http.ResponseWriter, r *http.Request) {
		res, err := s.System(r.PathValue("key"))
		if err != nil {
			settingsFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("POST /v1/settings/set", func(w http.ResponseWriter, r *http.Request) {
		var in SetSettingRequest
		if !readBody(w, r, &in) {
			return
		}
		if in.Scope == "" {
			in.Scope = settings.ScopeSystem
		}
		ch, err := s.Set(r.Context(), who(r), in.Scope, in.Target, in.Key, in.Value, in.Reason, in.IfVersion)
		if err != nil {
			settingsFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ch)
	})
	mux.HandleFunc("POST /v1/settings/reset", func(w http.ResponseWriter, r *http.Request) {
		var in ResetSettingRequest
		if !readBody(w, r, &in) {
			return
		}
		if in.Scope == "" {
			in.Scope = settings.ScopeSystem
		}
		ch, err := s.Reset(r.Context(), who(r), in.Scope, in.Target, in.Key, in.Reason, in.IfVersion)
		if err != nil {
			settingsFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ch)
	})
	mux.HandleFunc("GET /v1/settings/history", func(w http.ResponseWriter, r *http.Request) {
		limit := 20
		if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
			limit = n
		}
		before := int64(0)
		if n, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil {
			before = n
		}
		writeJSON(w, http.StatusOK, struct {
			Changes []settings.Change `json:"changes"`
		}{Changes: s.History(r.URL.Query().Get("key"), r.URL.Query().Get("scope"), r.URL.Query().Get("target"), before, limit)})
	})
	mux.HandleFunc("POST /v1/settings/revert", func(w http.ResponseWriter, r *http.Request) {
		var in RevertSettingRequest
		if !readBody(w, r, &in) {
			return
		}
		ch, err := s.Revert(r.Context(), who(r), in.Version, in.Reason)
		if err != nil {
			settingsFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ch)
	})
	return mux
}

// ListSettings is every setting, as the socket serves it.
func (c *Client) ListSettings(ctx context.Context) (settings.Listing, error) {
	var out settings.Listing
	return out, c.do(ctx, "GET", "/v1/settings", nil, &out)
}

// GetSetting is one setting at the system level.
func (c *Client) GetSetting(ctx context.Context, key string) (settings.Resolved, error) {
	var out settings.Resolved
	return out, c.do(ctx, "GET", "/v1/settings/"+key, nil, &out)
}

// SetSetting changes one setting.
func (c *Client) SetSetting(ctx context.Context, in SetSettingRequest) (settings.Change, error) {
	var out settings.Change
	return out, c.do(ctx, "POST", "/v1/settings/set", in, &out)
}

// ResetSetting takes a row away.
func (c *Client) ResetSetting(ctx context.Context, in ResetSettingRequest) (settings.Change, error) {
	var out settings.Change
	return out, c.do(ctx, "POST", "/v1/settings/reset", in, &out)
}

// SettingsHistory lists changes, newest first.
func (c *Client) SettingsHistory(ctx context.Context, limit int) ([]settings.Change, error) {
	var out struct {
		Changes []settings.Change `json:"changes"`
	}
	err := c.do(ctx, "GET", "/v1/settings/history?limit="+strconv.Itoa(limit), nil, &out)
	return out.Changes, err
}

// RevertSetting undoes one change.
func (c *Client) RevertSetting(ctx context.Context, in RevertSettingRequest) (settings.Change, error) {
	var out settings.Change
	return out, c.do(ctx, "POST", "/v1/settings/revert", in, &out)
}
