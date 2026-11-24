package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// The agents: programs that act for a person, each with a preset that says what it may do (never more
// than its owner's role) and the machines it reaches, and its credentials: static tokens for headless
// agents and CI, OAuth sign-ins for interactive clients such as Claude Code and Codex, and SSH keys for
// the channel. People see their own agents; owners see everyone's. Every change goes through the
// bridge, audited and echoed to the chat; a token's secret is shown once, when it is made.

func (s *Server) registerAgents(at func(method, path string, h http.HandlerFunc)) {
	at("GET", "/agents", s.console("", s.agentsList))
	at("POST", "/agents", s.action("", s.createAgent))
	at("GET", "/agents/{name}", s.console("", s.agentPage))
	at("POST", "/agents/{name}/settings", s.action("", s.agentSettings))
	at("POST", "/agents/{name}/tokens", s.action("", s.agentToken))
	at("POST", "/agents/{name}/tokens/{token}/revoke", s.action("", s.revokeAgentToken))
	at("POST", "/agents/{name}/keys", s.action("", s.addAgentKey))
	at("POST", "/agents/{name}/keys/remove", s.action("", s.removeAgentKey))
	at("POST", "/agents/{name}/grants/{id}/revoke", s.action("", s.revokeGrant))
	at("POST", "/agents/{name}/delete", s.action("", s.deleteAgent))
}

// expiries are the lifetimes the console offers a token; 365 days is the default for a headless agent.
var expiries = []struct {
	Key string
	D   time.Duration
}{{"30d", 30 * 24 * time.Hour}, {"90d", 90 * 24 * time.Hour}, {"365d", 365 * 24 * time.Hour}}

func (s *Server) expiryOptions() []option {
	var out []option
	for _, e := range expiries {
		out = append(out, option{Value: e.Key, Label: s.t("%d days", int(e.D.Hours()/24)), Selected: e.Key == "365d"})
	}
	return out
}

func expiry(key string) (time.Duration, bool) {
	for _, e := range expiries {
		if e.Key == key {
			return e.D, true
		}
	}
	return 0, false
}

// presetOptions are the presets within role, the strongest selected unless selected names one.
func (s *Server) presetOptions(role, selected string) []option {
	if selected == "" {
		selected = store.PresetFor(role)
	}
	var out []option
	for _, p := range store.Presets {
		if role == "" || store.PresetWithin(p, role) {
			out = append(out, option{Value: p, Label: s.presetName(p), Help: strings.Join(activeAgentScopes(store.PresetScopes(p)), ", "), Selected: p == selected})
		}
	}
	return out
}

