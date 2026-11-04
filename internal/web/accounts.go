package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// People's accounts on the console: setting Beacon up with its first owner, joining with an invitation,
// choosing a new password with a reset link (each a one-time link, the forms beside the brand panel like
// the sign-in), one's own account and password, and, for owners, the people: invitations, roles,
// disabling, reset links. Every change goes through the bridge, audited and echoed to the chat; links
// and passwords never are.

func (s *Server) registerAccounts(at func(method, path string, h http.HandlerFunc)) {
	at("GET", "/setup", s.accountFormPage(store.LinkSetup))
	at("POST", "/setup", s.accountFormPost(store.LinkSetup))
	at("GET", "/join", s.accountFormPage(store.LinkInvite))
	at("POST", "/join", s.accountFormPost(store.LinkInvite))
	at("GET", "/reset", s.accountFormPage(store.LinkReset))
	at("POST", "/reset", s.accountFormPost(store.LinkReset))
	at("GET", "/account", s.console("", s.accountPage))
	at("POST", "/account/password", s.action("", s.changePassword))
	at("GET", "/people", s.console("accounts:write", s.people))
	at("POST", "/people/invite", s.action("accounts:write", s.invite))
	at("POST", "/people/invitations/{id}/withdraw", s.action("accounts:write", s.withdraw))
	at("POST", "/people/{name}/role", s.action("accounts:write", s.setRole))
	at("POST", "/people/{name}/disable", s.action("accounts:write", s.setDisabled(true)))
	at("POST", "/people/{name}/enable", s.action("accounts:write", s.setDisabled(false)))
	at("POST", "/people/{name}/reset", s.action("accounts:write", s.resetLink))
}

// roleName is how the console names a role.
func (s *Server) roleName(role string) string {
	switch role {
	case "viewer":
		return s.t("Viewer")
	case "oncall":
		return s.t("On call")
	case "operator":
		return s.t("Operator")
	case "owner":
		return s.t("Owner")
	}
	return role
}

// roleHelp says what a role may do.
func (s *Server) roleHelp(role string) string {
	switch role {
	case "viewer":
		return s.t("Sees everything: alerts, machines, sessions and their replays, the configuration, the audit log.")
	case "oncall":
		return s.t("And acts on alerts and reaches machines through the SSH channel, by their agents.")
	case "operator":
		return s.t("And adds and removes machines, pauses agents and schedules maintenance.")
	case "owner":
		return s.t("And changes the configuration, invites people, sets roles and manages every agent.")
	}
	return ""
}

// presetName is how the console names an agent's preset.
func (s *Server) presetName(preset string) string {
	switch preset {
	case "readonly":
		return s.t("Read only")
	case "oncall":
		return s.t("On call")
	case "operator":
		return s.t("Operator")
	case store.PresetCustom:
		return s.t("Custom")
	}
	return preset
}

type option struct {
	Value, Label, Help string
	Selected           bool
}

func (s *Server) roleOptions(selected string) []option {
	var out []option
	for _, r := range store.Roles {
		out = append(out, option{Value: r, Label: s.roleName(r), Help: s.roleHelp(r), Selected: r == selected})
	}
	return out
}

// accountProblem says what went wrong with an account form, in the console's words.
func (s *Server) accountProblem(err error) string {
	switch {
	case errors.Is(err, store.ErrBadUsername):
		return s.t("The username: use 2 to 32 lower-case letters, digits, dots, dashes and underscores, starting with a letter or a digit.")
	case errors.Is(err, store.ErrReservedUsername):
		return s.t("That username is reserved; choose another.")
	case errors.Is(err, store.ErrBadDisplayName):
		return s.t("The display name is at most 64 characters.")
	case errors.Is(err, store.ErrPasswordShort):
		return s.t("The password is shorter than 10 characters.")
	case errors.Is(err, store.ErrPasswordLong):
		return s.t("The password is too long.")
	case errors.Is(err, store.ErrPasswordUsername):
		return s.t("The password may not be the username.")
	case errors.Is(err, store.ErrPasswordCommon):
		return s.t("That password is one of the commonest; choose another.")
	case errors.Is(err, store.ErrExists):
		return s.t("Someone has this username; choose another.")
	case errors.Is(err, store.ErrLinkExpired):
		return s.t("This link has expired; ask an owner for a new one.")
	case errors.Is(err, store.ErrBadLink):
		return s.t("This link does not work: it was used already, or it is wrong. Ask an owner for a new one.")
	case errors.Is(err, store.ErrHasAccounts):
		return s.t("Beacon is set up already: sign in, or ask an owner for an invitation.")
	case errors.Is(err, store.ErrBadCredentials):
		return s.t("The current password is wrong.")
	case errors.Is(err, store.ErrLastOwner):
		return s.t("Beacon would be left without an owner; make someone else owner first.")
	}
	return s.t("That did not work; the log says why.")
}

