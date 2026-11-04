package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// People is the bridge as the socket serves it for `beacon setup`, `beacon user …`, `beacon agent …`
// and `beacon token claim`: the methods the console, the API and MCP call, so that what the command line
// does is audited and echoed like theirs. The command line on the host may do everything an owner may.
type People interface {
	SetupLink(via string) (bridge.LinkView, error)
	Users(who bridge.Actor) ([]bridge.UserView, []bridge.LinkView, error)
	Invite(ctx context.Context, who bridge.Actor, role, note string) (bridge.LinkView, error)
	UpdateUser(ctx context.Context, who bridge.Actor, username string, ch bridge.UserChange) (bridge.UserView, error)
	ResetLink(ctx context.Context, who bridge.Actor, username string) (bridge.LinkView, error)
	Agents(who bridge.Actor, owner string) ([]bridge.AgentView, error)
	CreateAgent(ctx context.Context, who bridge.Actor, in bridge.AgentInput) (bridge.AgentCreated, error)
	DeleteAgent(ctx context.Context, who bridge.Actor, name string) error
	ClaimToken(ctx context.Context, who bridge.Actor, token, owner, agent, preset string) (bridge.AgentView, error)
}

// InviteRequest invites someone with a role.
type InviteRequest struct {
	Role string `json:"role"`
	Note string `json:"note,omitempty"`
}