// legacyMachineSelection reads forms issued before the explicit choice of access range.
func legacyMachineSelection(text string) []string {
	var out []string
	for _, m := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
		if m != "all" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

// agentMachines checks the machine choice before passing it to the bridge. An empty selection must
// never be interpreted as a request for unrestricted access. The bridge still decides who may grant it.
func (s *Server) agentMachines(f url.Values, existing []string) (machines []string, problem string) {
	if !f.Has("machine_access_form") && !f.Has("machine_access") && !f.Has("machine") {
		if f.Has("machines") {
			return legacyMachineSelection(f.Get("machines")), ""
		}
		return existing, ""
	}
	if len(f["machine_access"]) != 1 {
		return nil, "machine_access"
	}
	switch f.Get("machine_access") {
	case "all":
		return nil, ""
	case "selected":
		if len(f["machine"]) == 0 {
			return nil, "machine_selection"
		}
		known := make(map[string]bool)
		for _, m := range s.Store.Machines() {
			known[m.Name] = true
		}
		for _, name := range f["machine"] {
			if !known[name] && !slices.Contains(existing, name) {
				return nil, "machine_unknown"
			}
			if !slices.Contains(machines, name) {
				machines = append(machines, name)
			}
		}
		return machines, ""
	default:
		return nil, "machine_access"
	}
}

type agentMachineAccess struct {
	L                agentLabels
	All, Selected    bool
	CanAll           bool
	Options          []option
	SelectionSummary string
}

func (s *Server) agentMachineAccess(v *viewer, current []string, editing bool) agentMachineAccess {
	a := agentMachineAccess{L: s.agentLabels(), All: editing && len(current) == 0, Selected: len(current) > 0,
		CanAll: len(v.Principal.Machines) == 0 || editing && len(current) == 0, SelectionSummary: s.t("%s machines selected", num(len(current)))}
	for _, m := range s.Store.Machines() {
		selected := slices.Contains(current, m.Name)
		if selected || len(v.Principal.Machines) == 0 || slices.Contains(v.Principal.Machines, m.Name) {
			a.Options = append(a.Options, option{Value: m.Name, Label: m.Name, Selected: selected})
		}
	}
	for _, name := range current {
		if !slices.ContainsFunc(a.Options, func(o option) bool { return o.Value == name }) {
			a.Options = append(a.Options, option{Value: name, Label: name, Selected: true, Help: s.t("Not registered")})
		}
	}
	return a
}

// Existing grants may carry this reserved permission. Keep it on updates, but do not offer it as an
// operation a person can grant in the console until it has an operation behind it.
const reservedAgentScope = "incidents:write"

func activeAgentScopes(scopes []string) []string {
	return slices.DeleteFunc(slices.Clone(scopes), func(scope string) bool { return scope == reservedAgentScope })
}

type agentLabels struct {
	Agent, Owner, Preset, Scopes, Machines, Credentials, Keys, Created, Name, NameHint, Expires, Create, Tokens, Grants,
	Client, LastUsed, Revoke, NewToken, AddKey, Key, KeyHint, Comment, Remove, Delete, DeleteHint, Save, Settings, SSHConfig, OAuth, OAuthHelp,
	Headless, HeadlessHelp, Hooks, None, Renewed, All, NoAgents, NoTokens, NoGrants, NoKeys, Fingerprint, Added, Until, Allowed,
	CustomScopes, CustomScopesHint, AccessRange, AllCurrentAndFuture, SelectedMachines, NoMachineOptions, PickMachines, SelectionCount string
}

func (s *Server) agentLabels() agentLabels {
	return agentLabels{
		Agent: s.t("Agent"), Owner: s.t("Owner"), Preset: s.t("Preset"), Scopes: s.t("May do now"), Machines: s.t("Machines"),
		Credentials: s.t("Credentials"), Keys: s.t("SSH keys"), Created: s.t("Created"), Name: s.t("Name"),
		NameHint: s.t("2 to 40 lower-case letters, digits, dots, dashes and underscores; the audit log names it with you, as you / name."),
		Expires:  s.t("Token expires after"), Create: s.t("Make the agent and its token"), Tokens: s.t("Tokens"), Grants: s.t("OAuth sign-ins"),
		Client: s.t("Client"), LastUsed: s.t("Last used"), Revoke: s.t("Revoke"), NewToken: s.t("New token"), AddKey: s.t("Add an SSH key"),
		Key: s.t("Public key"), KeyHint: s.t("An authorized_keys line, such as the content of ~/.ssh/id_ed25519.pub; never a private key."),
		Comment: s.t("Comment"), Remove: s.t("Remove"), Delete: s.t("Delete the agent"),
		DeleteHint: s.t("Deleting ends every token, OAuth sign-in and key of the agent at once; what it did stays on record."),
		Save:       s.t("Save"), Settings: s.t("What it may do"), SSHConfig: s.t("For its own ssh, scp and rsync"),
		OAuth:     s.t("Connect Claude Code or Codex"),
		OAuthHelp: s.t("Add Beacon to the client, then sign in from it: your browser opens here and you allow it as one of your agents. No token to copy."),
		Headless:  s.t("A headless agent or a CI job"), HeadlessHelp: s.t("Make an agent with a token: the token is shown once, with the commands that use it."),
		Hooks: s.t("Hooks that wake agents"), None: s.t("None"), Renewed: s.t("Renewed"), All: s.t("All machines"),
		NoAgents: s.t("No agents yet."), NoTokens: s.t("No tokens."), NoGrants: s.t("No OAuth sign-ins."), NoKeys: s.t("No SSH keys."),
		Fingerprint: s.t("Fingerprint"), Added: s.t("Added"), Until: s.t("Expires"), Allowed: s.t("Allowed"),
		CustomScopes:     s.t("Custom scopes"),
		CustomScopesHint: s.t("Choose permissions within the owner's role and your own access."),
		AccessRange:      s.t("Machine access"), AllCurrentAndFuture: s.t("All current and future machines"),
		SelectedMachines: s.t("Selected machines"), NoMachineOptions: s.t("There are no machines available to select."),
		PickMachines: s.t("Select at least one machine."), SelectionCount: s.t("%s machines selected", "{count}"),
	}
}

// scopeOptions are the people's scopes, the ones in selected ticked.
func (s *Server) scopeOptions(selected []string) []option {
	var out []option
	for _, sc := range activeAgentScopes(store.PeopleScopes) {
		out = append(out, option{Value: sc, Label: sc, Selected: slices.Contains(selected, sc)})
	}
	return out
}

// withCustom adds the custom preset to presets. Only an owner who is a person is offered it.
func (s *Server) withCustom(presets []option, selected string) []option {
	return append(presets, option{Value: store.PresetCustom, Label: s.presetName(store.PresetCustom), Selected: selected == store.PresetCustom})
}

type agentRow struct {
	Name, Href, Owner, Preset, Machines, Credentials, Keys, Created string
	Scopes                                                          []string
}

type agentOwnerChoice struct {
	option
	Scopes string
}

type agentsPage struct {
	shell
	L                    agentLabels
	Rows                 []agentRow
	Presets, Expiries    []option
	ScopeOptions         []option
	Owners               []agentOwnerChoice
	MachineAccess        agentMachineAccess
	CanCreate, ShowOwner bool
	CanCustom            bool
	ClaudeCmd, CodexCmd  string
	HooksHref            string
}

var agentsTmpl = parse("agents.html")

// base is Beacon's public address, for the commands the console shows.
func (s *Server) base(r *http.Request) string {
	if s.Pages != nil && s.Pages.PublicURL != "" {
		return strings.TrimSuffix(s.Pages.PublicURL, "/")
	}
	return "https://" + r.Host
}

func (s *Server) credentials(a bridge.AgentView) string {
	return s.t("%d tokens, %d OAuth sign-ins", len(a.Tokens), len(a.Grants))
}

func (s *Server) agentsList(w http.ResponseWriter, r *http.Request, v *viewer) {
	owner := r.URL.Query().Get("owner")
	if !v.owner() {
		owner = ""
	}
	list, err := s.Bridge.Agents(v.actor(), owner)
	if err != nil {
		list = nil // a token's sign-in has no agents of its own
	}
	base := s.base(r)
	p := agentsPage{shell: s.shellFor(r, v, "agents", s.t("Agents")), L: s.agentLabels(), Expiries: s.expiryOptions(), ShowOwner: v.owner(),
		CanCreate: v.person() || v.owner(), HooksHref: consolePath("/hooks"),
		ClaudeCmd: "claude mcp add --transport http beacon " + base + "/mcp\n# then /mcp in Claude Code, or: claude mcp login beacon",
		CodexCmd:  "codex mcp add beacon --url " + base + "/mcp\ncodex mcp login beacon"}
	role := ""
	if v.person() {
		role = v.Account.Role
	}
	p.Presets = s.presetOptions(role, "")
	if p.CanCreate {
		p.MachineAccess = s.agentMachineAccess(v, nil, false)
	}
	if v.owner() && v.person() {
		p.CanCustom = true
		p.Presets = s.withCustom(p.Presets, "")
		p.ScopeOptions = s.scopeOptions(nil)
	}
	if v.owner() {
		for _, a := range s.Store.Accounts() {
			if !a.Disabled {
				p.Owners = append(p.Owners, agentOwnerChoice{
					option: option{Value: a.Username, Label: a.DisplayName + " (" + a.Username + ")", Selected: v.person() && a.Username == v.Account.Username},
					Scopes: strings.Join(activeAgentScopes(store.RoleScopes(a.Role)), ", "),
				})
			}
		}
	}
	p.Count = num(len(list))
	for _, a := range list {
		row := agentRow{Name: a.Name, Href: agentHref(a.Name), Owner: a.Owner, Preset: s.presetName(a.Preset), Scopes: activeAgentScopes(a.Scopes),
			Machines: s.t("All machines"), Credentials: s.credentials(a), Keys: num(len(a.SSHKeys)), Created: s.clock(a.Created)}
		if len(a.Machines) > 0 {
			row.Machines = strings.Join(a.Machines, ", ")
		}
		p.Rows = append(p.Rows, row)
	}
	s.render(w, agentsTmpl, p)
}

// agentProblem names what went wrong with an agent's form, for the page to say (problem).
func agentProblem(err error) string {
	switch {
	case errors.Is(err, bridge.ErrForbidden):
		return "forbidden"
	case errors.Is(err, store.ErrExists):
		return "taken"
	case errors.Is(err, store.ErrNotFound):
		return "not_found"
	case errors.Is(err, bridge.ErrBadInput):
		return "bad_agent"
	}
	return problemCode(err)
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request, v *viewer) {
	f := r.PostForm
	ttl, ok := expiry(f.Get("expires"))
	if !ok {
		back(w, r, consolePath("/agents"), "error", "bad_agent")
		return
	}
	owner := ""
	if v.owner() {
		owner = f.Get("owner")
	}
	machines, problem := s.agentMachines(f, nil)
	if problem != "" {
		back(w, r, consolePath("/agents"), "error", problem)
		return
	}
	in := bridge.AgentInput{Name: strings.TrimSpace(f.Get("name")), Owner: owner, Preset: f.Get("preset"),
		Machines: machines, TTL: ttl}
	if in.Preset == store.PresetCustom {
		in.Scopes = activeAgentScopes(f["scopes"])
	}
	out, err := s.Bridge.CreateAgent(r.Context(), v.actor(), in)
	if err != nil {
		back(w, r, consolePath("/agents"), "error", agentProblem(err))
		return
	}
	s.showToken(w, r, v, out)
}

// showToken shows an agent's new token, once, with the snippets that use it.
func (s *Server) showToken(w http.ResponseWriter, r *http.Request, v *viewer, out bridge.AgentCreated) {
	href := consolePath("/agents/") + url.PathEscape(out.Agent.Name)
	p := oncePage{shell: s.shellFor(r, v, "agents", s.t("Token for agent %s", out.Agent.Name), crumb{Label: s.t("Agents"), Href: consolePath("/agents")},
		crumb{Label: out.Agent.Name, Href: href}), Value: out.Secret, Mono: true, Snippets: out.Snippets, SnippetsTitle: s.t("Ready to paste"),
		Text:    s.t("Token %s of agent %s (%s), until %s. Put it where the agent reads it.", out.Token.Name, out.Agent.Actor, s.presetName(out.Agent.Preset), s.clock(out.Token.Expires)),
		Warning: s.t("The token is shown only now; Beacon keeps only a hash of it. It renews itself if the agent calls renew_token before it expires."),
		Back:    s.t("To the agent"), BackHref: href}
	s.renderStatus(w, http.StatusCreated, onceTmpl, p)
}

type agentTokenRow struct{ Name, Created, Expires, Renewed, Revoke string }

type grantRow struct{ Client, Allowed, Created, LastUsed, Expires, Revoke string }

type keyRow struct{ Fingerprint, Comment, Added string }

type agentPage struct {
	shell
	L                                                         agentLabels
	Facts                                                     []kvLink
	Scopes                                                    []string
	Presets, Expiries                                         []option
	SSHConfig                                                 string
	MachineAccess                                             agentMachineAccess
	ScopeOptions                                              []option
	Custom, CanCustom                                         bool
	Tokens                                                    []agentTokenRow
	Grants                                                    []grantRow
	Keys                                                      []keyRow
	SettingsAction, TokenAction, KeyAction, RemoveKey, Delete string
}

var agentTmpl = parse("agent.html")

func (s *Server) agentPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	a, err := s.Bridge.Agent(v.actor(), r.PathValue("name"))
	if err != nil {
		s.missing(w, r, v)
		return
	}
	path := consolePath("/agents/") + url.PathEscape(a.Name)
	p := agentPage{shell: s.shellFor(r, v, "agents", a.Name, crumb{Label: s.t("Agents"), Href: consolePath("/agents")}), L: s.agentLabels(),
		Scopes: activeAgentScopes(a.Scopes), Expiries: s.expiryOptions(), MachineAccess: s.agentMachineAccess(v, a.Machines, true), Custom: a.Preset == store.PresetCustom,
		SSHConfig: s.Bridge.SSHConfig(a.Name), SettingsAction: path + "/settings", TokenAction: path + "/tokens", KeyAction: path + "/keys",
		RemoveKey: path + "/keys/remove", Delete: path + "/delete"}
	role := ""
	if owner, ok := s.Store.Account(a.Owner); ok {
		role = owner.Role
	}
	p.Presets = s.presetOptions(role, a.Preset)
	if v.owner() && v.person() {
		p.CanCustom = true
		p.Presets = s.withCustom(p.Presets, a.Preset)
		p.ScopeOptions = s.scopeOptions(a.Wanted)
	}
	p.Facts = []kvLink{{K: s.t("In the audit log"), V: a.Actor}, {K: s.t("Owner"), V: a.Owner}, {K: s.t("Preset"), V: s.presetName(a.Preset)},
		{K: s.t("Created"), V: s.clock(a.Created) + " · " + a.CreatedBy}}
	access := s.t("All machines")
	if len(a.Machines) != 0 {
		access = strings.Join(a.Machines, ", ")
	}
	p.Facts = append(p.Facts, kvLink{K: p.L.AccessRange, V: access})
	if a.Preset == store.PresetCustom {
		p.Facts = append(p.Facts, kvLink{K: s.t("Asks for"), V: strings.Join(activeAgentScopes(a.Wanted), ", ")})
	}
	for _, t := range a.Tokens {
		row := agentTokenRow{Name: t.Name, Created: s.clock(t.Created), Expires: s.clock(t.Expires), Revoke: path + "/tokens/" + url.PathEscape(t.Name) + "/revoke"}
		if !t.Renewed.IsZero() {
			row.Renewed = s.clock(t.Renewed)
		}
		p.Tokens = append(p.Tokens, row)
	}
	for _, g := range a.Grants {
		row := grantRow{Client: g.Client, Allowed: s.presetName(g.Preset), Created: s.clock(g.Created), Expires: s.clock(g.Expires), Revoke: path + "/grants/" + url.PathEscape(g.ID) + "/revoke"}
		if len(g.Machines) > 0 {
			row.Allowed += " · " + strings.Join(g.Machines, ", ")
		}
		if !g.LastUsed.IsZero() {
			row.LastUsed = s.clock(g.LastUsed)
		}
		p.Grants = append(p.Grants, row)
	}
	for _, k := range a.SSHKeys {
		row := keyRow{Fingerprint: k.Fingerprint, Comment: k.Comment}
		if !k.Added.IsZero() {
			row.Added = s.clock(k.Added)
		}
		p.Keys = append(p.Keys, row)
	}
	s.render(w, agentTmpl, p)
}