// ---- setting up, joining, resetting

type accountFormLabels struct {
	Username, UsernameHint, DisplayName, DisplayHint, Password, PasswordHint, Again, Submit, SignIn string
}

type accountFormPage struct {
	shell
	L                               accountFormLabels
	Kind, Action, Code, Form, Error string
	Heading, Intro, Note            string
	Username, DisplayName           string
	Broken                          bool // the link does not work: nothing to fill in
	AskUsername                     bool
	SignInHref                      string
}

var accountFormTmpl = parse("accountform.html")

func (s *Server) accountFormLabels(kind string) accountFormLabels {
	l := accountFormLabels{Username: s.t("Username"), UsernameHint: s.t("2 to 32 lower-case letters, digits, dots, dashes and underscores. It names you in the audit log and cannot change later."),
		DisplayName: s.t("Display name"), DisplayHint: s.t("How the console shows you; your username when empty."),
		Password: s.t("Password"), PasswordHint: s.t("At least 10 characters; not your username, not one of the commonest passwords."),
		Again: s.t("Password again"), SignIn: s.t("Sign in")}
	switch kind {
	case store.LinkSetup:
		l.Submit = s.t("Set Beacon up")
	case store.LinkInvite:
		l.Submit = s.t("Join")
	default:
		l.Password, l.Submit = s.t("New password"), s.t("Change the password")
	}
	return l
}

func linkPath(kind string) string {
	switch kind {
	case store.LinkSetup:
		return consolePath("/setup")
	case store.LinkInvite:
		return consolePath("/join")
	}
	return consolePath("/reset")
}

// accountForm shows the form a one-time link opens, with a fresh secret of the sign-in forms'.
func (s *Server) accountForm(w http.ResponseWriter, r *http.Request, status int, kind, code, username, displayName, problem string) {
	p := accountFormPage{shell: s.bare(r, s.t("Sign in")), L: s.accountFormLabels(kind), Kind: kind, Action: linkPath(kind), Code: code,
		Form: s.formSecret(w), Error: problem, Username: username, DisplayName: displayName, AskUsername: kind != store.LinkReset,
		SignInHref: consolePath("/sign-in")}
	l, err := s.Store.Link(kind, code)
	if kind == store.LinkSetup && s.Store.HasAccounts() {
		err = store.ErrHasAccounts
	}
	if err != nil {
		p.Broken, p.Error = true, s.accountProblem(err)
	}
	switch kind {
	case store.LinkSetup:
		p.Heading, p.Title = s.t("Set Beacon up"), s.t("Set Beacon up")+" – Beacon"
		p.Intro = s.t("Make the first account. It is an owner: it invites people, sets their roles and manages every agent.")
	case store.LinkInvite:
		p.Heading, p.Title = s.t("Join Beacon"), s.t("Join Beacon")+" – Beacon"
		if !p.Broken {
			p.Intro = s.t("%s invites you as %s: %s", l.CreatedBy, s.roleName(l.Role), s.roleHelp(l.Role))
			p.Note = l.Note
		}
	default:
		p.Heading, p.Title = s.t("Choose a new password"), s.t("Choose a new password")+" – Beacon"
		if !p.Broken {
			p.Intro = s.t("For %s. Every sign-in of this account ends.", l.User)
		}
	}
	s.renderStatus(w, status, accountFormTmpl, p)
}

