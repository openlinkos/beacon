package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/sshgate"
)

// The SSH channel's routes: machines, the tools that work on them, sessions, the pause switch.

func gateActor(who bridge.Actor) sshgate.Actor {
	return sshgate.Actor{Name: who.Name, Via: who.Via, Key: who.Key, Cred: who.Cred}
}

// gateError maps the channel's errors to HTTP answers.
func (s *Server) gateError(w http.ResponseWriter, err error) {
	for _, e := range []struct {
		err    error
		status int
		code   string
	}{
		{sshgate.ErrDisabled, http.StatusServiceUnavailable, "ssh.disabled"},
		{sshgate.ErrPaused, http.StatusConflict, "ssh.paused"},
		{sshgate.ErrStandby, http.StatusServiceUnavailable, "ssh.standby"},
		{sshgate.ErrNoMachine, http.StatusNotFound, "machine.not_found"},
		{sshgate.ErrForbidden, http.StatusForbidden, "machine.forbidden"},
		{sshgate.ErrUnreachable, http.StatusBadGateway, "machine.unreachable"},
		{sshgate.ErrExists, http.StatusConflict, "machine.exists"},
		{sshgate.ErrNoFile, http.StatusNotFound, "file.not_found"},
		{sshgate.ErrNotRegular, http.StatusBadRequest, "file.not_regular"},
		{sshgate.ErrBinary, http.StatusUnsupportedMediaType, "file.binary"},
		{sshgate.ErrTooLarge, http.StatusRequestEntityTooLarge, "file.too_large"},
		{sshgate.ErrChanged, http.StatusConflict, "file.changed"},
		{sshgate.ErrNoMatch, http.StatusUnprocessableEntity, "edit.no_match"},
		{sshgate.ErrAmbiguous, http.StatusUnprocessableEntity, "edit.ambiguous"},
		{sshgate.ErrNoSession, http.StatusNotFound, "session.not_found"},
		{sshgate.ErrBadInput, http.StatusBadRequest, "bad_input"},
		{sshgate.ErrDisconnected, http.StatusBadGateway, "machine.disconnected"},
	} {
		if errors.Is(err, e.err) {
			writeError(w, e.status, e.code, err.Error())
			return
		}
	}
	s.Log.Error("ssh channel operation failed", "err", err)
	writeError(w, http.StatusBadGateway, "upstream", err.Error())
}

// readBody is readJSON with a larger limit, for file contents.
func readBody(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return readJSON(w, r, v)
}

func (s *Server) sshState(w http.ResponseWriter, _ *http.Request, _ bridge.Actor) {
	out := map[string]any{"enabled": s.SSH != nil}
	if s.SSH != nil {
		out["paused"] = s.SSH.Paused()
		if t := s.SSH.PausedSince(); !t.IsZero() {
			out["paused_since"] = t
		}
		out["address"] = s.SSHAddress
		out["host_key"] = s.SSH.HostKey()
		out["trusted_user_ca_key"] = s.SSH.CAPublicKey()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) sshPause(paused bool) handler {
	return func(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
		if err := s.SSH.SetPaused(r.Context(), gateActor(who), paused); err != nil {
			s.gateError(w, err)
			return
		}
		s.sshState(w, r, who)
	}
}

func (s *Server) listMachines(w http.ResponseWriter, _ *http.Request, who bridge.Actor) {
	l, err := s.SSH.List(gateActor(who), s.SSHAddress)
	if err != nil {
		s.gateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) addMachine(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in sshgate.MachineInput
	if !readJSON(w, r, &in) {
		return
	}
	a, err := s.SSH.Add(r.Context(), gateActor(who), in)
	if err != nil {
		s.gateError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) removeMachine(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	if err := s.SSH.Remove(r.Context(), gateActor(who), r.PathValue("name")); err != nil {
		s.gateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setConnector sets or changes the key a machine behind NAT connects out to the channel with.
func (s *Server) setConnector(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in struct {
		Key string `json:"key"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	v, err := s.SSH.SetConnector(r.Context(), gateActor(who), r.PathValue("name"), in.Key)
	if err != nil {
		s.gateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) runCommand(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in sshgate.RunInput
	if !readBody(w, r, &in, 2<<20) {
		return
	}
	out, err := s.SSH.Run(r.Context(), gateActor(who), r.PathValue("name"), in)
	if err != nil {
		s.gateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	q := r.URL.Query()
	num := func(k string) (int, bool) {
		v := q.Get(k)
		if v == "" {
			return 0, true
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "bad_input", k+" is a whole number")
			return 0, false
		}
		return n, true
	}
	offset, ok1 := num("offset")
	if !ok1 {
		return
	}
	limit, ok2 := num("limit")
	if !ok2 {
		return
	}
	v, err := s.SSH.ReadFile(r.Context(), gateActor(who), r.PathValue("name"), q.Get("path"), offset, limit)
	if err != nil {
		s.gateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) writeFile(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !readBody(w, r, &in, 2<<20) {
		return
	}
	out, err := s.SSH.WriteFile(r.Context(), gateActor(who), r.PathValue("name"), in.Path, in.Content)
	if err != nil {
		s.gateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) editFile(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in sshgate.EditInput
	if !readBody(w, r, &in, 2<<20) {
		return
	}
	out, err := s.SSH.EditFile(r.Context(), gateActor(who), r.PathValue("name"), in)
	if err != nil {
		s.gateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, "bad_input", "limit is 1 to 1000")
			return
		}
		limit = n
	}
	ss, err := s.SSH.Sessions(sshgate.SessionFilter{Machine: q.Get("machine"), Agent: q.Get("agent"), Limit: limit})
	if err != nil {
		s.gateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": ss})
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	t, err := s.SSH.Session(r.PathValue("id"))
	if err != nil {
		s.gateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) getRecording(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	rc, err := s.SSH.Recording(r.PathValue("id"))
	if err != nil {
		s.gateError(w, err)
		return
	}
	defer func() { _ = rc.Close() }()
	w.Header().Set("Content-Type", "application/x-asciicast")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="`+r.PathValue("id")+`.cast"`)
	_, _ = io.Copy(w, rc)
}