func agentHref(name string) string { return consolePath("/agents/") + url.PathEscape(name) }

func (s *Server) agentSettings(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	a, err := s.Bridge.Agent(v.actor(), name)
	if err != nil {
		s.missing(w, r, v)
		return
	}
	f := r.PostForm
	preset := f.Get("preset")
	machines, problem := s.agentMachines(f, a.Machines)
	if problem != "" {
		back(w, r, agentHref(a.Name), "error", problem)
		return
	}
	up := bridge.AgentUpdate{Preset: &preset, Machines: &machines}
	// A person who is an owner and chooses custom always sends a list, so an unticked form is empty and
	// refused. Anyone else sets scopes only when the form carries them; a custom agent saved without
	// that field keeps its scopes. The bridge checks the caller again.
	if preset == store.PresetCustom && (f.Has("scopes") || (v.owner() && v.person())) {
		up.Scopes = append([]string{}, activeAgentScopes(f["scopes"])...)
		if a.Preset == store.PresetCustom && slices.Contains(a.Wanted, reservedAgentScope) {
			up.Scopes = append(up.Scopes, reservedAgentScope)
		}
	}
	if _, err := s.Bridge.UpdateAgent(r.Context(), v.actor(), name, up); err != nil {
		back(w, r, agentHref(a.Name), "error", agentProblem(err))
		return
	}
	back(w, r, agentHref(a.Name), "done", "saved")
}

