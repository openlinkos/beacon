package web

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/openlinkos/beacon/internal/oauth"
	"github.com/openlinkos/beacon/internal/store"
)

// /oauth/authorize, where an MCP client such as Claude Code or Codex sends a person to let it use Beacon
// (package oauth speaks the rest of the protocol). The person signs in to the console with their account
// if they have not, and sees who asks, where they will be sent back to, and what they allow: the agent it
// acts as (made, or reused by its name), its preset (the strongest their role allows unless the client
// asked for less, and they may lower it), its machines, and for how long. Allowing makes the code the
// client exchanges for its tokens; denying tells the client so. A token's sign-in cannot allow a client:
// an OAuth client always acts for a person.

type consentLabels struct {
	Agent, AgentHint, Preset, Machines, All, Some, Duration, Allow, Deny, SignedIn, SignOut, Back string
}

type consentPage struct {
	shell
	L                  consentLabels
	Heading, Intro     string
	Fields             []kv
	AgentName          string
	Presets, Durations []option
	Machines           []option
	AllMachines        bool
	Error, Broken      string
	SignedInAs, Return string
}

var consentTmpl = parse("consent.html")

// hostRe is what the consent page's Content-Security-Policy takes from a redirect URI: an origin of
// plain host characters, nothing that could end the directive.
var hostRe = regexp.MustCompile(`^(https|http)://[A-Za-z0-9.\-]+(:[0-9]{1,5})?$|^http://\[::1\](:[0-9]{1,5})?$`)

// consentCSP lets the consent form lead to the client's redirect URI: browsers hold the redirect that
// answers a form to the page's form-action.
func consentCSP(w http.ResponseWriter, q oauth.Request) {
	origin := q.FormAction()
	if !hostRe.MatchString(origin) {
		return
	}
	w.Header().Set("Content-Security-Policy", strings.Replace(CSP, "form-action 'self'", "form-action 'self' "+origin, 1))
}

func (s *Server) oauthBroken(w http.ResponseWriter, r *http.Request, p *oauth.Problem) {
	text := s.t("The request is not right, so Beacon stops here; try signing in again from the client.")
	if p.Code == "invalid_client" {
		text = s.t("Beacon does not know this client: a client registers itself, or names its metadata document, before it sends you here.")
	} else if strings.Contains(p.Description, "redirect_uri") {
		text = s.t("The client asked to send you back to an address it did not register, so Beacon stops here.")
	}
	page := consentPage{shell: s.bare(r, s.t("Sign in")), L: s.consentLabels(), Heading: s.t("This sign-in cannot go on"), Broken: text}
	s.renderStatus(w, http.StatusBadRequest, consentTmpl, page)
}

func (s *Server) consentLabels() consentLabels {
	return consentLabels{
		Agent: s.t("Agent name"), AgentHint: s.t("Its name in the audit log and the chat, after yours. The same name later reuses the agent."),
		Preset: s.t("What it may do"), Machines: s.t("Machines"), All: s.t("All machines"), Some: s.t("Only these"),
		Duration: s.t("For how long"), Allow: s.t("Allow"), Deny: s.t("Deny"), SignOut: s.t("Sign out"),
	}
}

// problem answers an authorization request that failed its checks: back to the client when its redirect
// URI is known good, on a page otherwise.
func (s *Server) oauthProblem(w http.ResponseWriter, r *http.Request, p *oauth.Problem) {
	if p.Redirect != "" {
		http.Redirect(w, r, p.RedirectURL(s.OAuth.Issuer(r)), http.StatusSeeOther) //nolint:gosec // G710: the client's registered redirect URI, checked by Parse
		return
	}
	s.oauthBroken(w, r, p)
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	headers(w)
	q, problem := s.OAuth.Parse(r, r.URL.Query())
	if problem != nil {
		s.oauthProblem(w, r, problem)
		return
	}
	v := s.viewer(r)
	if v == nil {
		toSignIn(w, r)
		return
	}
	if !v.person() {
		s.message(w, r, v, http.StatusForbidden, s.t("Sign in with your account"),
			s.t("An OAuth client acts as one of a person's agents, so it is allowed by a person signed in with their account, not with a token. Sign out, sign in with your username and password, and start again from the client."))
		return
	}
	s.consentForm(w, r, v, q, http.StatusOK, s.consentDefaults(v, q))
}