func (s *Server) accountFormPage(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers(w)
		s.accountForm(w, r, http.StatusOK, kind, r.URL.Query().Get("code"), "", "", "")
	}
}

func (s *Server) accountFormPost(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers(w)
		r.Body = http.MaxBytesReader(w, r.Body, maxForm)
		if err := r.ParseForm(); err != nil {
			s.accountForm(w, r, http.StatusBadRequest, kind, "", "", "", s.t("The form has expired"))
			return
		}
		f := r.PostForm
		code, username, display := f.Get("code"), strings.ToLower(strings.TrimSpace(f.Get("username"))), f.Get("display_name")
		if !formOK(r) {
			s.accountForm(w, r, http.StatusForbidden, kind, code, username, display, s.t("The form has expired; try again."))
			return
		}
		if s.Auth != nil {
			if d, blocked := s.Auth.BlockedFor(r); blocked {
				s.accountForm(w, r, http.StatusTooManyRequests, kind, code, username, display, s.lockedOutFromHere(d))
				return
			}
		}
		if f.Get("password") != f.Get("again") {
			s.accountForm(w, r, http.StatusBadRequest, kind, code, username, display, s.t("The passwords differ."))
			return
		}
		var (
			a   store.Account
			err error
		)
		if kind == store.LinkReset {
			a, err = s.Bridge.Reset(r.Context(), code, f.Get("password"))
		} else {
			a, err = s.Bridge.Join(r.Context(), code, username, display, f.Get("password"))
		}
		if err != nil {
			if errors.Is(err, store.ErrBadLink) && s.Auth != nil {
				s.Auth.Fail(r) // guessing codes counts like guessing tokens
			}
			s.accountForm(w, r, http.StatusBadRequest, kind, code, username, display, s.accountProblem(err))
			return
		}
		s.startSession(w, r, a, consolePath(""))
	}
}

// ---- your account

type accountLabels struct {
	Username, DisplayName, Role, LastSignIn, Created, Change, Current, New, Again, Save, Agents, TokenNote, YourAgents string
}

type accountPage struct {
	shell
	L      accountLabels
	Facts  []kvLink
	Person bool
	Help   string
	Agents string
}

var accountTmpl = parse("account.html")

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	p := accountPage{shell: s.shellFor(r, v, "account", s.t("Your account")), Person: v.person(), L: accountLabels{
		Change: s.t("Change your password"), Current: s.t("Current password"), New: s.t("New password"), Again: s.t("New password again"),
		Save: s.t("Change the password"), YourAgents: s.t("Your agents"),
		TokenNote: s.t("You signed in with a token. People sign in with an account: an owner invites you from the People page, or with beacon user invite on the host.")}}
	if a := v.Account; a != nil {
		p.Facts = []kvLink{{K: s.t("Username"), V: a.Username}, {K: s.t("Display name"), V: a.DisplayName},
			{K: s.t("Role"), V: s.roleName(a.Role) + " · " + s.roleHelp(a.Role)}, {K: s.t("Created"), V: s.clock(a.Created)}}
		if !a.LastSignIn.IsZero() {
			p.Facts = append(p.Facts, kvLink{K: s.t("Last sign-in"), V: s.clock(a.LastSignIn)})
		}
		p.Facts = append(p.Facts, kvLink{K: s.t("Agents"), V: num(len(s.Store.Agents(a.Username))), Href: consolePath("/agents")})
		p.Help = s.t("A new password ends every other sign-in of yours.")
	} else {
		p.Facts = []kvLink{{K: s.t("Token"), V: v.Token.Name}, {K: s.t("Scopes"), V: strings.Join(v.Principal.Scopes, ", ")},
			{K: s.t("Expires"), V: s.clock(v.Token.Expires)}}
	}
	s.render(w, accountTmpl, p)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, v *viewer) {
	f := r.PostForm
	switch {
	case !v.person():
		back(w, r, consolePath("/account"), "error", "forbidden")
		return
	case f.Get("password") != f.Get("again"):
		back(w, r, consolePath("/account"), "error", "differ")
		return
	case s.Auth != nil && s.Auth.Blocked(r): // guessing the current password counts like guessing a token
		back(w, r, consolePath("/account"), "error", "blocked")
		return
	}
	err := s.Bridge.ChangePassword(r.Context(), v.actor(), f.Get("current"), f.Get("password"), v.ID)
	if err != nil {
		if errors.Is(err, store.ErrBadCredentials) && s.Auth != nil {
			s.Auth.Fail(r)
		}
		back(w, r, consolePath("/account"), "error", passwordCode(err))
		return
	}
	back(w, r, consolePath("/account"), "done", "password")
}

