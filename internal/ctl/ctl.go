// Package ctl is the local admin socket: a small HTTP API on a Unix socket in the data directory, so
// that `beacon token …`, `machine …`, `ssh …`, `config …` and `drill …` work against the
// running service, which holds the database lock.
//
// Only processes of the service's own user (or root) can connect: the socket is mode 0600 in the
// data directory, and on Linux and macOS the peer's user id is checked on every connection.
package ctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/version"
)

// Path is where the socket lives in the data directory.
func Path(dataDir string) string { return filepath.Join(dataDir, "ctl.sock") }

// UserHeader carries the name of the person running the command, for the audit log.
const UserHeader = "X-Status-User"

// Info describes the running service.
type Info struct {
	Version string `json:"version"`
}

// TokenRequest creates a token. SSHKeys (authorized_keys lines) sign in to the SSH channel; Machines
// limits the machines it reaches there (none: all).
type TokenRequest struct {
	Name     string   `json:"name"`
	Scopes   []string `json:"scopes"`
	TTL      string   `json:"ttl"` // such as 365d or 720h
	SSHKeys  []string `json:"ssh_keys,omitempty"`
	Machines []string `json:"machines,omitempty"`
}

// TokenResponse carries the new token's secret, shown once.
type TokenResponse struct {
	Secret string      `json:"secret"`
	Token  store.Token `json:"token"`
}

// EnrollRequest gives a machine its gateway token: a probe location, a host, or a heartbeat sender.
// An enrolled machine is enrolled again only with Rotate, which revokes its old token.
type EnrollRequest struct {
	Kind    string `json:"kind"`    // probe, host or heartbeat
	Subject string `json:"subject"` // the location id, host name or sender name
	Rotate  bool   `json:"rotate,omitempty"`
}

// EnrollKinds map what is enrolled to the scope of its token.
var EnrollKinds = map[string]string{"probe": "probe:write", "host": "host:write", "heartbeat": "heartbeat:write"}

// EnrollTTL is how long a machine token lasts. A probe stops being able to write when its machine
// expires (the gateway checks the catalogue), so the token itself may outlive that.
const EnrollTTL = 2 * 365 * 24 * time.Hour

// DrillRequest starts a drill for a while, or ends it ("off").
type DrillRequest struct {
	Duration string `json:"duration"`
}

// DrillState says until when the drill check fails.
type DrillState struct {
	Until time.Time `json:"until,omitzero"`
}

// Service is what the socket serves.
type Service interface {
	CreateToken(name string, scopes []string, ttl time.Duration, by string) (string, store.Token, error)
	CreateSubjectToken(name, subject string, scopes []string, ttl time.Duration, by string) (string, store.Token, error)
	Tokens() []store.Token
	UpdateToken(name string, change func(*store.Token) error) (store.Token, error)
	RevokeToken(name string) error
	Audit(e store.AuditEntry) error
	SetTime(key string, t time.Time) error
	Time(key string) time.Time
}

// DrillKey is where the drill's end is stored.
const DrillKey = "drill"

// MaxDrill is the longest a drill may run.
const MaxDrill = 2 * time.Hour

var userRe = regexp.MustCompile(`^[A-Za-z0-9._@:-]{1,64}$`)