// AgentRequest makes an agent for Owner with a token lasting TTL (365d, 720h).
type AgentRequest struct {
	Name     string   `json:"name"`
	Owner    string   `json:"owner"`
	Preset   string   `json:"preset,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
	Machines []string `json:"machines,omitempty"`
	SSHKeys  []string `json:"ssh_keys,omitempty"`
	TTL      string   `json:"ttl"`
}

// ClaimRequest makes a legacy token an agent of Owner.
type ClaimRequest struct {
	Token  string `json:"token"`
	Owner  string `json:"owner"`
	Agent  string `json:"agent,omitempty"`
	Preset string `json:"preset,omitempty"`
}

// UsersResponse lists the people and the invitations not yet used.
type UsersResponse struct {
	Users       []bridge.UserView `json:"users"`
	Invitations []bridge.LinkView `json:"invitations"`
}

// PeopleHandler serves /v1/setup, /v1/users, /v1/agents and /v1/claims, beside Handler.
func PeopleHandler(p People) http.Handler {
	mux := http.NewServeMux()
	who := func(r *http.Request) bridge.Actor { return bridge.Actor{Name: cliActor(r), Via: "cli"} }
	decode := func(w http.ResponseWriter, r *http.Request, v any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			fail(w, http.StatusBadRequest, err)
			return false
		}
		return true
	}
	peopleFail := func(w http.ResponseWriter, err error) {
		code := http.StatusInternalServerError
		switch {
		case errors.Is(err, store.ErrNotFound):
			code = http.StatusNotFound
		case errors.Is(err, store.ErrExists), errors.Is(err, store.ErrHasAccounts), errors.Is(err, store.ErrLastOwner):
			code = http.StatusConflict
		case errors.Is(err, bridge.ErrBadInput), errors.Is(err, bridge.ErrForbidden):
			code = http.StatusBadRequest
		}
		fail(w, code, err)
	}
	mux.HandleFunc("POST /v1/setup", func(w http.ResponseWriter, _ *http.Request) {
		l, err := p.SetupLink("cli")
		if err != nil {
			peopleFail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, l)
	})
	mux.HandleFunc("GET /v1/users", func(w http.ResponseWriter, r *http.Request) {
		users, invs, err := p.Users(who(r))
		if err != nil {
			peopleFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, UsersResponse{Users: users, Invitations: invs})
	})
	mux.HandleFunc("POST /v1/users/invitations", func(w http.ResponseWriter, r *http.Request) {
		var in InviteRequest
		if !decode(w, r, &in) {
			return
		}
		l, err := p.Invite(r.Context(), who(r), in.Role, in.Note)
		if err != nil {
			peopleFail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, l)
	})
	mux.HandleFunc("PATCH /v1/users/{name}", func(w http.ResponseWriter, r *http.Request) {
		var in bridge.UserChange
		if !decode(w, r, &in) {
			return
		}
		u, err := p.UpdateUser(r.Context(), who(r), r.PathValue("name"), in)
		if err != nil {
			peopleFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, u)
	})
	mux.HandleFunc("POST /v1/users/{name}/reset", func(w http.ResponseWriter, r *http.Request) {
		l, err := p.ResetLink(r.Context(), who(r), r.PathValue("name"))
		if err != nil {
			peopleFail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, l)
	})
	mux.HandleFunc("GET /v1/agents", func(w http.ResponseWriter, r *http.Request) {
		list, err := p.Agents(who(r), r.URL.Query().Get("owner"))
		if err != nil {
			peopleFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("POST /v1/agents", func(w http.ResponseWriter, r *http.Request) {
		var in AgentRequest
		if !decode(w, r, &in) {
			return
		}
		ttl, err := ParseTTL(in.TTL)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		machines, err := machineNames(in.Machines)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		out, err := p.CreateAgent(r.Context(), who(r), bridge.AgentInput{Name: in.Name, Owner: in.Owner, Preset: in.Preset, Scopes: in.Scopes,
			Machines: machines, SSHKeys: in.SSHKeys, TTL: ttl})
		if err != nil {
			peopleFail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	})
	mux.HandleFunc("DELETE /v1/agents/{name}", func(w http.ResponseWriter, r *http.Request) {
		if err := p.DeleteAgent(r.Context(), who(r), r.PathValue("name")); err != nil {
			peopleFail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/claims", func(w http.ResponseWriter, r *http.Request) {
		var in ClaimRequest
		if !decode(w, r, &in) {
			return
		}
		a, err := p.ClaimToken(r.Context(), who(r), in.Token, in.Owner, in.Agent, in.Preset)
		if err != nil {
			peopleFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a)
	})
	return mux
}

// SetupLink prints the one-time link that creates the first account.
func (c *Client) SetupLink(ctx context.Context) (bridge.LinkView, error) {
	var out bridge.LinkView
	err := c.do(ctx, http.MethodPost, "/v1/setup", nil, &out)
	return out, err
}

// Users lists the people and the invitations not yet used.
func (c *Client) Users(ctx context.Context) (UsersResponse, error) {
	var out UsersResponse
	err := c.do(ctx, http.MethodGet, "/v1/users", nil, &out)
	return out, err
}

// Invite makes an invitation.
func (c *Client) Invite(ctx context.Context, in InviteRequest) (bridge.LinkView, error) {
	var out bridge.LinkView
	err := c.do(ctx, http.MethodPost, "/v1/users/invitations", in, &out)
	return out, err
}

// UpdateUser changes someone's role, or disables or enables them.
func (c *Client) UpdateUser(ctx context.Context, name string, ch bridge.UserChange) (bridge.UserView, error) {
	var out bridge.UserView
	err := c.do(ctx, http.MethodPatch, "/v1/users/"+url.PathEscape(name), ch, &out)
	return out, err
}

// ResetUser makes a password reset link.
func (c *Client) ResetUser(ctx context.Context, name string) (bridge.LinkView, error) {
	var out bridge.LinkView
	err := c.do(ctx, http.MethodPost, "/v1/users/"+url.PathEscape(name)+"/reset", nil, &out)
	return out, err
}

// Agents lists the agents.
func (c *Client) Agents(ctx context.Context) ([]bridge.AgentView, error) {
	var out []bridge.AgentView
	err := c.do(ctx, http.MethodGet, "/v1/agents", nil, &out)
	return out, err
}

// AddAgent makes an agent and its first token.
func (c *Client) AddAgent(ctx context.Context, in AgentRequest) (bridge.AgentCreated, error) {
	var out bridge.AgentCreated
	err := c.do(ctx, http.MethodPost, "/v1/agents", in, &out)
	return out, err
}

// RemoveAgent deletes an agent with its credentials.
func (c *Client) RemoveAgent(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/agents/"+url.PathEscape(name), nil, nil)
}

// Claim makes a legacy token an agent of a person.
func (c *Client) Claim(ctx context.Context, in ClaimRequest) (bridge.AgentView, error) {
	var out bridge.AgentView
	err := c.do(ctx, http.MethodPost, "/v1/claims", in, &out)
	return out, err
}
