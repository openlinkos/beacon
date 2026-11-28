package ctl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
)

// Machines is the SSH channel as the socket serves it.
type Machines interface {
	Add(ctx context.Context, who sshgate.Actor, in sshgate.MachineInput) (sshgate.Added, error)
	Remove(ctx context.Context, who sshgate.Actor, name string) error
	List(who sshgate.Actor, address string) (sshgate.Machines, error)
	SetPaused(ctx context.Context, who sshgate.Actor, paused bool) error
	PausedSince() time.Time
	CAPublicKey() string
	HostKey() string
	SetConnector(ctx context.Context, who sshgate.Actor, name, key string) (sshgate.MachineView, error)
	// Recheck closes what is open on the channel for whoever may no longer reach it.
	Recheck() int
}

// ConnectorKeyRequest sets the key a machine behind NAT connects out with (an authorized_keys line).
type ConnectorKeyRequest struct {
	Key string `json:"key"`
}

// SSHKeyRequest gives a token a public key for the SSH channel (an authorized_keys line).
type SSHKeyRequest struct {
	Key string `json:"key"`
}

// MachinesRequest sets the machines a token reaches; empty means all.
type MachinesRequest struct {
	Machines []string `json:"machines"`
}

// SSHState is the channel's state for `beacon ssh status`.
type SSHState struct {
	Enabled          bool      `json:"enabled"`
	PausedSince      time.Time `json:"paused_since,omitzero"`
	HostKey          string    `json:"host_key,omitempty"`
	TrustedUserCAKey string    `json:"trusted_user_ca_key,omitempty"`
}

// PauseRequest pauses or resumes agents' access to machines.
type PauseRequest struct {
	Paused bool `json:"paused"`
}

// ParseSSHKey reads an authorized_keys line into what a token keeps.
func ParseSSHKey(line string) (store.SSHKey, error) { return store.ParseSSHKey(line) }

