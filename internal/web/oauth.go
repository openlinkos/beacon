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
//
// The machines are all those that are not critical, or only those ticked: ticking none is asked again,
// never taken for all. Critical machines are offered only to an owner, apart and unticked, and ticking
// one takes a confirmation; the sign-in reaches them for store.CriticalGrantTTL at most. A reused agent's
// sign-in reaches no machine the agent does not.

type consentLabels struct {
	Agent, AgentHint, Preset, Machines, All, Some, Duration, Allow, Deny, SignedIn, SignOut, Back string
	Critical, CriticalHint, CriticalConfirm                                                       string
}

type consentPage struct {
	shell
	L                  consentLabels
	Heading, Intro     string
	Fields             []kv
	AgentName          string
	Presets, Durations []option
	Machines, Critical []option
	AllMachines        bool
	CanAll             bool
	Error, Broken      string
	SignedInAs, Return string

	// Provenance says where what the client says about itself comes from: who registered it and when,
	// or the host of its metadata document. RedirectWarning says, prominently, that the answer goes to a
	// website rather than to a program on this computer.
	Provenance, RedirectWarning string
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
		Preset: s.t("What it may do"), Machines: s.t("Machines"), Some: s.t("Only these"),
		Duration: s.t("For how long"), Allow: s.t("Allow"), Deny: s.t("Deny"), SignOut: s.t("Sign out"),
		All:             s.t("All machines that are not critical, including ones added later"),
		Critical:        s.t("Critical machines"),
		CriticalHint:    s.t("Critical machines are reached only when named. Tick only those this client must reach; it reaches them for %d days at most.", int(store.CriticalGrantTTL.Hours()/24)),
		CriticalConfirm: s.t("I confirm this client may reach the critical machines ticked"),
	}
}

// oauthProblem answers an authorization request that failed its checks. It sends the error back to the
// client only when the client's redirect URI is known good and the person signed in has allowed this
// client before (RFC 9700 section 4.11.2: no automatic redirect to a URI nobody has trusted yet);
// otherwise it shows the person a page and sends nothing anywhere.
func (s *Server) oauthProblem(w http.ResponseWriter, r *http.Request, v *viewer, q oauth.Request, p *oauth.Problem) {
	if p.Redirect != "" && s.allowedBefore(v, q.ClientID) {
		http.Redirect(w, r, p.RedirectURL(s.OAuth.Issuer(r)), http.StatusSeeOther) //nolint:gosec // G710: the client's registered redirect URI, checked by Parse, of a client the person allowed
		return
	}
	s.oauthBroken(w, r, p)
}

// allowedBefore reports whether the person signed in has a grant for this client that has not ended:
// one of their agents' OAuth sign-ins.
func (s *Server) allowedBefore(v *viewer, clientID string) bool {
	if v == nil || !v.person() || clientID == "" {
		return false
	}
	now := s.now()
	for _, a := range s.Store.Agents(v.Account.Username) {
		for _, g := range s.Store.Grants(a.Name) {
			if g.Client == clientID && now.Before(g.Expires) {
				return true
			}
		}
	}
	return false
}

// authorizeParams are the parameters of an authorization request, taken from a form as they came, for
// signing in and coming back to the question.
func authorizeParams(f url.Values) url.Values {
	out := url.Values{}
	for _, k := range []string{"response_type", "client_id", "redirect_uri", "state", "code_challenge", "code_challenge_method", "scope", "resource"} {
		if v := f.Get(k); v != "" {
			out.Set(k, v)
		}
	}
	return out
}

// authorize is the authorization request. The person signs in first, before the request is read: so
// nothing is fetched for a client (its metadata document) and nothing is sent to its redirect URI for a
// request nobody signed in to.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	headers(w)
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
	q, problem := s.OAuth.Parse(r, r.URL.Query())
	if problem != nil {
		s.oauthProblem(w, r, v, q, problem)
		return
	}
	s.consentForm(w, r, v, q, http.StatusOK, s.consentDefaults(v, q))
}

// consentChoice is what the person chose, or what the form starts with: Machines the machines that are
// not critical (all, or those ticked), Critical the critical machines ticked.
type consentChoice struct {
	Agent, Preset string
	Machines      store.MachineAccess
	Critical      []string
	Duration      int // index in oauth.Durations
	Problem       string
}