// consentChoice is what the person chose, or what the form starts with.
type consentChoice struct {
	Agent, Preset string
	Machines      []string
	Duration      int // index in oauth.Durations
	Problem       string
}

// consentDefaults are what the consent form starts with: the person's agent for this client, or one
// named after it; the strongest preset the client asked for within the person's role (or the role's
// own); all machines; 30 days.
func (s *Server) consentDefaults(v *viewer, q oauth.Request) consentChoice {
	c := consentChoice{Duration: 1, Agent: store.AgentName(q.ClientName)}
	role := v.Account.Role
	found := false
	for _, a := range s.Store.Agents(v.Account.Username) { // the person's agent for this client: the one named after it, or another
		if a.Client == q.ClientID && (!found || a.Name == store.AgentName(q.ClientName)) {
			c.Agent, found = a.Name, true
		}
	}
	if a, ok := s.Store.Agent(c.Agent); ok && a.Owner != v.Account.Username {
		c.Agent = c.Agent + "-" + v.Account.Username
		if len(c.Agent) > 40 {
			c.Agent = c.Agent[:40]
		}
	}
	c.Preset = store.PresetFor(role)
	if asked := q.Presets(); len(asked) > 0 {
		best := ""
		for _, p := range store.Presets {
			if slices.Contains(asked, p) && store.PresetWithin(p, role) {
				best = p
			}
		}
		if best != "" {
			c.Preset = best
		}
	}
	if a, ok := s.Store.Agent(c.Agent); ok && a.Owner == v.Account.Username { // reused: never more than it may already do
		if a.Preset != store.PresetCustom && !store.PresetAtMost(c.Preset, a.Preset) {
			c.Preset = a.Preset
		}
		c.Machines = a.Machines
	}
	return c
}

func (s *Server) consentForm(w http.ResponseWriter, r *http.Request, v *viewer, q oauth.Request, status int, c consentChoice) {
	name := q.ClientName
	page := consentPage{shell: s.bare(r, s.t("Allow %s", name)), L: s.consentLabels(), Heading: s.t("%s wants to use Beacon as you", name),
		Intro:     s.t("It acts as one of your agents, named %s / the name below in the audit log and the chat, and never does more than your role (%s) allows.", v.Account.Username, s.roleName(v.Account.Role)),
		AgentName: c.Agent, Presets: s.presetOptions(v.Account.Role, c.Preset), AllMachines: len(c.Machines) == 0, Error: c.Problem,
		SignedInAs: s.t("Signed in as %s (%s).", v.Account.DisplayName, v.Account.Username),
		Return:     s.t("Whatever you answer, Beacon sends you back to %s.", q.FormAction())}
	page.CSRF = v.Session.CSRF
	reused, isReused := s.Store.Agent(c.Agent)
	isReused = isReused && reused.Owner == v.Account.Username
	if isReused && reused.Preset != store.PresetCustom { // a reused agent: no preset beyond its own
		page.Presets = slices.DeleteFunc(page.Presets, func(o option) bool { return !store.PresetAtMost(o.Value, reused.Preset) })
	}
	for k, vals := range q.Fields() {
		page.Fields = append(page.Fields, kv{K: k, V: vals[0]})
	}
	slices.SortFunc(page.Fields, func(a, b kv) int { return strings.Compare(a.K, b.K) })
	for _, m := range s.Store.Machines() {
		if isReused && len(reused.Machines) > 0 && !slices.Contains(reused.Machines, m.Name) {
			continue // a reused agent: no machine beyond its own
		}
		page.Machines = append(page.Machines, option{Value: m.Name, Label: m.Name, Selected: slices.Contains(c.Machines, m.Name)})
	}
	slices.SortFunc(page.Machines, func(a, b option) int { return strings.Compare(a.Value, b.Value) })
	for i, d := range oauth.Durations {
		label := s.t("%d days", int(d.Hours()/24))
		if d.Hours() == 24 {
			label = s.t("1 day")
		}
		page.Durations = append(page.Durations, option{Value: strconv.Itoa(i), Label: label, Selected: i == c.Duration})
	}
	consentCSP(w, q)
	s.renderStatus(w, status, consentTmpl, page)
}

