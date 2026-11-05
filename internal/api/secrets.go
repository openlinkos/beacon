package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/openlinkos/beacon/internal/scrub"
	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/store"
)

// The grants' routes: the agents' door (search, request, get, grants, renew, release) and the
// people's (entitlements, agent tags, the requests and their decisions, revocation, the scope
// previews). Agents reach items only through their grants; people manage the policy around them.
// Every route resolves the principal afresh, and the bridge methods carry the audit and the chat
// echo, as everywhere in Beacon.

func secretsRoutes() []string {
	return []string{
		"GET /api/v1/secrets/search", "POST /api/v1/secrets/request", "GET /api/v1/secrets/requests",
		"GET /api/v1/secrets/requests/{id}", "POST /api/v1/secrets/requests/{id}/decide",
		"POST /api/v1/secrets/requests/{id}/withdraw",
		"POST /api/v1/secrets/get", "GET /api/v1/secrets/grants", "POST /api/v1/secrets/grants/{id}/renew",
		"POST /api/v1/secrets/grants/{id}/release", "POST /api/v1/secrets/grants/{id}/revoke",
		"GET /api/v1/secrets/entitlements", "POST /api/v1/secrets/entitlements", "DELETE /api/v1/secrets/entitlements/{name}",
		"POST /api/v1/secrets/agent-tags", "GET /api/v1/secrets/agents/{name}/scope", "POST /api/v1/secrets/agents/{name}/templates",
		"POST /api/v1/secrets/agents/{name}/unsuspend",
		"POST /api/v1/secrets/use", "POST /api/v1/secrets/http-request",
	}
}

func init() { Routes = append(Routes, secretsRoutes()...) }

// SecretsAPI is what the grants' routes need of the vault (nil: no routes), so tests can stand in.
type SecretsAPI interface {
	SearchForAgent(agent, query, collection string, tags []string) []secrets.AgentSearchResult
	RequestSecret(agent, via string, in secrets.RequestIn) (secrets.RequestOutcome, error)
	Request(id string) (secrets.Request, bool)
	Requests(state string) []secrets.Request
	Withdraw(agent, id, via string) error
	Decide(who secrets.DecideActor, requestID string, approve bool, note string, uses, duration *int64) (secrets.Request, secrets.Grant, error)
	GetSecret(agent, via, ref, field, reason, alert, context, ip, client string) (secrets.UseResult, error)
	GrantsOf(agent string) []secrets.Grant
	AllGrants(state string) []secrets.Grant
	RenewGrant(agent, id, reason, via string) (secrets.Grant, *secrets.Request, error)
	ReleaseGrant(agent, id, via string) error
	RevokeGrant(who secrets.Actor, id, reason string) error
	Entitlements() []secrets.Entitlement
	SetEntitlement(who secrets.Actor, e secrets.Entitlement) (secrets.Entitlement, error)
	DeleteEntitlement(who secrets.Actor, name string) error
	TagsOfAgent(agent string) []string
	SetAgentTags(who secrets.Actor, agent string, tags []string) error
	PreviewForAgent(agent string) secrets.ScopePreview
	PreviewForTemplates(names []string) secrets.ScopePreview
	EntitlementsOfAgent(agent string) []secrets.Entitlement
	TemplateEntitlements() []secrets.Entitlement
	ApplyTemplates(who secrets.Actor, agent string, names []string) error
	SuspendedAgents() map[string]string
	UnsuspendAgent(who secrets.Actor, agent string) error
	AgentOwner(name string) (string, bool)
	UseForMachine(agentName, machine string, asks []secrets.UseAsk, reason, ip, client string) (map[string]string, *scrub.Scrubber, error)
	UseForProcess(agentName, via string, refs map[string]string, reason, ip, client string) (map[string]string, error)
	HTTPRequest(agentName, via string, in secrets.HTTPRequestIn, ip, client string) (secrets.HTTPResponse, error)
}