// passwordCode names what is wrong with a password, for the page to say (problem).
func passwordCode(err error) string {
	switch {
	case errors.Is(err, store.ErrPasswordShort):
		return "password_short"
	case errors.Is(err, store.ErrPasswordLong):
		return "password_long"
	case errors.Is(err, store.ErrPasswordUsername):
		return "password_username"
	case errors.Is(err, store.ErrPasswordCommon):
		return "password_common"
	case errors.Is(err, store.ErrBadCredentials):
		return "password_wrong"
	}
	return "failed"
}

// ---- people

type peopleLabels struct {
	Person, Role, LastSignIn, Agents, Status, Disabled, Enabled, Disable, Enable, Reset, Change, Invite, InviteHelp, Note, NoteHint, Send,
	Pending, Expires, By, Withdraw, NoInvitations, Never, You string
}

type personRow struct {
	Username, DisplayName, Role, LastSignIn, Agents, AgentsHref string
	Disabled, Self                                              bool
	Roles                                                       []option
	RoleAction, DisableAction, EnableAction, ResetAction        string
}

type invitationRow struct{ Role, Note, By, Expires, Withdraw string }

type peoplePage struct {
	shell
	L           peopleLabels
	Rows        []personRow
	Invitations []invitationRow
	Roles       []option
}

var peopleTmpl = parse("people.html")

func (s *Server) people(w http.ResponseWriter, r *http.Request, v *viewer) {
	users, invs, err := s.Bridge.Users(v.actor())
	if err != nil {
		s.forbidden(w, r, v, "accounts:write")
		return
	}
	p := peoplePage{shell: s.shellFor(r, v, "people", s.t("People")), Roles: s.roleOptions("oncall"), L: peopleLabels{
		Person: s.t("Who"), Role: s.t("Role"), LastSignIn: s.t("Last sign-in"), Agents: s.t("Agents"), Status: s.t("Status"),
		Disabled: s.t("Disabled"), Enabled: s.t("Enabled"), Disable: s.t("Disable"), Enable: s.t("Enable"), Reset: s.t("Password reset link"),
		Change: s.t("Change"), Invite: s.t("Invite someone"), InviteHelp: s.t("A one-time link, valid for 24 hours, with which the person chooses their username and password. Send it to them only."),
		Note: s.t("Note"), NoteHint: s.t("Who it is for, as the list of invitations shows it"), Send: s.t("Make the link"),
		Pending: s.t("Invitations not yet used"), Expires: s.t("Expires"), By: s.t("By"), Withdraw: s.t("Withdraw"),
		NoInvitations: s.t("No invitation is waiting."), Never: s.t("Never"), You: s.t("you")}}
	p.Count = num(len(users))
	for _, u := range users {
		path := consolePath("/people/") + url.PathEscape(u.Username)
		row := personRow{Username: u.Username, DisplayName: u.DisplayName, Role: s.roleName(u.Role), Agents: num(len(u.Agents)),
			Disabled: u.Disabled, Self: v.person() && v.Account.Username == u.Username, Roles: s.roleOptions(u.Role),
			RoleAction: path + "/role", DisableAction: path + "/disable", EnableAction: path + "/enable", ResetAction: path + "/reset",
			AgentsHref: consolePath("/agents") + "?owner=" + url.QueryEscape(u.Username)}
		if !u.LastSignIn.IsZero() {
			row.LastSignIn = s.clock(u.LastSignIn)
		}
		p.Rows = append(p.Rows, row)
	}
	for _, l := range invs {
		p.Invitations = append(p.Invitations, invitationRow{Role: s.roleName(l.Role), Note: l.Note, By: l.CreatedBy, Expires: s.clock(l.Expires),
			Withdraw: consolePath("/people/invitations/") + url.PathEscape(l.ID) + "/withdraw"})
	}
	s.render(w, peopleTmpl, p)
}

