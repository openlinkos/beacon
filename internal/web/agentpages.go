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
			out = append(out, option{Value: p, Label: s.presetName(p), Help: s.roleHelp(map[string]string{"readonly": "viewer", "oncall": "oncall", "operator": "operator"}[p]), Selected: p == selected})
		}
	}
	return out
}

// machineList reads the machines a form names, separated by commas or spaces; none means all.
func machineList(text string) []string {
	var out []string
	for _, m := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
		if m != "all" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

type agentLabels struct {
	Agent, Owner, Preset, Scopes, Machines, Credentials, Keys, Created, New, Name, NameHint, MachinesHint, Expires, Create, Tokens, Grants,
	Client, LastUsed, Revoke, NewToken, AddKey, Key, KeyHint, Comment, Remove, Delete, DeleteHint, Save, Settings, SSHConfig, OAuth, OAuthHelp,
	Headless, HeadlessHelp, Hooks, None, Renewed, All, NoAgents, NoTokens, NoGrants, NoKeys, Fingerprint, Added, Until, Allowed string
}

func (s *Server) agentLabels() agentLabels {
	return agentLabels{
		Agent: s.t("Agent"), Owner: s.t("Owner"), Preset: s.t("Preset"), Scopes: s.t("May do now"), Machines: s.t("Machines"),
		Credentials: s.t("Credentials"), Keys: s.t("SSH keys"), Created: s.t("Created"), New: s.t("New agent"), Name: s.t("Name"),
		NameHint:     s.t("2 to 40 lower-case letters, digits, dots, dashes and underscores; the audit log names it with you, as you / name."),
		MachinesHint: s.t("Machines it may reach through the SSH channel, separated by commas; all when empty."),
		Expires:      s.t("Token expires after"), Create: s.t("Make the agent and its token"), Tokens: s.t("Tokens"), Grants: s.t("OAuth sign-ins"),
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
	}
}

type agentRow struct {
	Name, Href, Owner, Preset, Machines, Credentials, Keys, Created string
	Scopes                                                          []string
}

type agentsPage struct {
	shell
	L                    agentLabels
	Rows                 []agentRow
	Presets, Expiries    []option
	Owners               []option
	CanCreate, ShowOwner bool
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
	if v.owner() {
		for _, a := range s.Store.Accounts() {
			if !a.Disabled {
				p.Owners = append(p.Owners, option{Value: a.Username, Label: a.DisplayName + " (" + a.Username + ")", Selected: v.person() && a.Username == v.Account.Username})
			}
		}
	}
	p.Count = num(len(list))
	for _, a := range list {
		row := agentRow{Name: a.Name, Href: consolePath("/agents/") + url.PathEscape(a.Name), Owner: a.Owner, Preset: s.presetName(a.Preset), Scopes: a.Scopes,
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
	out, err := s.Bridge.CreateAgent(r.Context(), v.actor(), bridge.AgentInput{Name: strings.TrimSpace(f.Get("name")), Owner: owner, Preset: f.Get("preset"),
		Machines: machineList(f.Get("machines")), TTL: ttl})
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
	Machines, SSHConfig                                       string
	Custom                                                    bool
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
		Scopes: a.Scopes, Expiries: s.expiryOptions(), Machines: strings.Join(a.Machines, ", "), Custom: a.Preset == store.PresetCustom,
		SSHConfig: s.Bridge.SSHConfig(a.Name), SettingsAction: path + "/settings", TokenAction: path + "/tokens", KeyAction: path + "/keys",
		RemoveKey: path + "/keys/remove", Delete: path + "/delete"}
	role := ""
	if owner, ok := s.Store.Account(a.Owner); ok {
		role = owner.Role
	}
	p.Presets = s.presetOptions(role, a.Preset)
	p.Facts = []kvLink{{K: s.t("In the audit log"), V: a.Actor}, {K: s.t("Owner"), V: a.Owner}, {K: s.t("Preset"), V: s.presetName(a.Preset)},
		{K: s.t("Created"), V: s.clock(a.Created) + " · " + a.CreatedBy}}
	if a.Preset == store.PresetCustom {
		p.Facts = append(p.Facts, kvLink{K: s.t("Asks for"), V: strings.Join(a.Wanted, ", ")})
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
	if err := s.Bridge.UpdateAgent(r.Context(), v.actor(), name, r.PostForm.Get("preset"), machineList(r.PostForm.Get("machines"))); err != nil {
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