func (s *Server) agentToken(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	ttl, ok := expiry(r.PostForm.Get("expires"))
	if !ok {
		back(w, r, agentHref(name), "error", "bad_agent")
		return
	}
	out, err := s.Bridge.NewAgentToken(r.Context(), v.actor(), name, ttl)
	if err != nil {
		back(w, r, agentHref(name), "error", agentProblem(err))
		return
	}
	s.showToken(w, r, v, out)
}

func (s *Server) revokeAgentToken(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	if err := s.Bridge.RevokeAgentToken(r.Context(), v.actor(), r.PathValue("token")); err != nil {
		back(w, r, agentHref(name), "error", agentProblem(err))
		return
	}
	if v.Token != nil && v.Token.Name == r.PathValue("token") { // its own token: the sign-in ended with it
		http.SetCookie(w, s.cookie(sessionCookie, "", time.Time{}))
		http.Redirect(w, r, consolePath("/sign-in"), http.StatusSeeOther)
		return
	}
	back(w, r, agentHref(name), "done", "revoked")
}

func (s *Server) addAgentKey(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	if _, err := s.Bridge.AddSSHKey(r.Context(), v.actor(), name, r.PostForm.Get("key"), r.PostForm.Get("comment")); err != nil {
		code := agentProblem(err)
		if code == "taken" {
			code = "key_taken"
		}
		if code == "bad_agent" {
			code = "bad_key"
		}
		back(w, r, agentHref(name), "error", code)
		return
	}
	back(w, r, agentHref(name), "done", "key_added")
}

func (s *Server) removeAgentKey(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	if err := s.Bridge.RemoveSSHKey(r.Context(), v.actor(), name, r.PostForm.Get("fingerprint")); err != nil {
		back(w, r, agentHref(name), "error", agentProblem(err))
		return
	}
	back(w, r, agentHref(name), "done", "key_removed")
}

func (s *Server) revokeGrant(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	if err := s.Bridge.RevokeGrant(r.Context(), v.actor(), r.PathValue("id")); err != nil {
		back(w, r, agentHref(name), "error", agentProblem(err))
		return
	}
	back(w, r, agentHref(name), "done", "grant_revoked")
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	if err := s.Bridge.DeleteAgent(r.Context(), v.actor(), name); err != nil {
		back(w, r, agentHref(name), "error", agentProblem(err))
		return
	}
	if v.Token != nil && v.Token.Agent == name {
		http.SetCookie(w, s.cookie(sessionCookie, "", time.Time{}))
		http.Redirect(w, r, consolePath("/sign-in"), http.StatusSeeOther)
		return
	}
	back(w, r, consolePath("/agents"), "done", "agent_deleted")
}