// ParseTTL accepts Go durations and whole days ("365d").
func ParseTTL(s string) (time.Duration, error) {
	if d, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(d)
		if err != nil || n <= 0 {
			return 0, errors.New("days are written like 365d")
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// cliActor is who runs a command, for the audit log: cli:<user>, as the client names the user.
func cliActor(r *http.Request) string {
	name := r.Header.Get(UserHeader)
	if !userRe.MatchString(name) {
		name = "unknown"
	}
	return "cli:" + name
}

// Handler serves the socket's API. active reports whether a probe location exists and its machine has
// not expired; nil accepts any location. m is the SSH channel, nil when it is off.
func Handler(s Service, active func(location string) error, m Machines) http.Handler {
	mux := http.NewServeMux()
	actor := cliActor
	decode := func(w http.ResponseWriter, r *http.Request, v any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			fail(w, http.StatusBadRequest, err)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, Info{Version: version.Version})
	})
	mux.HandleFunc("GET /v1/tokens", func(w http.ResponseWriter, _ *http.Request) {
		var out []TokenInfo
		for _, t := range s.Tokens() {
			out = append(out, info(t))
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		var in TokenRequest
		if !decode(w, r, &in) {
			return
		}
		ttl, err := ParseTTL(in.TTL)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		var keys []store.SSHKey
		for _, line := range in.SSHKeys {
			k, err := ParseSSHKey(line)
			if err != nil {
				fail(w, http.StatusBadRequest, err)
				return
			}
			keys = append(keys, k)
		}
		machines, err := machineNames(in.Machines)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		secret, tok, err := s.CreateToken(in.Name, in.Scopes, ttl, actor(r))
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if len(keys) > 0 || len(machines) > 0 {
			tok, err = s.UpdateToken(tok.Name, func(t *store.Token) error {
				for _, k := range keys {
					addSSHKey(t, k)
				}
				t.Machines = machines
				return nil
			})
			if err != nil {
				fail(w, http.StatusInternalServerError, err)
				return
			}
		}
		_ = s.Audit(store.AuditEntry{Actor: actor(r), Via: "cli", Action: "create_token", Target: tok.Name, Detail: strings.Join(tok.Scopes, ",")})
		writeJSON(w, http.StatusCreated, TokenResponse{Secret: secret, Token: tok})
	})
	mux.HandleFunc("POST /v1/enroll", func(w http.ResponseWriter, r *http.Request) {
		var in EnrollRequest
		if !decode(w, r, &in) {
			return
		}
		scope, ok := EnrollKinds[in.Kind]
		if !ok {
			fail(w, http.StatusBadRequest, errors.New("enroll a probe, a host or a heartbeat"))
			return
		}
		if in.Kind == "probe" && active != nil {
			if err := active(in.Subject); err != nil {
				fail(w, http.StatusBadRequest, err)
				return
			}
		}
		name := in.Kind + "-" + in.Subject
		exists := false
		for _, t := range s.Tokens() {
			exists = exists || t.Name == name
		}
		if exists && !in.Rotate {
			fail(w, http.StatusConflict, fmt.Errorf("%s is enrolled already; enroll it with -rotate to replace its token", name))
			return
		}
		if exists {
			if err := s.RevokeToken(name); err != nil {
				fail(w, http.StatusInternalServerError, err)
				return
			}
		}
		secret, tok, err := s.CreateSubjectToken(name, in.Subject, []string{scope}, EnrollTTL, actor(r))
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		action := "enroll"
		if exists {
			action = "enroll_rotate"
		}
		_ = s.Audit(store.AuditEntry{Actor: actor(r), Via: "cli", Action: action, Target: tok.Name, Detail: scope})
		writeJSON(w, http.StatusCreated, TokenResponse{Secret: secret, Token: tok})
	})
	mux.HandleFunc("DELETE /v1/tokens/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if err := s.RevokeToken(name); err != nil {
			code := http.StatusInternalServerError
			if errors.Is(err, store.ErrNotFound) {
				code = http.StatusNotFound
			}
			fail(w, code, err)
			return
		}
		_ = s.Audit(store.AuditEntry{Actor: actor(r), Via: "cli", Action: "revoke_token", Target: name})
		w.WriteHeader(http.StatusNoContent)
	})
	machineRoutes(mux, s, m, actor, decode)
	mux.HandleFunc("GET /v1/drill", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, DrillState{Until: s.Time(DrillKey)})
	})
	mux.HandleFunc("POST /v1/drill", func(w http.ResponseWriter, r *http.Request) {
		var in DrillRequest
		if !decode(w, r, &in) {
			return
		}
		var until time.Time
		if in.Duration != "off" {
			d, err := time.ParseDuration(in.Duration)
			if err != nil || d <= 0 || d > MaxDrill {
				fail(w, http.StatusBadRequest, errors.New("a drill lasts between 1s and 2h, or is off"))
				return
			}
			until = time.Now().Add(d)
		}
		if err := s.SetTime(DrillKey, until); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		_ = s.Audit(store.AuditEntry{Actor: actor(r), Via: "cli", Action: "drill", Detail: in.Duration})
		writeJSON(w, http.StatusOK, DrillState{Until: until})
	})
	return mux
}