func machineRoutes(mux *http.ServeMux, s Service, m Machines, actor func(*http.Request) string, decode func(http.ResponseWriter, *http.Request, any) bool) {
	who := func(r *http.Request) sshgate.Actor { return sshgate.Actor{Name: actor(r), Via: "cli"} }
	gateFail := func(w http.ResponseWriter, err error) {
		code := http.StatusBadRequest
		switch {
		case errors.Is(err, sshgate.ErrNoMachine):
			code = http.StatusNotFound
		case errors.Is(err, sshgate.ErrExists):
			code = http.StatusConflict
		case errors.Is(err, sshgate.ErrUnreachable):
			code = http.StatusBadGateway
		}
		fail(w, code, err)
	}
	off := func(w http.ResponseWriter) bool {
		if m == nil {
			fail(w, http.StatusServiceUnavailable, errors.New("the SSH channel is off: set ssh.ca_key_file and ssh.host_key_file in the configuration"))
			return true
		}
		return false
	}
	mux.HandleFunc("POST /v1/tokens/{name}/ssh-keys", func(w http.ResponseWriter, r *http.Request) {
		var in SSHKeyRequest
		if !decode(w, r, &in) {
			return
		}
		k, err := ParseSSHKey(in.Key)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		// The key and its record are one write: a key is never added without it.
		tok, err := s.AddTokenSSHKeyRecorded(r.PathValue("name"), k, func(_, t store.Token) []store.AuditEntry {
			return []store.AuditEntry{{Actor: actor(r), Via: "cli", Action: "token_ssh_key_add", Target: t.Name, Detail: k.Fingerprint}}
		})
		if err != nil {
			code := http.StatusBadRequest
			switch {
			case errors.Is(err, store.ErrUnrecorded):
				code, err = http.StatusServiceUnavailable, errUnrecorded
			case errors.Is(err, store.ErrNotFound):
				code = http.StatusNotFound
			case errors.Is(err, store.ErrExists):
				code = http.StatusConflict
			}
			fail(w, code, err)
			return
		}
		writeJSON(w, http.StatusOK, info(tok))
	})
	mux.HandleFunc("DELETE /v1/tokens/{name}/ssh-keys", func(w http.ResponseWriter, r *http.Request) {
		fp := r.URL.Query().Get("fingerprint")
		tok, err := s.UpdateToken(r.PathValue("name"), func(t *store.Token) error {
			n := len(t.SSHKeys)
			t.SSHKeys = slices.DeleteFunc(t.SSHKeys, func(x store.SSHKey) bool { return x.Fingerprint == fp })
			if len(t.SSHKeys) == n {
				return fmt.Errorf("the token has no key %s", fp)
			}
			return nil
		})
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		_ = s.Audit(store.AuditEntry{Actor: actor(r), Via: "cli", Action: "token_ssh_key_remove", Target: tok.Name, Detail: fp})
		if m != nil {
			m.Recheck()
		}
		writeJSON(w, http.StatusOK, info(tok))
	})
	mux.HandleFunc("PUT /v1/tokens/{name}/machines", func(w http.ResponseWriter, r *http.Request) {
		var in MachinesRequest
		if !decode(w, r, &in) {
			return
		}
		// A change that reaches more is one write with its record, or not made; one that reaches less is
		// recorded afterwards, and goes ahead whatever the audit log says.
		var before string
		critical, widened := s.CriticalMachines(), false
		entry := func(t store.Token) store.AuditEntry {
			return store.AuditEntry{Actor: actor(r), Via: "cli", Action: "token_machines", Target: t.Name, Detail: before + " -> " + t.String()}
		}
		tok, err := s.UpdateTokenRecorded(r.PathValue("name"), func(t *store.Token) error {
			if t.Agent != "" {
				return fmt.Errorf("token %s is agent %s's: set the agent's machines with beacon agent machines", t.Name, t.Agent)
			}
			access, err := machineAccess(s, in.Machines, t.MachineAccess)
			if err != nil {
				return err
			}
			before, t.MachineAccess = t.String(), access
			return nil
		}, func(was, t store.Token) []store.AuditEntry {
			if widened = store.TokenAccessWidens(was.MachineAccess, t.MachineAccess, critical, time.Now()); !widened {
				return nil
			}
			return []store.AuditEntry{entry(t)}
		})
		switch {
		case errors.Is(err, store.ErrUnrecorded):
			fail(w, http.StatusServiceUnavailable, errUnrecorded)
			return
		case err != nil:
			fail(w, http.StatusBadRequest, err)
			return
		}
		if !widened {
			_ = s.Audit(entry(tok))
		}
		if m != nil {
			m.Recheck()
		}
		writeJSON(w, http.StatusOK, info(tok))
	})
	mux.HandleFunc("GET /v1/machines", func(w http.ResponseWriter, r *http.Request) {
		if off(w) {
			return
		}
		l, err := m.List(who(r), "")
		if err != nil {
			gateFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, l)
	})
	mux.HandleFunc("POST /v1/machines", func(w http.ResponseWriter, r *http.Request) {
		if off(w) {
			return
		}
		var in sshgate.MachineInput
		if !decode(w, r, &in) {
			return
		}
		a, err := m.Add(r.Context(), who(r), in)
		if err != nil {
			gateFail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, a)
	})
	mux.HandleFunc("DELETE /v1/machines/{name}", func(w http.ResponseWriter, r *http.Request) {
		if off(w) {
			return
		}
		if err := m.Remove(r.Context(), who(r), r.PathValue("name")); err != nil {
			gateFail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /v1/machines/{name}/connector", func(w http.ResponseWriter, r *http.Request) {
		if off(w) {
			return
		}
		var in ConnectorKeyRequest
		if !decode(w, r, &in) {
			return
		}
		v, err := m.SetConnector(r.Context(), who(r), r.PathValue("name"), in.Key)
		if err != nil {
			gateFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
	state := func(w http.ResponseWriter) {
		if m == nil {
			writeJSON(w, http.StatusOK, SSHState{})
			return
		}
		writeJSON(w, http.StatusOK, SSHState{Enabled: true, PausedSince: m.PausedSince(), HostKey: m.HostKey(), TrustedUserCAKey: m.CAPublicKey()})
	}
	mux.HandleFunc("GET /v1/ssh", func(w http.ResponseWriter, _ *http.Request) { state(w) })
	mux.HandleFunc("POST /v1/ssh", func(w http.ResponseWriter, r *http.Request) {
		if off(w) {
			return
		}
		var in PauseRequest
		if !decode(w, r, &in) {
			return
		}
		if err := m.SetPaused(r.Context(), who(r), in.Paused); err != nil {
			gateFail(w, err)
			return
		}
		state(w)
	})
}

// info is a token as the socket shows it.
func info(t store.Token) TokenInfo {
	ti := TokenInfo{Name: t.Name, Scopes: t.Scopes, Subject: t.Subject, Created: t.Created, CreatedBy: t.CreatedBy, Expires: t.Expires,
		Lifetime: store.FormatTTL(t.Lifetime), Renewed: t.Renewed, Agent: t.Agent}
	if t.Agent == "" {
		ti.MachineAccessView = bridge.AccessView(t.MachineAccess)
	}
	for _, k := range t.SSHKeys {
		ti.SSHKeys = append(ti.SSHKeys, k.Fingerprint)
	}
	return ti
}

// AddSSHKey gives a token a public key for the SSH channel.
func (c *Client) AddSSHKey(ctx context.Context, token, key string) (TokenInfo, error) {
	var out TokenInfo
	err := c.do(ctx, http.MethodPost, "/v1/tokens/"+token+"/ssh-keys", SSHKeyRequest{Key: key}, &out)
	return out, err
}

// RemoveSSHKey takes a key, by its fingerprint, from a token.
func (c *Client) RemoveSSHKey(ctx context.Context, token, fingerprint string) (TokenInfo, error) {
	var out TokenInfo
	err := c.do(ctx, http.MethodDelete, "/v1/tokens/"+token+"/ssh-keys?fingerprint="+urlQuery(fingerprint), nil, &out)
	return out, err
}

// SetTokenMachines sets the machines a token reaches, as the command line writes them
// (store.ParseMachineList): all, or names; none when empty.
func (c *Client) SetTokenMachines(ctx context.Context, token string, machines []string) (TokenInfo, error) {
	var out TokenInfo
	err := c.do(ctx, http.MethodPut, "/v1/tokens/"+token+"/machines", MachinesRequest{Machines: machines}, &out)
	return out, err
}

// Machines lists the machines.
func (c *Client) Machines(ctx context.Context) (sshgate.Machines, error) {
	var out sshgate.Machines
	err := c.do(ctx, http.MethodGet, "/v1/machines", nil, &out)
	return out, err
}

// AddMachine adds a machine, pinning its host key.
func (c *Client) AddMachine(ctx context.Context, in sshgate.MachineInput) (sshgate.Added, error) {
	var out sshgate.Added
	err := c.do(ctx, http.MethodPost, "/v1/machines", in, &out)
	return out, err
}

// RemoveMachine forgets a machine.
func (c *Client) RemoveMachine(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/machines/"+name, nil, nil)
}

// SetMachineConnector sets or changes the key a machine behind NAT connects out with.
func (c *Client) SetMachineConnector(ctx context.Context, name, key string) (sshgate.MachineView, error) {
	var out sshgate.MachineView
	err := c.do(ctx, http.MethodPut, "/v1/machines/"+name+"/connector", ConnectorKeyRequest{Key: key}, &out)
	return out, err
}

// SSH reports the channel's state.
func (c *Client) SSH(ctx context.Context) (SSHState, error) {
	var out SSHState
	err := c.do(ctx, http.MethodGet, "/v1/ssh", nil, &out)
	return out, err
}

// PauseSSH pauses or resumes agents' access to machines.
func (c *Client) PauseSSH(ctx context.Context, paused bool) (SSHState, error) {
	var out SSHState
	err := c.do(ctx, http.MethodPost, "/v1/ssh", PauseRequest{Paused: paused}, &out)
	return out, err
}