// oncePage shows something once: a link or a token that is not kept, with what to do with it.
type oncePage struct {
	shell
	Value, Warning, Text string
	Snippets             []bridge.Snippet
	SnippetsTitle        string
	Back, BackHref       string
	Mono                 bool
}

var onceTmpl = parse("once.html")

func (s *Server) invite(w http.ResponseWriter, r *http.Request, v *viewer) {
	l, err := s.Bridge.Invite(r.Context(), v.actor(), r.PostForm.Get("role"), r.PostForm.Get("note"))
	if err != nil {
		s.failed(w, r, consolePath("/people"), "invite", err)
		return
	}
	p := oncePage{shell: s.shellFor(r, v, "people", s.t("Invitation"), crumb{Label: s.t("People"), Href: consolePath("/people")}), Value: l.URL,
		Text:    s.t("An invitation to join as %s, valid until %s. Send the link to that person only: it works once.", s.roleName(l.Role), s.clock(l.Expires)),
		Warning: s.t("The link is shown only now; Beacon keeps only a hash of it."), Back: s.t("Back to the people"), BackHref: consolePath("/people")}
	s.renderStatus(w, http.StatusCreated, onceTmpl, p)
}

func (s *Server) withdraw(w http.ResponseWriter, r *http.Request, v *viewer) {
	if err := s.Bridge.WithdrawInvitation(r.Context(), v.actor(), r.PathValue("id")); err != nil {
		s.failed(w, r, consolePath("/people"), "withdraw", err)
		return
	}
	back(w, r, consolePath("/people"), "done", "withdrawn")
}

func (s *Server) setRole(w http.ResponseWriter, r *http.Request, v *viewer) {
	role := r.PostForm.Get("role")
	if _, err := s.Bridge.UpdateUser(r.Context(), v.actor(), r.PathValue("name"), bridge.UserChange{Role: &role}); err != nil {
		s.failed(w, r, consolePath("/people"), "role", err)
		return
	}
	back(w, r, consolePath("/people"), "done", "role")
}

func (s *Server) setDisabled(disabled bool) func(http.ResponseWriter, *http.Request, *viewer) {
	return func(w http.ResponseWriter, r *http.Request, v *viewer) {
		name := r.PathValue("name")
		if disabled && v.person() && v.Account.Username == name {
			back(w, r, consolePath("/people"), "error", "self")
			return
		}
		if _, err := s.Bridge.UpdateUser(r.Context(), v.actor(), name, bridge.UserChange{Disabled: &disabled}); err != nil {
			s.failed(w, r, consolePath("/people"), "disable", err)
			return
		}
		done := "enabled"
		if disabled {
			done = "disabled"
		}
		back(w, r, consolePath("/people"), "done", done)
	}
}

func (s *Server) resetLink(w http.ResponseWriter, r *http.Request, v *viewer) {
	l, err := s.Bridge.ResetLink(r.Context(), v.actor(), r.PathValue("name"))
	if err != nil {
		s.failed(w, r, consolePath("/people"), "reset", err)
		return
	}
	p := oncePage{shell: s.shellFor(r, v, "people", s.t("Password reset link"), crumb{Label: s.t("People"), Href: consolePath("/people")}), Value: l.URL,
		Text:    s.t("With this link %s chooses a new password, once, until %s; every sign-in of theirs ends then.", l.User, s.clock(l.Expires)),
		Warning: s.t("The link is shown only now; Beacon keeps only a hash of it."), Back: s.t("Back to the people"), BackHref: consolePath("/people")}
	s.renderStatus(w, http.StatusCreated, onceTmpl, p)
}