// registerSecrets adds the grants' routes.
func (s *Server) registerSecrets(mux *http.ServeMux) {
	if s.Secrets == nil {
		return
	}
	mux.Handle("GET /api/v1/secrets/search", s.withPrincipal("secrets:describe", s.searchSecrets))
	mux.Handle("POST /api/v1/secrets/request", s.withPrincipal("secrets:use", s.postSecretRequest))
	mux.Handle("GET /api/v1/secrets/requests", s.withPrincipal("secrets:describe", s.listSecretRequests))
	mux.Handle("GET /api/v1/secrets/requests/{id}", s.withPrincipal("secrets:describe", s.getSecretRequest))
	mux.Handle("POST /api/v1/secrets/requests/{id}/decide", s.withPrincipal("", s.decideSecretRequest))
	mux.Handle("POST /api/v1/secrets/requests/{id}/withdraw", s.withPrincipal("secrets:use", s.withdrawSecretRequest))
	mux.Handle("POST /api/v1/secrets/get", s.withPrincipal("secrets:read", s.postGetSecret))
	mux.Handle("GET /api/v1/secrets/grants", s.withPrincipal("secrets:describe", s.listSecretGrants))
	mux.Handle("POST /api/v1/secrets/grants/{id}/renew", s.withPrincipal("secrets:use", s.renewSecretGrant))
	mux.Handle("POST /api/v1/secrets/grants/{id}/release", s.withPrincipal("secrets:use", s.releaseSecretGrant))
	mux.Handle("POST /api/v1/secrets/grants/{id}/revoke", s.withPrincipal("secrets:admin", s.revokeSecretGrant))
	mux.Handle("GET /api/v1/secrets/entitlements", s.withPrincipal("secrets:admin", s.listEntitlements))
	mux.Handle("POST /api/v1/secrets/entitlements", s.withPrincipal("secrets:admin", s.postEntitlement))
	mux.Handle("DELETE /api/v1/secrets/entitlements/{name}", s.withPrincipal("secrets:admin", s.deleteEntitlement))
	mux.Handle("POST /api/v1/secrets/agent-tags", s.withPrincipal("secrets:admin", s.postAgentTags))
	mux.Handle("GET /api/v1/secrets/agents/{name}/scope", s.withPrincipal("", s.agentSecretScope))
	mux.Handle("POST /api/v1/secrets/agents/{name}/templates", s.withPrincipal("", s.postAgentTemplates))
	mux.Handle("POST /api/v1/secrets/agents/{name}/unsuspend", s.withPrincipal("secrets:admin", s.unsuspendAgent))
	mux.Handle("POST /api/v1/secrets/use", s.withPrincipal("secrets:use", s.postSecretUse))
	mux.Handle("POST /api/v1/secrets/http-request", s.withPrincipal("secrets:use", s.postSecretHTTPRequest))
}

// agentOf maps the principal to the agent's name and its owner; ok false when the caller is not an
// agent.
func agentOf(p store.Principal) (string, bool) {
	if p.Agent == "" {
		return "", false
	}
	return p.Agent, true
}