// consentDefaults are what the consent form starts with: the person's agent made for this client, or a
// new name after the client's (never an existing agent made for another client, or for none: the name
// is the client's own say); the strongest preset the client asked for within the person's role (or the
// role's own); all machines that are not critical (a reused agent's own, when it names them); no
// critical machine; 30 days.
func (s *Server) consentDefaults(v *viewer, q oauth.Request) consentChoice {
	c := consentChoice{Duration: 1, Machines: store.AllAccess()}
	role := v.Account.Role
	found := false
	for _, a := range s.Store.Agents(v.Account.Username) { // the person's agent for this client: the one named after it, or another
		if a.Client == q.ClientID && (!found || a.Name == store.AgentName(q.ClientName)) {
			c.Agent, found = a.Name, true
		}
	}
	if !found {
		c.Agent = s.newAgentName(store.AgentName(q.ClientName), v.Account.Username)
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
		if !a.IsAll() {
			critical := s.Store.CriticalMachines()
			c.Machines = store.NamedAccess(slices.DeleteFunc(slices.Clone(a.Live(s.now()).Machines), func(m string) bool { return critical[m] })...)
		}
	}
	return c
}

// newAgentName is a name for a new agent after base that no agent has: base; base-username when someone
// else has base; then base-2 and on.
func (s *Server) newAgentName(base, username string) string {
	fit := func(n string) string {
		if len(n) > 40 {
			n = strings.TrimRight(n[:40], "-._")
		}
		return n
	}
	candidates := []string{base}
	if a, ok := s.Store.Agent(base); ok && a.Owner != username {
		candidates = append(candidates, fit(base+"-"+username))
	}
	for i := 2; i <= 99; i++ {
		candidates = append(candidates, fit(base+"-"+strconv.Itoa(i)))
	}
	for _, n := range candidates {
		if _, taken := s.Store.Agent(n); !taken {
			return n
		}
	}
	return ""
}

// consentProvenance says where what the client says about itself comes from, and whether the answer
// goes to a website: the client's name is its own say, so the page shows what Beacon knows besides.
func (s *Server) consentProvenance(page *consentPage, q oauth.Request) {
	switch {
	case q.Document():
		page.Provenance = s.t("Its name and where it sends you are what a document on %s says. Allow it only if you expect a client from that site.", q.DocumentHost())
	case !q.Registered.IsZero():
		page.Provenance = s.t("It registered itself with Beacon at %s and chose its own name: anyone can register a client under any name. Allow it only if you started this sign-in yourself, just now.", s.clock(q.Registered))
	}
	if !q.RedirectLocal() {
		page.RedirectWarning = s.t("Whatever you answer, Beacon sends you, with the code that signs the client in, to the website %s, not to a program on this computer. Allow it only if you trust that site.", q.FormAction())
	}
}