func (s *Server) consent(w http.ResponseWriter, r *http.Request) {
	headers(w)
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		s.oauthBroken(w, r, &oauth.Problem{Code: "invalid_request"})
		return
	}
	q, problem := s.OAuth.Parse(r, r.PostForm)
	if problem != nil {
		s.oauthProblem(w, r, problem)
		return
	}
	v := s.viewer(r)
	if v == nil { // the sign-in ended meanwhile: sign in, and see the question again
		http.Redirect(w, r, consolePath("/sign-in")+"?next="+url.QueryEscape("/oauth/authorize?"+q.Fields().Encode()), http.StatusSeeOther)
		return
	}
	if !sameOrigin(r) || !equal(r.PostForm.Get(csrfField), v.Session.CSRF) {
		s.message(w, r, v, http.StatusForbidden, s.t("The form has expired"), s.t("Reload the page and try again."))
		return
	}
	if !v.person() {
		s.message(w, r, v, http.StatusForbidden, s.t("Sign in with your account"),
			s.t("An OAuth client acts as one of a person's agents, so it is allowed by a person signed in with their account, not with a token. Sign out, sign in with your username and password, and start again from the client."))
		return
	}
	f := r.PostForm
	if f.Get("decision") != "allow" {
		consentCSP(w, q)
		_ = s.Store.Audit(store.AuditEntry{Time: s.now(), Actor: v.name(), Via: "console", Action: "oauth_deny", Detail: q.ClientName})
		http.Redirect(w, r, s.OAuth.Denied(r, q), http.StatusSeeOther) //nolint:gosec // G710: the client's registered redirect URI, checked by Parse
		return
	}
	c := consentChoice{Agent: strings.TrimSpace(f.Get("agent")), Preset: f.Get("preset")}
	if f.Get("machines") == "some" {
		c.Machines = f["machine"]
	}
	i, err := strconv.Atoi(f.Get("duration"))
	if err != nil || i < 0 || i >= len(oauth.Durations) {
		i = 1
	}
	c.Duration = i
	until := s.now().Add(oauth.Durations[i])
	a, machines, err := s.Bridge.OAuthAgent(r.Context(), v.actor(), q.ClientID, q.ClientName, c.Agent, c.Preset, c.Machines, until)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrExists):
			c.Problem = s.t("That name is taken; choose another.")
		default:
			c.Problem = s.t("That did not work: check the name (2 to 40 lower-case letters, digits, dots, dashes and underscores), the machines, and that the preset is within the owner's role.")
		}
		s.consentForm(w, r, v, q, http.StatusBadRequest, c)
		return
	}
	to, err := s.OAuth.Code(r, q, a.Name, c.Preset, machines, until)
	if err != nil {
		s.log().Error("oauth: issuing a code failed", "err", err)
		c.Problem = s.t("That did not work; the log says why.")
		s.consentForm(w, r, v, q, http.StatusInternalServerError, c)
		return
	}
	consentCSP(w, q)
	http.Redirect(w, r, to, http.StatusSeeOther) //nolint:gosec // G710: the client's registered redirect URI, checked by Parse
}