// searchSecrets serves both doors: an agent sees what its entitlements reach, a person what their
// permissions describe.
func (s *Server) searchSecrets(w http.ResponseWriter, r *http.Request, p store.Principal) {
	q := r.URL.Query()
	tags := splitTags(q["tag"])
	if name, ok := agentOf(p); ok {
		out := s.Secrets.SearchForAgent(name, q.Get("q"), q.Get("collection"), tags)
		if out == nil {
			out = []secrets.AgentSearchResult{}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if s.Vault == nil {
		writeError(w, http.StatusForbidden, "secrets.agent", "agents reach items through grants, not this door: request_secret asks, get_secret takes")
		return
	}
	who, ok := s.vaultActor(p)
	if !ok {
		writeError(w, http.StatusForbidden, "secrets.agent", "agents reach items through grants, not this door: request_secret asks, get_secret takes")
		return
	}
	out := s.Vault.Search(who, q.Get("q"), q.Get("collection"), tags)
	if out == nil {
		out = []secrets.SearchResult{}
	}
	writeJSON(w, http.StatusOK, out)
}

// postSecretRequest is request_secret: the agent's ask, split by the level each item needs.
func (s *Server) postSecretRequest(w http.ResponseWriter, r *http.Request, p store.Principal) {
	name, ok := agentOf(p)
	if !ok {
		writeError(w, http.StatusForbidden, "secrets.agent", "only an agent asks for a grant; people view and manage in the console")
		return
	}
	var in secrets.RequestIn
	if !readBody(w, r, &in, 1<<20) {
		return
	}
	out, err := s.Secrets.RequestSecret(name, "api", in)
	if err != nil {
		s.vaultError2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getSecretRequest(w http.ResponseWriter, r *http.Request, p store.Principal) {
	req, ok := s.Secrets.Request(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "secrets.not_found", "no such request")
		return
	}
	if name, isAgent := agentOf(p); isAgent && req.Agent != name {
		writeError(w, http.StatusNotFound, "secrets.not_found", "no such request") // not the agent's own: not there
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (s *Server) listSecretRequests(w http.ResponseWriter, r *http.Request, p store.Principal) {
	state := r.URL.Query().Get("status")
	if name, ok := agentOf(p); ok {
		var out []secrets.Request
		for _, req := range s.Secrets.Requests(state) {
			if req.Agent == name {
				out = append(out, req)
			}
		}
		if out == nil {
			out = []secrets.Request{}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if !p.Person() {
		writeError(w, http.StatusForbidden, "secrets.agent", "agents reach items through grants, not this door: request_secret asks, get_secret takes")
		return
	}
	out := s.Secrets.Requests(state)
	if out == nil {
		out = []secrets.Request{}
	}
	writeJSON(w, http.StatusOK, out)
}

// decideIn is what a decision takes: the decision, its reason, and the uses and duration it may
// set (inside the ceilings).
type decideIn struct {
	Approve  bool   `json:"approve"`
	Note     string `json:"note"`
	Uses     *int64 `json:"uses,omitempty"`
	Duration *int64 `json:"duration,omitempty"` // seconds; 0: no expiry
}

func (s *Server) decideSecretRequest(w http.ResponseWriter, r *http.Request, p store.Principal) {
	var in decideIn
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	who := secrets.DecideActor{Via: "api"}
	switch {
	case p.Agent != "":
		who.Name, who.Kind = p.Agent, "agent"
	case p.Person():
		who.Name, who.Kind, who.Role = p.User, "person", s.roleOfPrincipal(p)
	default:
		writeError(w, http.StatusForbidden, "secrets.agent", "a credential that is neither a person nor an agent cannot decide")
		return
	}
	req, grant, err := s.Secrets.Decide(who, r.PathValue("id"), in.Approve, in.Note, in.Uses, in.Duration)
	if err != nil {
		s.vaultError2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"request": req, "grant": grant})
}

func (s *Server) withdrawSecretRequest(w http.ResponseWriter, r *http.Request, p store.Principal) {
	name, ok := agentOf(p)
	if !ok {
		writeError(w, http.StatusForbidden, "secrets.agent", "only the agent that asked may withdraw")
		return
	}
	if err := s.Secrets.Withdraw(name, r.PathValue("id"), "api"); err != nil {
		s.vaultError2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"withdrawn": true})
}

// getIn is get_secret's body: the reference, the field, the reason, and the context when the
// reason is optional.
type getIn struct {
	Ref     string `json:"ref"`
	Field   string `json:"field,omitempty"`
	Reason  string `json:"reason"`
	Alert   string `json:"alert,omitempty"`
	Context string `json:"context,omitempty"`
}

func (s *Server) postGetSecret(w http.ResponseWriter, r *http.Request, p store.Principal) {
	name, ok := agentOf(p)
	if !ok {
		writeError(w, http.StatusForbidden, "secrets.agent", "people view values in the console (the reveal), agents take them through grants")
		return
	}
	var in getIn
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	ip, client := r.Header.Get("X-Forwarded-For"), r.Header.Get("User-Agent")
	out, err := s.Secrets.GetSecret(name, "api", in.Ref, in.Field, in.Reason, in.Alert, in.Context, ip, client)
	if err != nil {
		s.vaultError2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listSecretGrants(w http.ResponseWriter, r *http.Request, p store.Principal) {
	if name, ok := agentOf(p); ok {
		out := s.Secrets.GrantsOf(name)
		if out == nil {
			out = []secrets.Grant{}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if !p.Person() {
		writeError(w, http.StatusForbidden, "secrets.agent", "agents reach items through grants, not this door: request_secret asks, get_secret takes")
		return
	}
	out := s.Secrets.AllGrants(r.URL.Query().Get("state"))
	if out == nil {
		out = []secrets.Grant{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) renewSecretGrant(w http.ResponseWriter, r *http.Request, p store.Principal) {
	name, ok := agentOf(p)
	if !ok {
		writeError(w, http.StatusForbidden, "secrets.agent", "only the agent that holds a grant renews it")
		return
	}
	var in struct {
		Reason string `json:"reason,omitempty"`
	}
	_ = readBody(w, r, &in, 1<<16)
	grant, pending, err := s.Secrets.RenewGrant(name, r.PathValue("id"), in.Reason, "api")
	if err != nil {
		s.vaultError2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"grant": grant, "pending": pending})
}

func (s *Server) releaseSecretGrant(w http.ResponseWriter, r *http.Request, p store.Principal) {
	name, ok := agentOf(p)
	if !ok {
		writeError(w, http.StatusForbidden, "secrets.agent", "only the agent that holds a grant releases it")
		return
	}
	if err := s.Secrets.ReleaseGrant(name, r.PathValue("id"), "api"); err != nil {
		s.vaultError2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"released": true})
}

func (s *Server) revokeSecretGrant(w http.ResponseWriter, r *http.Request, p store.Principal) {
	var in struct {
		Reason string `json:"reason,omitempty"`
	}
	_ = readBody(w, r, &in, 1<<16)
	who, ok := s.vaultPerson(w, p)
	if !ok {
		return
	}
	if err := s.Secrets.RevokeGrant(who, r.PathValue("id"), in.Reason); err != nil {
		s.vaultError2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

func (s *Server) listEntitlements(w http.ResponseWriter, _ *http.Request, _ store.Principal) {
	out := s.Secrets.Entitlements()
	if out == nil {
		out = []secrets.Entitlement{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) postEntitlement(w http.ResponseWriter, r *http.Request, p store.Principal) {
	var e secrets.Entitlement
	if !readBody(w, r, &e, 1<<20) {
		return
	}
	who, ok := s.vaultPerson(w, p)
	if !ok {
		return
	}
	out, err := s.Secrets.SetEntitlement(who, e)
	if err != nil {
		s.vaultError2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteEntitlement(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.vaultPerson(w, p)
	if !ok {
		return
	}
	if err := s.Secrets.DeleteEntitlement(who, r.PathValue("name")); err != nil {
		s.vaultError2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) postAgentTags(w http.ResponseWriter, r *http.Request, p store.Principal) {
	var in struct {
		Agent string   `json:"agent"`
		Tags  []string `json:"tags"`
	}
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	who, ok := s.vaultPerson(w, p)
	if !ok {
		return
	}
	if err := s.Secrets.SetAgentTags(who, in.Agent, in.Tags); err != nil {
		s.vaultError2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"tags": s.Secrets.TagsOfAgent(in.Agent)})
}

// agentSecretScope is the agent page's "secret scope": the entitlements that take the agent in,
// what they reach (by level), its grants, and the templates to hand.
func (s *Server) agentSecretScope(w http.ResponseWriter, r *http.Request, p store.Principal) {
	name := r.PathValue("name")
	if !p.Person() {
		writeError(w, http.StatusForbidden, "secrets.agent", "agents reach items through grants, not this door: request_secret asks, get_secret takes")
		return
	}
	if !s.ownsOrAdminsAgent(p, name) {
		writeError(w, http.StatusForbidden, "secrets.permission", "the owner or an admin sees an agent's scope")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agent":        name,
		"entitlements": s.Secrets.EntitlementsOfAgent(name),
		"preview":      s.Secrets.PreviewForAgent(name),
		"grants":       s.Secrets.GrantsOf(name),
		"tags":         s.Secrets.TagsOfAgent(name),
	})
}

func (s *Server) postAgentTemplates(w http.ResponseWriter, r *http.Request, p store.Principal) {
	var in struct {
		Names []string `json:"names"`
	}
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	who, ok := s.vaultPerson(w, p)
	if !ok {
		return
	}
	if !s.ownsOrAdminsAgent(p, r.PathValue("name")) {
		writeError(w, http.StatusForbidden, "secrets.permission", "the owner or an admin hands an agent its templates")
		return
	}
	if err := s.Secrets.ApplyTemplates(who, r.PathValue("name"), in.Names); err != nil {
		s.vaultError2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": in.Names, "preview": s.Secrets.PreviewForAgent(r.PathValue("name"))})
}

// ownsOrAdminsAgent says whether the person owns the agent, holds secrets:admin, or is an owner.
func (s *Server) ownsOrAdminsAgent(p store.Principal, name string) bool {
	if p.Has("secrets:admin") {
		return true
	}
	if acc, ok := s.Auth.Store.Account(p.User); ok && acc.Role == "owner" {
		return true
	}
	owner, ok := s.Secrets.AgentOwner(name)
	return ok && owner == p.User
}

func (s *Server) unsuspendAgent(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.vaultPerson(w, p)
	if !ok {
		return
	}
	if err := s.Secrets.UnsuspendAgent(who, r.PathValue("name")); err != nil {
		s.vaultError2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"unsuspended": true})
}

// vaultPerson maps the admin routes' principal to the vault's actor; an agent with the admin
// scope is still an agent — people manage — so it writes the refusal itself.
func (s *Server) vaultPerson(w http.ResponseWriter, p store.Principal) (secrets.Actor, bool) {
	who, ok := s.vaultActor(p)
	if !ok {
		writeError(w, http.StatusForbidden, "secrets.agent", "agents reach items through grants, not this door: request_secret asks, get_secret takes")
		return secrets.Actor{}, false
	}
	return who, true
}

// roleOfPrincipal names a person's role by the scopes their credential carries.
func (s *Server) roleOfPrincipal(p store.Principal) string {
	switch {
	case p.Has("accounts:write"):
		return "owner"
	case p.Has("machines:write"):
		return "operator"
	case p.Has("alerts:write"):
		return "oncall"
	case p.Has("status:read"):
		return "viewer"
	}
	return ""
}

// postSecretUse is beacon run's door over REST: the agent names environment keys and beacon://
// references; the values come back for injecting into a local process. Agents that must not see a
// value use run_command's secrets or http_request, which never return one.
func (s *Server) postSecretUse(w http.ResponseWriter, r *http.Request, p store.Principal) {
	name, ok := agentOf(p)
	if !ok {
		writeError(w, http.StatusForbidden, "secrets.agent", "only an agent uses a secret this way; people view values in the console")
		return
	}
	var in struct {
		Refs   map[string]string `json:"refs"`
		Reason string            `json:"reason"`
	}
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	values, err := s.Secrets.UseForProcess(name, "api", in.Refs, in.Reason,
		r.Header.Get("X-Forwarded-For"), r.Header.Get("User-Agent"))
	if err != nil {
		s.vaultError2(w, err)
		return
	}
	if values == nil {
		values = map[string]string{}
	}
	writeJSON(w, http.StatusOK, values)
}

// postSecretHTTPRequest is http_request: Beacon puts the values in and sends the request to a host
// the item is bound to; the answer comes back scrubbed.
func (s *Server) postSecretHTTPRequest(w http.ResponseWriter, r *http.Request, p store.Principal) {
	name, ok := agentOf(p)
	if !ok {
		writeError(w, http.StatusForbidden, "secrets.agent", "only an agent sends http_request; people view values in the console")
		return
	}
	var in secrets.HTTPRequestIn
	if !readBody(w, r, &in, 1<<20) {
		return
	}
	out, err := s.Secrets.HTTPRequest(name, "api", in, r.Header.Get("X-Forwarded-For"), r.Header.Get("User-Agent"))
	if err != nil {
		s.vaultError2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// vaultError2 answers with the grants' errors: the vault's own codes, and the grants' few.
func (s *Server) vaultError2(w http.ResponseWriter, err error) {
	var ng *secrets.NoGrantsError
	switch {
	case errors.Is(err, secrets.ErrSealed):
		writeError(w, http.StatusLocked, "secrets.sealed", err.Error())
	case errors.Is(err, secrets.ErrNotFound):
		writeError(w, http.StatusNotFound, "secrets.not_found", err.Error())
	case errors.Is(err, secrets.ErrBadInput), errors.Is(err, secrets.ErrInvalid), errors.Is(err, secrets.ErrRedirect):
		code := "secrets.bad_input"
		if errors.Is(err, secrets.ErrRedirect) {
			code = "secrets.redirect"
		}
		writeError(w, http.StatusBadRequest, code, err.Error())
	case errors.Is(err, secrets.ErrConflict):
		writeError(w, http.StatusConflict, "secrets.conflict", err.Error())
	case errors.Is(err, secrets.ErrSelfApproval):
		writeError(w, http.StatusForbidden, "secrets.self_approval", "the requester cannot review its own request")
	case errors.Is(err, secrets.ErrNoGrant):
		writeError(w, http.StatusForbidden, "secrets.no_grant", err.Error())
	case errors.Is(err, secrets.ErrNotBound):
		writeError(w, http.StatusForbidden, "secrets.not_bound", err.Error())
	case errors.Is(err, secrets.ErrPrivateAddress):
		writeError(w, http.StatusForbidden, "secrets.private_address", err.Error())
	case errors.Is(err, secrets.ErrThrottled):
		writeError(w, http.StatusTooManyRequests, "secrets.throttled", err.Error())
	case errors.Is(err, secrets.ErrSuspendedAgent):
		writeError(w, http.StatusForbidden, "secrets.suspended", err.Error())
	case errors.Is(err, secrets.ErrForbidden), errors.Is(err, secrets.ErrAgent):
		writeError(w, http.StatusForbidden, "secrets.permission", err.Error())
	default:
		if errors.As(err, &ng) {
			writeError(w, http.StatusForbidden, "secrets.no_grant", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "secrets.error", err.Error())
	}
}

// splitTags flattens the repeated tag query values, commas and all.
func splitTags(in []string) []string {
	var out []string
	for _, v := range in {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}