func (s *Server) consentForm(w http.ResponseWriter, r *http.Request, v *viewer, q oauth.Request, status int, c consentChoice) {
	name := q.ClientName
	page := consentPage{shell: s.bare(r, s.t("Allow %s", name)), L: s.consentLabels(), Heading: s.t("%s wants to use Beacon as you", name),
		Intro:     s.t("It acts as one of your agents, named %s / the name below in the audit log and the chat, and never does more than your role (%s) allows.", v.Account.Username, s.roleName(v.Account.Role)),
		AgentName: c.Agent, Presets: s.presetOptions(v.Account.Role, c.Preset), AllMachines: c.Machines.IsAll(), CanAll: true, Error: c.Problem,
		SignedInAs: s.t("Signed in as %s (%s).", v.Account.DisplayName, v.Account.Username),
		Return:     s.t("Whatever you answer, Beacon sends you back to %s.", q.FormAction())}
	s.consentProvenance(&page, q)
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
	agentAccess := v.Principal.MachineAccess // a new agent: what the person reaches
	if isReused {
		agentAccess = reused.Live(s.now())
		page.CanAll = agentAccess.IsAll() // a reused agent that names its machines: no more than those
		page.AllMachines = page.AllMachines && page.CanAll
	}
	for _, m := range s.Store.Machines() {
		switch {
		case !agentAccess.Covers(m.Name, m.Critical):
			continue // a reused agent: no machine beyond its own
		case m.Critical && v.Principal.Owner():
			page.Critical = append(page.Critical, option{Value: m.Name, Label: m.Name, Selected: slices.Contains(c.Critical, m.Name)})
		case !m.Critical:
			page.Machines = append(page.Machines, option{Value: m.Name, Label: m.Name, Selected: !c.Machines.IsAll() && c.Machines.Names(m.Name)})
		}
	}
	slices.SortFunc(page.Machines, func(a, b option) int { return strings.Compare(a.Value, b.Value) })
	slices.SortFunc(page.Critical, func(a, b option) int { return strings.Compare(a.Value, b.Value) })
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
	v := s.viewer(r)
	if v == nil { // the sign-in ended meanwhile: sign in, and see the question again
		http.Redirect(w, r, consolePath("/sign-in")+"?next="+url.QueryEscape("/oauth/authorize?"+authorizeParams(r.PostForm).Encode()), http.StatusSeeOther)
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
	q, problem := s.OAuth.Parse(r, r.PostForm)
	if problem != nil {
		s.oauthProblem(w, r, v, q, problem)
		return
	}
	f := r.PostForm
	if f.Get("decision") != "allow" {
		consentCSP(w, q)
		_ = s.Store.Audit(store.AuditEntry{Time: s.now(), Actor: v.name(), Via: "console", Action: "oauth_deny", Detail: q.ClientName})
		http.Redirect(w, r, s.OAuth.Denied(r, q), http.StatusSeeOther) //nolint:gosec // G710: the client's registered redirect URI, checked by Parse
		return
	}
	c := consentChoice{Agent: strings.TrimSpace(f.Get("agent")), Preset: f.Get("preset"), Machines: store.AllAccess()}
	i, err := strconv.Atoi(f.Get("duration"))
	if err != nil || i < 0 || i >= len(oauth.Durations) {
		i = 1
	}
	c.Duration = i
	access, why := s.consentMachines(v, f, &c)
	if why != "" {
		c.Problem = why
		s.consentForm(w, r, v, q, http.StatusBadRequest, c)
		return
	}
	until := s.now().Add(oauth.Durations[i])
	a, machines, err := s.Bridge.OAuthAgent(r.Context(), v.actor(), q.ClientID, q.ClientName, c.Agent, c.Preset, access, until)
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

// consentMachines reads the machines the person chose into c, and returns the grant's machine access, or
// what to ask again: no machine ticked under only these, a machine that is not enrolled, a critical
// machine ticked without the confirmation or by someone who is not an owner.
func (s *Server) consentMachines(v *viewer, f url.Values, c *consentChoice) (store.MachineAccess, string) {
	enrolled := map[string]store.Machine{}
	for _, m := range s.Store.Machines() {
		enrolled[m.Name] = m
	}
	var names []string
	for _, name := range f["machine"] {
		if m, ok := enrolled[name]; !ok || m.Critical {
			return store.MachineAccess{}, s.t("Choose machines from the list.")
		}
		names = append(names, name)
	}
	switch f.Get("machines") {
	case "all":
		c.Machines = store.AllAccess()
	case "some":
		c.Machines = store.NamedAccess(names...)
		if len(names) == 0 {
			return store.MachineAccess{}, s.t("Tick at least one machine, or choose all machines that are not critical.")
		}
	default:
		return store.MachineAccess{}, s.t("Choose which machines it reaches.")
	}
	for _, name := range f["critical"] {
		if m, ok := enrolled[name]; !ok || !m.Critical {
			return store.MachineAccess{}, s.t("Choose machines from the list.")
		}
		c.Critical = append(c.Critical, name)
	}
	if len(c.Critical) > 0 && !v.Principal.Owner() {
		return store.MachineAccess{}, s.t("Only an owner gives access to critical machines.")
	}
	if len(c.Critical) > 0 && f.Get("critical_confirm") != "yes" {
		return store.MachineAccess{}, s.t("Confirm that this client may reach the critical machines you ticked, or untick them.")
	}
	access := c.Machines
	access.Machines = append(slices.Clone(access.Machines), c.Critical...)
	if access.IsAll() {
		return store.AllAccess(access.Machines...), ""
	}
	return store.NamedAccess(access.Machines...), ""
}
