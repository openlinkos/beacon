package bridge

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

// People and their agents: the operations the console, the REST API, MCP and the command line offer on
// accounts, invitations, agents, their tokens, OAuth grants and SSH keys. Each is checked here (who may
// act on whose), audited and echoed to the chat; secrets and the codes of links never reach the chat or
// the audit log.
//
// An owner (accounts:write) manages everyone; a person manages their own agents; an agent manages its
// own SSH keys; the command line on the host may do everything.

// ErrForbidden means the caller may not act on this.
var ErrForbidden = errors.New("not allowed")

// UserView is an account as the API shows it: never its password's hash.
type UserView struct {
	Username     string    `json:"username"`
	DisplayName  string    `json:"display_name"`
	Role         string    `json:"role"`
	FeishuOpenID string    `json:"feishu_open_id,omitempty"`
	Created      time.Time `json:"created"`
	CreatedBy    string    `json:"created_by,omitempty"`
	LastSignIn   time.Time `json:"last_sign_in,omitzero"`
	Disabled     bool      `json:"disabled"`
	Agents       []string  `json:"agents,omitempty"`
	// The person's own machine access: every machine for an owner; every machine that is not critical,
	// and the critical machines named on the account, for anyone else.
	MachineAccessView
}

// LinkView is a one-time link: an invitation, the setup link or a password reset. URL, with the code, is
// shown only when the link is made.
type LinkView struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Role      string    `json:"role,omitempty"`
	Note      string    `json:"note,omitempty"`
	User      string    `json:"user,omitempty"`
	URL       string    `json:"url,omitempty"`
	Created   time.Time `json:"created"`
	CreatedBy string    `json:"created_by,omitempty"`
	Expires   time.Time `json:"expires"`
}

// SSHKeyView is a key for the SSH channel.
type SSHKeyView struct {
	Key         string    `json:"key"`
	Fingerprint string    `json:"fingerprint"`
	Comment     string    `json:"comment,omitempty"`
	Added       time.Time `json:"added,omitzero"`
	Via         string    `json:"via,omitempty"` // the agent's own credential that added it (token:NAME, grant:ID); the key goes with it
}

// AgentTokenView is one of an agent's static tokens.
type AgentTokenView struct {
	Name     string    `json:"name"`
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"`
	Lifetime string    `json:"lifetime"`
	Renewed  time.Time `json:"renewed,omitzero"`
}

// GrantView is one of an agent's OAuth grants.
type GrantView struct {
	ID     string `json:"id"`
	Client string `json:"client"`
	Scope  string `json:"scope"`
	Preset string `json:"preset"` // what the person allowed it, within what the agent may do
	MachineAccessView
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"last_used,omitzero"`
	Expires  time.Time `json:"expires"`
}

// AgentView is an agent as its owner sees it: what its preset asks for (Wanted) and what it may do now
// (Scopes, within its owner's role), its machine access (as it was given; never more than its owner
// reaches), keys and credentials.
type AgentView struct {
	Name   string   `json:"name"`
	Owner  string   `json:"owner"`
	Actor  string   `json:"actor"`
	Preset string   `json:"preset"`
	Wanted []string `json:"wanted"`
	Scopes []string `json:"scopes"`
	MachineAccessView
	SSHKeys   []SSHKeyView     `json:"ssh_keys,omitempty"`
	Tokens    []AgentTokenView `json:"tokens,omitempty"`
	Grants    []GrantView      `json:"grants,omitempty"`
	Client    string           `json:"client,omitempty"`
	Created   time.Time        `json:"created"`
	CreatedBy string           `json:"created_by,omitempty"`
}

// Snippet is a ready-made way to connect an agent: a command or a configuration block.
type Snippet struct {
	Key   string `json:"key"`   // claude-code, codex, curl, ssh
	Title string `json:"title"` // what it is for, in English
	Text  string `json:"text"`
}

// AgentCreated is an agent just made, or given a new token: the token's secret, shown once, and the
// snippets that use it.
type AgentCreated struct {
	Agent    AgentView       `json:"agent"`
	Token    *AgentTokenView `json:"token,omitempty"`
	Secret   string          `json:"secret,omitempty"`
	Snippets []Snippet       `json:"snippets,omitempty"`
}

// AgentInput makes an agent. Owner defaults to the person making it; Preset to the strongest their role
// allows; TTL is its first token's lifetime (zero: no token, as for an OAuth client). Machines is its
// machine access, none unless given; a critical machine named in it is reached for
// store.CriticalGrantTTL unless it gives an end, or Forever names the machine.
type AgentInput struct {
	Name     string
	Owner    string
	Preset   string
	Scopes   []string // PresetCustom only; see checkCustom
	Machines store.MachineAccess
	Forever  []string
	SSHKeys  []string // authorized_keys lines
	TTL      time.Duration
}

// UserChange changes an account; nil fields stay.
type UserChange struct {
	Role         *string `json:"role,omitempty"`
	Disabled     *bool   `json:"disabled,omitempty"`
	DisplayName  *string `json:"display_name,omitempty"`
	FeishuOpenID *string `json:"feishu_open_id,omitempty"`
}

// roleName is how the chat names a role.
func (b *Bridge) roleName(role string) string {
	switch role {
	case "viewer":
		return b.t("Viewer")
	case "oncall":
		return b.t("On call")
	case "operator":
		return b.t("Operator")
	case "owner":
		return b.t("Owner")
	}
	return role
}

// presetName is how the chat names an agent's preset.
func (b *Bridge) presetName(preset string) string {
	switch preset {
	case "readonly":
		return b.t("Read only")
	case "oncall":
		return b.t("On call")
	case "operator":
		return b.t("Operator")
	case store.PresetCustom:
		return b.t("Custom")
	}
	return preset
}

// Recheck closes what is open on the SSH channel for whoever may no longer reach it (Deps.Recheck).
func (b *Bridge) Recheck() {
	if b.d.Recheck != nil {
		b.d.Recheck()
	}
}

// caller resolves who acts, afresh.
func (b *Bridge) caller(who Actor) (store.Principal, bool) {
	if who.Key == "" {
		return store.Principal{}, false
	}
	return b.d.Store.PrincipalWith(who.Key, who.Cred)
}

// admin reports whether who may manage identities: the local command line, an owner account, or a
// standalone token with accounts:write. Agents handle their own credentials through separate methods.
func (b *Bridge) admin(who Actor) bool {
	if who.Via == "cli" && who.Key == "" {
		return true
	}
	p, ok := b.caller(who)
	return ok && p.Agent == "" && p.Has("accounts:write")
}

// mayManage reports whether who may manage the agents of owner: an admin, or owner themself.
func (b *Bridge) mayManage(who Actor, owner string) bool {
	if b.admin(who) {
		return true
	}
	p, ok := b.caller(who)
	return ok && p.Person() && p.User == owner
}

// manageableAgent is the agent called name, for who may manage it. Permission comes before existence:
// a caller who is not an admin gets ErrForbidden alike for another person's agent and for a name that
// no agent has, so that the answer tells them nothing about what exists. An admin learns that an agent
// does not exist (store.ErrNotFound).
func (b *Bridge) manageableAgent(who Actor, name string) (store.Agent, error) {
	if !b.admin(who) {
		p, ok := b.caller(who)
		if !ok || !p.Person() {
			return store.Agent{}, ErrForbidden
		}
		a, found := b.d.Store.Agent(name)
		if !found || a.Owner != p.User {
			return store.Agent{}, fmt.Errorf("%w: you manage only your own agents, and %q is not one of them", ErrForbidden, name)
		}
		return a, nil
	}
	a, ok := b.d.Store.Agent(name)
	if !ok {
		return store.Agent{}, fmt.Errorf("%w: no agent %q", store.ErrNotFound, name)
	}
	return a, nil
}

// linkMaker is who makes an invitation or a reset link, as the link records it: the command line, or
// the principal the caller is. The link works only while that principal could still make it.
func linkMaker(who Actor) store.LinkMaker {
	if who.Via == "cli" && who.Key == "" {
		return store.LinkMaker{Name: who.Name, Key: store.LinkCLI}
	}
	return store.LinkMaker{Name: who.Name, Key: who.Key}
}

// withinRole reports whether who may act on an account with role, or give someone that role: the
// command line may; anyone else, read afresh, must hold every scope the role gives, and reach what the
// role reaches (store.RoleWithin). acc is the account acted on, whose critical machines count too; nil
// for an invitation. So a token with accounts:write but less than an owner's scopes cannot invite an
// owner, reset an owner's password or change an owner; only an owner gives or acts on the owner role, or
// acts on someone with critical machines named; and a role that reaches machines is given or acted on
// only by a caller with access to all machines. It changes nothing.
func (b *Bridge) withinRole(who Actor, role string, acc *store.Account) error {
	if cliActor(who) {
		return nil
	}
	caller, ok := b.caller(who)
	if !ok {
		return ErrForbidden
	}
	for _, sc := range store.RoleScopes(role) {
		if !caller.Has(sc) {
			return fmt.Errorf("%w: the %s role gives %s, which you do not hold", ErrForbidden, role, sc)
		}
	}
	var named store.MachineAccess
	if acc != nil {
		named = acc.CriticalAccess().Live(b.now())
	}
	switch err := store.RoleWithin(role, named, caller.Owner(), caller.MachineAccess); {
	case errors.Is(err, store.ErrRoleMachines):
		return fmt.Errorf("%w: the %s role reaches every machine that is not critical, so giving it, or acting on someone who has it, needs access to all machines; this credential reaches only the machines it names", ErrForbidden, role)
	case errors.Is(err, store.ErrNamedCritical):
		return fmt.Errorf("%w: %s reaches critical machines named on their account (%s); only an owner, or beacon on the host, acts on them", ErrForbidden, acc.Username, strings.Join(named.Machines, ", "))
	case err != nil:
		return fmt.Errorf("%w: %v", ErrForbidden, err) //nolint:errorlint // one wrapped error is enough
	}
	return nil
}

// mayEquip reports whether who may give agent a a new credential (a token) or an SSH key: its owner, a
// person; beacon on the host; or a caller, read afresh, that holds every scope the agent's preset asks
// for and whose machine access covers the agent's, the critical machines it names included. So a new
// credential never carries more than whoever made it holds, now or after the owner's role changes. It
// changes nothing.
func (b *Bridge) mayEquip(who Actor, a store.Agent) error {
	if cliActor(who) {
		return nil
	}
	p, ok := b.caller(who)
	if !ok {
		return ErrForbidden
	}
	if p.Person() && p.User == a.Owner {
		return nil
	}
	for _, sc := range a.Wanted() {
		if !p.Has(sc) {
			return fmt.Errorf("%w: agent %s may use %s, which you do not hold; its owner, an owner, or beacon on the host gives it a credential", ErrForbidden, a.Name, sc)
		}
	}
	critical := b.d.Store.CriticalMachines()
	if !a.Live(b.now()).Within(p.MachineAccess, func(m string) bool { return critical[m] }) {
		return fmt.Errorf("%w: agent %s reaches machines you do not (%s); its owner, an owner, or beacon on the host gives it a credential", ErrForbidden, a.Name, a.Live(b.now()).Summary())
	}
	return nil
}

// checkCustom reports whether scopes may be an agent's custom scopes: at least one, each a scope of
// people (store.PeopleScopes), each within the agent's owner's role, and, except for the command line,
// each a scope the caller itself holds. It changes nothing.
func (b *Bridge) checkCustom(who Actor, owner store.Account, scopes []string) error {
	if !b.admin(who) {
		return fmt.Errorf("%w: custom scopes need accounts:write from a person or the command line, never an agent", ErrForbidden)
	}
	if len(scopes) == 0 {
		return fmt.Errorf("%w: a custom agent needs at least one scope", ErrBadInput)
	}
	cli := who.Via == "cli" && who.Key == ""
	var caller store.Principal
	if !cli {
		var ok bool
		caller, ok = b.caller(who)
		if !ok {
			return fmt.Errorf("%w: custom scopes need accounts:write from a person or the command line", ErrForbidden)
		}
	}
	allowed := store.RoleScopes(owner.Role)
	for _, sc := range scopes {
		if !slices.Contains(store.PeopleScopes, sc) {
			return fmt.Errorf("%w: an agent's scope is one of the people's scopes, not %q", ErrBadInput, sc)
		}
		if !slices.Contains(allowed, sc) {
			return fmt.Errorf("%w: %s gives more than %s's role (%s) allows", ErrBadInput, sc, owner.Username, owner.Role)
		}
		if !cli && !caller.Has(sc) {
			return fmt.Errorf("%w: you cannot grant %s; you do not hold it", ErrForbidden, sc)
		}
	}
	return nil
}

// consoleURL is the address of a console page with a one-time code.
func (b *Bridge) consoleURL(page, code string) string {
	return b.apiURL(link.Root + page + "?code=" + url.QueryEscape(code))
}

func linkView(l store.AccountLink, u string) LinkView {
	return LinkView{ID: l.ID(), Kind: l.Kind, Role: l.Role, Note: l.Note, User: l.User, URL: u, Created: l.Created, CreatedBy: l.CreatedBy, Expires: l.Expires}
}

func (b *Bridge) userView(a store.Account) UserView {
	v := UserView{Username: a.Username, DisplayName: a.DisplayName, Role: a.Role, FeishuOpenID: a.FeishuOpenID, Created: a.Created,
		CreatedBy: a.CreatedBy, LastSignIn: a.LastSignIn, Disabled: a.Disabled, MachineAccessView: AccessView(a.MachineAccess().Live(b.now()))}
	for _, ag := range b.d.Store.Agents(a.Username) {
		v.Agents = append(v.Agents, ag.Name)
	}
	return v
}

// ---- accounts

// Users lists the accounts and the pending invitations; for an admin only.
func (b *Bridge) Users(who Actor) ([]UserView, []LinkView, error) {
	if !b.admin(who) {
		return nil, nil, fmt.Errorf("%w: listing people needs an owner account or a standalone token with accounts:write", ErrForbidden)
	}
	users := []UserView{}
	for _, a := range b.d.Store.Accounts() {
		users = append(users, b.userView(a))
	}
	invs := []LinkView{}
	for _, l := range b.d.Store.Invitations() {
		invs = append(invs, linkView(l, ""))
	}
	return users, invs, nil
}

// User returns one account; for an admin, or the person themself.
func (b *Bridge) User(who Actor, username string) (UserView, error) {
	if !b.mayManage(who, username) {
		return UserView{}, ErrForbidden
	}
	a, ok := b.d.Store.Account(username)
	if !ok {
		return UserView{}, fmt.Errorf("%w: no account %q", store.ErrNotFound, username)
	}
	return b.userView(a), nil
}

// Invite makes an invitation to join with role, valid for a day; the link is shown only now. The caller
// must hold every scope of the role and reach what it reaches (withinRole), and the invitation works
// only while it still does. It is made with its audit entry, or not at all (ErrUnrecorded).
func (b *Bridge) Invite(ctx context.Context, who Actor, role, note string) (LinkView, error) {
	if !b.admin(who) {
		return LinkView{}, fmt.Errorf("%w: inviting people needs an owner account or a standalone token with accounts:write", ErrForbidden)
	}
	if store.ValidRole(role) {
		if err := b.withinRole(who, role, nil); err != nil {
			return LinkView{}, err
		}
	}
	code, l, err := b.d.Store.CreateInvitationRecorded(role, note, linkMaker(who), func(l store.AccountLink) []store.AuditEntry {
		return []store.AuditEntry{{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_invite", Target: l.ID(), Detail: role + " " + l.Note}}
	})
	switch {
	case errors.Is(err, store.ErrUnrecorded):
		return LinkView{}, unrecorded("user_invite", role)
	case err != nil:
		return LinkView{}, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	b.echo(ctx, b.t("%s invited someone to join as %s via %s", who.Name, b.roleName(role), b.viaName(who.Via)))
	return linkView(l, b.consoleURL("/join", code)), nil
}

// WithdrawInvitation deletes a pending invitation.
func (b *Bridge) WithdrawInvitation(ctx context.Context, who Actor, id string) error {
	return b.withdraw(ctx, who, id, b.d.Store.WithdrawInvitation)
}

// WithdrawLink deletes a pending invitation or password reset link, by its ID (LinkView.ID), for an
// admin. Withdrawing only takes access away, so any admin may.
func (b *Bridge) WithdrawLink(ctx context.Context, who Actor, id string) (LinkView, error) {
	var gone store.AccountLink
	err := b.withdraw(ctx, who, id, func(id string) (store.AccountLink, error) {
		l, err := b.d.Store.WithdrawLink(id)
		gone = l
		return l, err
	})
	if err != nil {
		return LinkView{}, err
	}
	return linkView(gone, ""), nil
}

func (b *Bridge) withdraw(ctx context.Context, who Actor, id string, del func(string) (store.AccountLink, error)) error {
	if !b.admin(who) {
		return fmt.Errorf("%w: withdrawing a link needs an owner account or a standalone token with accounts:write", ErrForbidden)
	}
	l, err := del(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: no pending link %q", store.ErrNotFound, id)
		}
		return err
	}
	if l.Kind == store.LinkReset {
		_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_reset_withdraw", Target: l.User, Detail: id})
		b.echo(ctx, b.t("%s withdrew a password reset link for %s via %s", who.Name, l.User, b.viaName(who.Via)))
		return nil
	}
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_invite_withdraw", Target: id, Detail: l.Role})
	b.echo(ctx, b.t("%s withdrew an invitation via %s", who.Name, b.viaName(who.Via)))
	return nil
}

// ResetLinks lists the password reset links that still work, the newest first; for an admin only. Their
// codes are never shown again.
func (b *Bridge) ResetLinks(who Actor) ([]LinkView, error) {
	if !b.admin(who) {
		return nil, fmt.Errorf("%w: listing reset links needs an owner account or a standalone token with accounts:write", ErrForbidden)
	}
	out := []LinkView{}
	for _, l := range b.d.Store.ResetLinks() {
		out = append(out, linkView(l, ""))
	}
	return out, nil
}

// UpdateUser changes someone's role, display name or contact, or disables or enables them. A lowered role
// or a disabled account limits their sessions and agents at once, and withdraws the links they made;
// Beacon always keeps an enabled owner. The caller must hold every scope of the account's role, and of
// the role it is given, and reach what those reach (withinRole). A raised role or an enabled account is
// written with its audit entry, or not at all (ErrUnrecorded); an account that changed meanwhile is
// refused, to be looked at again.
func (b *Bridge) UpdateUser(ctx context.Context, who Actor, username string, ch UserChange) (UserView, error) {
	if !b.admin(who) {
		return UserView{}, fmt.Errorf("%w: changing people needs an owner account or a standalone token with accounts:write", ErrForbidden)
	}
	before, ok := b.d.Store.Account(username)
	if !ok {
		return UserView{}, fmt.Errorf("%w: no account %q", store.ErrNotFound, username)
	}
	if err := b.withinRole(who, before.Role, &before); err != nil {
		return UserView{}, err
	}
	if ch.Role != nil && store.ValidRole(*ch.Role) {
		if err := b.withinRole(who, *ch.Role, &before); err != nil {
			return UserView{}, err
		}
	}
	recorded := map[string]bool{} // the entries written with the change
	a, err := b.d.Store.UpdateAccountRecorded(username, func(a *store.Account) error {
		if a.Role != before.Role || a.Disabled != before.Disabled || !slices.Equal(a.CriticalMachines, before.CriticalMachines) {
			return fmt.Errorf("%w: %s changed meanwhile; look again and retry", ErrBadInput, username)
		}
		if ch.Role != nil {
			a.Role = *ch.Role
		}
		if ch.Disabled != nil {
			a.Disabled = *ch.Disabled
		}
		if ch.DisplayName != nil {
			a.DisplayName = *ch.DisplayName
		}
		if ch.FeishuOpenID != nil {
			id := strings.TrimSpace(*ch.FeishuOpenID)
			if len(id) > 64 || strings.ContainsFunc(id, func(r rune) bool { return r <= ' ' || r > '~' }) {
				return errors.New("a Feishu open_id is up to 64 printable characters")
			}
			a.FeishuOpenID = id
		}
		return nil
	}, func(prev, after store.Account) []store.AuditEntry {
		var out []store.AuditEntry
		if store.RoleRaised(prev.Role, after.Role) {
			out = append(out, store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_role", Target: username, Detail: prev.Role + " -> " + after.Role})
			recorded["user_role"] = true
		}
		if prev.Disabled && !after.Disabled {
			out = append(out, store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_enable", Target: username})
			recorded["user_enable"] = true
		}
		return out
	})
	switch {
	case errors.Is(err, store.ErrUnrecorded):
		return UserView{}, unrecorded("user_change", username)
	case errors.Is(err, store.ErrLastOwner), errors.Is(err, store.ErrNotFound), errors.Is(err, ErrBadInput):
		return UserView{}, err
	case err != nil:
		return UserView{}, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	b.Recheck() // a lowered role or a disabled account limits the agents at once
	via := b.viaName(who.Via)
	if a.Role != before.Role {
		if !recorded["user_role"] {
			_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_role", Target: username, Detail: before.Role + " -> " + a.Role})
		}
		b.echo(ctx, b.t("%s changed the role of %s to %s via %s", who.Name, username, b.roleName(a.Role), via))
	}
	if a.Disabled != before.Disabled {
		action, text := "user_enable", b.t("%s enabled %s via %s", who.Name, username, via)
		if a.Disabled {
			action, text = "user_disable", b.t("%s disabled %s via %s", who.Name, username, via)
		}
		if !recorded[action] {
			_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: action, Target: username})
		}
		b.echo(ctx, text)
	}
	if a.DisplayName != before.DisplayName || a.FeishuOpenID != before.FeishuOpenID {
		_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_update", Target: username})
	}
	return b.userView(a), nil
}

// ResetLink makes a one-time link, valid 30 minutes, with which username chooses a new password. The
// caller must hold every scope of the account's role and reach what the person reaches (withinRole), and
// the link works only while it still does. It is made with its audit entry, or not at all
// (ErrUnrecorded).
func (b *Bridge) ResetLink(ctx context.Context, who Actor, username string) (LinkView, error) {
	if !b.admin(who) {
		return LinkView{}, fmt.Errorf("%w: resetting a password needs an owner account or a standalone token with accounts:write", ErrForbidden)
	}
	if a, ok := b.d.Store.Account(username); ok {
		if err := b.withinRole(who, a.Role, &a); err != nil {
			return LinkView{}, err
		}
	}
	code, l, err := b.d.Store.CreateResetLinkRecorded(username, linkMaker(who), func(store.AccountLink) []store.AuditEntry {
		return []store.AuditEntry{{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_reset_link", Target: username}}
	})
	switch {
	case errors.Is(err, store.ErrUnrecorded):
		return LinkView{}, unrecorded("user_reset_link", username)
	case err != nil:
		return LinkView{}, err
	}
	b.echo(ctx, b.t("%s made a password reset link for %s via %s", who.Name, username, b.viaName(who.Via)))
	return linkView(l, b.consoleURL("/reset", code)), nil
}

// SetupLink makes the one-time link that creates the first account, an owner, valid 30 minutes; only
// while Beacon has no account (store.ErrHasAccounts). via is start (written to the log) or cli.
func (b *Bridge) SetupLink(via string) (LinkView, error) {
	code, l, err := b.d.Store.CreateSetupLink()
	if err != nil {
		return LinkView{}, err
	}
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: "beacon", Via: via, Action: "setup_link", Detail: "expires " + l.Expires.Format(time.RFC3339)})
	return linkView(l, b.consoleURL("/setup", code)), nil
}

// Join creates an account with a setup link or an invitation.
func (b *Bridge) Join(ctx context.Context, code, username, displayName, password string) (store.Account, error) {
	a, err := b.d.Store.Join(code, username, displayName, password)
	if err != nil {
		return store.Account{}, err
	}
	action := "user_join"
	if a.CreatedBy == "setup" {
		action = "setup"
	}
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: a.Username, Via: "console", Action: action, Target: a.Username, Detail: a.Role})
	if action == "setup" {
		b.echo(ctx, b.t("%s set Beacon up and is its first owner", a.Username))
	} else {
		b.echo(ctx, b.t("%s joined Beacon as %s, invited by %s", a.Username, b.roleName(a.Role), a.CreatedBy))
	}
	return a, nil
}

// Reset gives an account a new password with a reset link (store.ResetAccount): its console sessions and
// the OAuth sign-ins of its agents end, its own SSH keys are removed, and what those had open on machines
// is closed. With revokeTokens the tokens of its agents are revoked too; otherwise they keep working.
func (b *Bridge) Reset(ctx context.Context, code, password string, revokeTokens bool) (store.Account, error) {
	a, keys, err := b.d.Store.ResetAccount(code, password)
	if err != nil {
		return store.Account{}, err
	}
	var revoked []string
	if revokeTokens {
		for _, t := range b.PersonTokens(a.Username) {
			if b.d.Store.RevokeToken(t.Name) == nil {
				b.d.Store.DeleteConsoleSessions(t.Name)
				revoked = append(revoked, t.Name)
			}
		}
	}
	b.Recheck()
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: a.Username, Via: "console", Action: "user_reset", Target: a.Username})
	b.echo(ctx, b.t("%s chose a new password with a reset link", a.Username))
	for _, name := range revoked {
		_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: a.Username, Via: "console", Action: "revoke_token", Target: name, Detail: "with a password reset"})
	}
	if len(revoked) > 0 {
		b.echo(ctx, b.t("%s revoked the tokens of their agents with the reset: %s", a.Username, strings.Join(revoked, ", ")))
	}
	var removed []string
	for _, k := range keys {
		removed = append(removed, k.Fingerprint)
		_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: a.Username, Via: "console", Action: "ssh_key_remove", Target: a.Username, Detail: k.Fingerprint + " with a password reset"})
	}
	if len(removed) > 0 {
		b.echo(ctx, b.t("The reset removed the SSH keys of %s: %s", a.Username, strings.Join(removed, ", ")))
	}
	return a, nil
}

// PersonTokens lists the static tokens of a person's agents that have not expired: what a new password
// leaves working.
func (b *Bridge) PersonTokens(username string) []store.Token {
	var out []store.Token
	now := b.now()
	for _, ag := range b.d.Store.Agents(username) {
		for _, t := range b.d.Store.AgentTokens(ag.Name) {
			if now.Before(t.Expires) {
				out = append(out, t)
			}
		}
	}
	return out
}

// ChangePassword changes the caller's own password; the console session keep stays signed in.
func (b *Bridge) ChangePassword(_ context.Context, who Actor, current, password, keep string) error {
	p, ok := b.caller(who)
	if !ok || !p.Person() {
		return ErrForbidden
	}
	if err := b.d.Store.ChangePassword(p.User, current, password, keep); err != nil {
		return err
	}
	b.Recheck() // the OAuth sign-ins of their agents ended, and so does what those had open on machines
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_password", Target: p.User})
	return nil
}

// ---- agents

func (b *Bridge) agentView(a store.Agent) AgentView {
	v := AgentView{Name: a.Name, Owner: a.Owner, Actor: a.Owner + " / " + a.Name, Preset: a.Preset, Wanted: a.Wanted(), Scopes: []string{},
		MachineAccessView: AccessView(a.MachineAccess), Client: a.Client, Created: a.Created, CreatedBy: a.CreatedBy}
	if owner, ok := b.d.Store.Account(a.Owner); ok && !owner.Disabled {
		for _, sc := range a.Wanted() {
			if slices.Contains(store.RoleScopes(owner.Role), sc) {
				v.Scopes = append(v.Scopes, sc)
			}
		}
	}
	for _, k := range a.SSHKeys {
		v.SSHKeys = append(v.SSHKeys, SSHKeyView(k))
	}
	for _, t := range b.d.Store.AgentTokens(a.Name) {
		v.Tokens = append(v.Tokens, AgentTokenView{Name: t.Name, Created: t.Created, Expires: t.Expires, Lifetime: store.FormatTTL(t.Lifetime), Renewed: t.Renewed})
	}
	for _, g := range b.d.Store.Grants(a.Name) {
		v.Grants = append(v.Grants, GrantView{ID: g.ID, Client: g.ClientName, Scope: g.Scope, Preset: g.Preset, MachineAccessView: AccessView(g.MachineAccess),
			Created: g.Created, LastUsed: g.LastUsed, Expires: g.Expires})
	}
	return v
}

// Agents lists the agents who may see: an admin everyone's (or owner's), a person their own.
func (b *Bridge) Agents(who Actor, owner string) ([]AgentView, error) {
	if !b.admin(who) {
		p, ok := b.caller(who)
		if !ok || !p.Person() || (owner != "" && owner != p.User) {
			return nil, ErrForbidden
		}
		owner = p.User
	}
	out := []AgentView{}
	for _, a := range b.d.Store.Agents(owner) {
		out = append(out, b.agentView(a))
	}
	return out, nil
}

// Agent returns one agent, for its owner or an admin.
func (b *Bridge) Agent(who Actor, name string) (AgentView, error) {
	a, err := b.manageableAgent(who, name)
	if err != nil {
		return AgentView{}, err
	}
	return b.agentView(a), nil
}

// CreateAgent makes an agent for a person (in.Owner; the caller when it is a person), with a first
// token when in.TTL is set, and returns the token's secret, shown once, with the snippets that use it. A
// person makes agents for themself; an admin for anyone. The preset may not exceed the owner's role. A
// named preset's scopes, and the machine access, must be within what the caller may give
// (withinCaller); custom scopes go through checkCustom. The command line is exempt.
func (b *Bridge) CreateAgent(ctx context.Context, who Actor, in AgentInput) (AgentCreated, error) {
	if in.Owner == "" {
		if p, ok := b.caller(who); ok && p.Person() {
			in.Owner = p.User
		}
	}
	if !b.mayManage(who, in.Owner) {
		return AgentCreated{}, fmt.Errorf("%w: a person makes agents for themself; an owner for anyone", ErrForbidden)
	}
	owner, ok := b.d.Store.Account(in.Owner)
	if !ok {
		return AgentCreated{}, fmt.Errorf("%w: no account %q", store.ErrNotFound, in.Owner)
	}
	if in.Preset == "" {
		in.Preset = store.PresetFor(owner.Role)
	}
	grant := store.PresetScopes(in.Preset)
	if in.Preset == store.PresetCustom {
		if err := b.checkCustom(who, owner, in.Scopes); err != nil {
			return AgentCreated{}, err
		}
		grant = in.Scopes
	} else if !store.PresetWithin(in.Preset, owner.Role) {
		return AgentCreated{}, fmt.Errorf("%w: preset %s gives more than %s's role (%s) allows; choose %s or less", ErrBadInput, in.Preset, owner.Username, owner.Role, store.PresetFor(owner.Role))
	}
	in.Machines = b.agentAccess(in.Machines, store.MachineAccess{}, in.Forever)
	if err := b.withinCaller(who, grant, in.Machines); err != nil {
		return AgentCreated{}, err
	}
	var keys []store.SSHKey
	for _, line := range in.SSHKeys {
		k, err := store.ParseSSHKey(line)
		if err != nil {
			return AgentCreated{}, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
		}
		if b.d.Store.KeyHolder(k.Fingerprint) != "" {
			return AgentCreated{}, fmt.Errorf("%w: the key %s is already registered; use a key of the agent's own", ErrBadInput, k.Fingerprint)
		}
		if _, ok := b.d.Store.MachineByConnectorKey(k.Fingerprint); ok {
			return AgentCreated{}, fmt.Errorf("%w: the key %s is already registered; use a key of the agent's own", ErrBadInput, k.Fingerprint)
		}
		keys = append(keys, k)
	}
	a, err := b.d.Store.CreateAgent(store.Agent{Name: in.Name, Owner: in.Owner, Preset: in.Preset, Scopes: in.Scopes, MachineAccess: in.Machines, CreatedBy: who.Name})
	switch {
	case errors.Is(err, store.ErrExists):
		return AgentCreated{}, err
	case err != nil:
		return AgentCreated{}, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	for _, k := range keys {
		k.Added = b.now().UTC()
		with, err := b.d.Store.AddAgentSSHKey(a.Name, k)
		if err != nil { // taken meanwhile: no agent half made
			_ = b.d.Store.DeleteAgent(a.Name)
			return AgentCreated{}, err
		}
		a = with
	}
	if err := b.recordGrant(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "agent_create", Target: a.Name,
		Detail: a.Owner + " " + a.Preset + " " + a.String()}); err != nil {
		_ = b.d.Store.DeleteAgent(a.Name) // not made without its record
		return AgentCreated{}, err
	}
	b.echo(ctx, b.t("%s made agent %s for %s (%s) via %s", who.Name, a.Name, a.Owner, b.presetName(a.Preset), b.viaName(who.Via)))
	out := AgentCreated{Agent: b.agentView(a)}
	if in.TTL > 0 {
		return b.issueAgentToken(ctx, who, a, in.TTL)
	}
	return out, nil
}

// AgentUpdate changes an agent. A nil field stays as it is. Scopes is nil to keep them; a non-nil list
// sets the custom scopes (and is refused when it is empty). Scopes are applied only when the result is
// custom; a preset clears them. Machines is the new machine access; a machine it keeps keeps its end,
// and a critical machine newly named ends as for AgentInput, unless Forever names it.
type AgentUpdate struct {
	Preset   *string
	Machines *store.MachineAccess
	Forever  []string
	Scopes   []string
}

// UpdateAgent sets what an agent may do and the machines it reaches, for its owner or an admin. The
// preset may not exceed the owner's role. Custom scopes follow checkCustom: a person or a legacy token
// with accounts:write, or the command line, never an agent, and never beyond the owner's role or the
// caller's own scopes. A change that would give the agent a scope it did not want, or a machine it did
// not reach, must stay within what the caller holds. Narrowing keeps working. A refusal changes nothing
// and is not audited. The command line is exempt.
func (b *Bridge) UpdateAgent(ctx context.Context, who Actor, name string, up AgentUpdate) (AgentView, error) {
	a, err := b.manageableAgent(who, name)
	if err != nil {
		return AgentView{}, err
	}
	owner, ok := b.d.Store.Account(a.Owner)
	if !ok {
		return AgentView{}, ErrForbidden
	}
	preset := a.Preset
	if up.Preset != nil && *up.Preset != "" {
		preset = *up.Preset
	}
	machines := a.Live(b.now()) // a named machine whose access has ended is not kept, nor counted as new
	if up.Machines != nil {
		machines = b.agentAccess(*up.Machines, a.Live(b.now()), up.Forever)
	}
	scopes := a.Scopes
	if preset == store.PresetCustom {
		// A list was sent, or the agent is moving onto custom: both are setting scopes, checked before
		// anything is written. An agent that is already custom and sends no list keeps its scopes.
		if up.Scopes != nil || a.Preset != store.PresetCustom {
			scopes = up.Scopes
			if err := b.checkCustom(who, owner, scopes); err != nil {
				return AgentView{}, err
			}
		}
	} else if !store.PresetWithin(preset, owner.Role) {
		return AgentView{}, fmt.Errorf("%w: preset %s gives more than %s's role (%s) allows", ErrBadInput, preset, owner.Username, owner.Role)
	}
	wanted := scopes
	if preset != store.PresetCustom {
		wanted = store.PresetScopes(preset)
	}
	// Only a change that gives the agent something new is held to the caller. Narrowing, or leaving it
	// as it is, stays allowed: the caller is not granting a scope or a machine.
	widens := b.grantWidens(a, wanted, machines)
	if widens {
		if err := b.withinCaller(who, wanted, machines); err != nil {
			return AgentView{}, err
		}
	}
	// A change that widens is written with its audit entry, or not at all; one that narrows is recorded
	// afterwards, and goes ahead whatever the audit log says. The agent checked above must be the one
	// changed: one that changed meanwhile is refused, to be looked at again.
	before := agentChangeText(a)
	a, err = b.d.Store.UpdateAgentRecorded(name, func(x *store.Agent) error {
		if agentChangeText(*x) != before {
			return fmt.Errorf("agent %s changed meanwhile; look again and retry", name)
		}
		x.Preset, x.MachineAccess = preset, machines
		if preset == store.PresetCustom {
			x.Scopes = scopes
		} else {
			x.Scopes = nil
		}
		return nil
	}, func(_, x store.Agent) []store.AuditEntry {
		if !widens || agentChangeText(x) == before {
			return nil
		}
		return []store.AuditEntry{{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "agent_update", Target: name, Detail: before + " -> " + agentChangeText(x)}}
	})
	switch {
	case errors.Is(err, store.ErrUnrecorded):
		return AgentView{}, unrecorded("agent_update", name)
	case err != nil:
		return AgentView{}, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	after := agentChangeText(a)
	if before == after {
		return b.agentView(a), nil
	}
	b.Recheck()
	if !widens {
		_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "agent_update", Target: name, Detail: before + " -> " + after})
	}
	b.echo(ctx, b.t("%s changed agent %s to %s, %s, via %s", who.Name, name, b.presetName(a.Preset), b.accessWords(a.MachineAccess), b.viaName(who.Via)))
	return b.agentView(a), nil
}

// agentChangeText is an agent's preset, its custom scopes and its machines, for the audit log.
func agentChangeText(a store.Agent) string {
	text := a.Preset
	if a.Preset == store.PresetCustom && len(a.Scopes) > 0 {
		text += " " + strings.Join(a.Scopes, ",")
	}
	return text + " " + a.String()
}

// NewAgentToken gives an agent another static token, for its owner, or an admin who holds what the
// agent may use (mayEquip).
func (b *Bridge) NewAgentToken(ctx context.Context, who Actor, name string, ttl time.Duration) (AgentCreated, error) {
	a, err := b.manageableAgent(who, name)
	if err != nil {
		return AgentCreated{}, err
	}
	if err := b.mayEquip(who, a); err != nil {
		return AgentCreated{}, err
	}
	return b.issueAgentToken(ctx, who, a, ttl)
}

func (b *Bridge) issueAgentToken(ctx context.Context, who Actor, a store.Agent, ttl time.Duration) (AgentCreated, error) {
	secret, t, err := b.d.Store.CreateAgentToken(a.Name, ttl, who.Name)
	if err != nil {
		return AgentCreated{}, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	if err := b.recordGrant(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "agent_token", Target: t.Name,
		Detail: a.Name + " expires " + t.Expires.UTC().Format(time.DateOnly)}); err != nil {
		_ = b.d.Store.RevokeToken(t.Name) // not issued without its record
		return AgentCreated{}, err
	}
	b.echo(ctx, b.t("%s gave agent %s a token, expires %s, via %s", who.Name, a.Name, t.Expires.In(b.d.Config.Location).Format(time.DateOnly), b.viaName(who.Via)))
	tv := AgentTokenView{Name: t.Name, Created: t.Created, Expires: t.Expires, Lifetime: store.FormatTTL(t.Lifetime)}
	a, _ = b.d.Store.Agent(a.Name)
	return AgentCreated{Agent: b.agentView(a), Token: &tv, Secret: secret, Snippets: b.Snippets(secret, a.Name)}, nil
}

// DeleteAgent forgets an agent with all its credentials, for its owner or an admin.
func (b *Bridge) DeleteAgent(ctx context.Context, who Actor, name string) error {
	a, err := b.manageableAgent(who, name)
	if err != nil {
		return err
	}
	if err := b.d.Store.DeleteAgent(name); err != nil {
		return err
	}
	b.Recheck()
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "agent_delete", Target: name, Detail: a.Owner})
	b.echo(ctx, b.t("%s deleted agent %s of %s via %s", who.Name, name, a.Owner, b.viaName(who.Via)))
	return nil
}

// RevokeAgentToken revokes one of an agent's static tokens, for the agent's owner or an admin.
func (b *Bridge) RevokeAgentToken(ctx context.Context, who Actor, tokenName string) error {
	t, ok := b.token(tokenName)
	if !ok || t.Agent == "" {
		return fmt.Errorf("%w: no agent token %q", store.ErrNotFound, tokenName)
	}
	a, ok := b.d.Store.Agent(t.Agent)
	if !ok || !b.mayManage(who, a.Owner) {
		return ErrForbidden
	}
	return b.revokeToken(ctx, who, tokenName)
}

// RevokeGrant revokes an OAuth grant, for its agent's owner or an admin.
func (b *Bridge) RevokeGrant(ctx context.Context, who Actor, id string) error {
	g, ok := b.d.Store.Grant(id)
	if !ok {
		return fmt.Errorf("%w: no grant %q", store.ErrNotFound, id)
	}
	a, ok := b.d.Store.Agent(g.Agent)
	if !ok || !b.mayManage(who, a.Owner) {
		return ErrForbidden
	}
	if err := b.d.Store.RevokeGrant(id); err != nil {
		return err
	}
	b.Recheck()
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "oauth_revoke", Target: a.Name, Detail: g.ClientName})
	b.echo(ctx, b.t("%s revoked %s's sign-in to Beacon as agent %s via %s", who.Name, clean(g.ClientName, maxShort, false), a.Name, b.viaName(who.Via)))
	return nil
}

// ClaimToken makes a legacy token an agent's of owner (an admin only): see store.ClaimToken.
func (b *Bridge) ClaimToken(ctx context.Context, who Actor, token, owner, agent, preset string) (AgentView, error) {
	if !b.admin(who) {
		return AgentView{}, fmt.Errorf("%w: claiming tokens needs an owner account or a standalone token with accounts:write", ErrForbidden)
	}
	if preset != "" && preset != store.PresetCustom && !store.ValidPreset(preset) {
		return AgentView{}, fmt.Errorf("%w: unknown preset %q", ErrBadInput, preset)
	}
	a, _, err := b.d.Store.ClaimToken(token, owner, agent, preset, who.Name)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrExists):
		return AgentView{}, err
	case errors.Is(err, store.ErrClaimWidens):
		return AgentView{}, fmt.Errorf("%w: %w", ErrBadInput, err)
	case err != nil:
		return AgentView{}, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	b.Recheck()
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "token_claim", Target: token, Detail: owner + " / " + a.Name + " " + a.Preset})
	b.echo(ctx, b.t("%s claimed token %s as agent %s of %s via %s", who.Name, token, a.Name, owner, b.viaName(who.Via)))
	return b.agentView(a), nil
}

// LegacyTokens lists the tokens from before accounts that nobody has claimed (store.Legacy).
func (b *Bridge) LegacyTokens() []store.Token {
	var out []store.Token
	for _, t := range b.d.Store.Tokens() {
		if store.Legacy(t) {
			out = append(out, t)
		}
	}
	return out
}

// OAuthAgent makes or reuses the agent a person allows an OAuth client to act as, and says what the grant
// may do: a new agent is made with the preset and machine access allowed; an existing agent of the
// person's, by its name, is never changed, and the grant may do no more than it may (a preset no
// stronger than its own, machine access within its own), so that allowing a client never widens an agent
// or its other credentials. The preset may not exceed the person's role, and the machine access is held
// to the person as any other (withinCaller): only an owner names a critical machine, and it is reached for
// store.CriticalGrantTTL at most. It returns the agent and the grant's machine access. Audited and echoed
// to the chat.
func (b *Bridge) OAuthAgent(ctx context.Context, who Actor, clientID, clientName, name, preset string, machines store.MachineAccess, until time.Time) (store.Agent, store.MachineAccess, error) {
	p, ok := b.caller(who)
	if !ok || !p.Person() {
		return store.Agent{}, store.MachineAccess{}, fmt.Errorf("%w: a person allows a client, signed in with their account", ErrForbidden)
	}
	owner, ok := b.d.Store.Account(p.User)
	if !ok {
		return store.Agent{}, store.MachineAccess{}, ErrForbidden
	}
	if !store.PresetWithin(preset, owner.Role) {
		return store.Agent{}, store.MachineAccess{}, fmt.Errorf("%w: preset %s gives more than your role (%s) allows", ErrBadInput, preset, owner.Role)
	}
	a, exists := b.d.Store.Agent(name)
	machines = b.agentAccess(machines, store.MachineAccess{}, nil)
	for m, end := range machines.MachineUntil {
		if end.After(until) { // never beyond the sign-in itself
			machines.MachineUntil[m] = until.UTC()
		}
	}
	if exists && a.Owner == p.User { // never beyond the reused agent's own access to a machine
		machines = capEnds(machines, a.Live(b.now()))
	}
	if err := b.withinCaller(who, store.PresetScopes(preset), machines); err != nil {
		return store.Agent{}, store.MachineAccess{}, err
	}
	var err error
	switch {
	case exists && a.Owner != p.User:
		return store.Agent{}, store.MachineAccess{}, fmt.Errorf("%w: agent %s is someone else's; choose another name", store.ErrExists, name)
	case exists:
		if a.Preset != store.PresetCustom && !store.PresetAtMost(preset, a.Preset) {
			return store.Agent{}, store.MachineAccess{}, fmt.Errorf("%w: agent %s may do only what %s allows; choose that or less, or change the agent first", ErrBadInput, name, a.Preset)
		}
		critical := b.d.Store.CriticalMachines()
		if !machines.Within(a.Live(b.now()), func(m string) bool { return critical[m] }) {
			return store.Agent{}, store.MachineAccess{}, fmt.Errorf("%w: agent %s reaches %s; allow that or less, or change the agent first", ErrBadInput, name, a.Live(b.now()).Summary())
		}
	default:
		a, err = b.d.Store.CreateAgent(store.Agent{Name: name, Owner: p.User, Preset: preset, MachineAccess: machines, Client: clientID, CreatedBy: who.Name})
	}
	if err != nil {
		if errors.Is(err, store.ErrExists) {
			return store.Agent{}, store.MachineAccess{}, err
		}
		return store.Agent{}, store.MachineAccess{}, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	if err := b.recordGrant(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "oauth_allow", Target: a.Name,
		Detail: clientName + " " + preset + " " + machines.String() + " until " + until.UTC().Format(time.DateOnly)}); err != nil {
		if !exists {
			_ = b.d.Store.DeleteAgent(a.Name) // not made without its record
		}
		return store.Agent{}, store.MachineAccess{}, err // and no sign-in either: no code is issued
	}
	b.echo(ctx, b.t("%s allowed %s to use Beacon as agent %s (%s) until %s", who.Name, clean(clientName, maxShort, false), a.Name, b.presetName(preset), until.In(b.d.Config.Location).Format(time.DateOnly)))
	return a, machines, nil
}

// capEnds gives every machine access names an end no later than within's for it, where within names the
// machine with an end: a sign-in of a reused agent reaches a machine for as long as the agent does, at
// most.
func capEnds(access, within store.MachineAccess) store.MachineAccess {
	out := store.MachineAccess{Access: access.Access, Machines: slices.Clone(access.Machines), MachineUntil: maps.Clone(access.MachineUntil)}
	for _, m := range out.Machines {
		limit := within.Until(m)
		if !within.Names(m) || limit.IsZero() {
			continue
		}
		if end := out.Until(m); end.IsZero() || end.After(limit) {
			if out.MachineUntil == nil {
				out.MachineUntil = map[string]time.Time{}
			}
			out.MachineUntil[m] = limit
		}
	}
	return out
}

// OAuthReplayed records that an authorization code or a refresh token was sent again after it was used,
// so that someone else may hold it: the grant it gave was revoked. what is code or refresh_token. Audited
// and echoed to the chat.
func (b *Bridge) OAuthReplayed(ctx context.Context, agent, client, what string) {
	b.Recheck()
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: "beacon", Via: "oauth", Action: "oauth_replay", Target: agent, Detail: client + " " + what})
	text := b.t("A used authorization code of agent %s (%s) was sent again; Beacon revoked that sign-in", agent, clean(client, maxShort, false))
	if what == "refresh_token" {
		text = b.t("A used refresh token of agent %s (%s) was sent again; Beacon revoked that sign-in", agent, clean(client, maxShort, false))
	}
	b.echo(ctx, text)
}

// ---- SSH keys

// keyOwner finds whose keys an operation is on: agent (named; the caller's own when ""), checked against
// who. It returns the agent, or the caller's own token for a token of its own.
type sshKeyOwner struct {
	agent   store.Agent
	token   string
	account *store.Account
}

func (b *Bridge) keyOwner(who Actor, agent string) (sshKeyOwner, error) {
	if agent == "" {
		p, ok := b.caller(who)
		switch {
		case !ok:
			return sshKeyOwner{}, ErrForbidden
		case p.Agent != "":
			agent = p.Agent
		case p.Person():
			a, found := b.d.Store.Account(p.User)
			if !found || a.Disabled {
				return sshKeyOwner{}, ErrForbidden
			}
			return sshKeyOwner{account: &a}, nil
		default:
			return sshKeyOwner{token: p.Token}, nil
		}
		a, ok := b.d.Store.Agent(agent)
		if !ok {
			return sshKeyOwner{}, store.ErrNotFound
		}
		return sshKeyOwner{agent: a}, nil
	}
	if p, ok := b.caller(who); ok && p.Agent != "" && p.Agent == agent { // an agent's own keys
		a, found := b.d.Store.Agent(agent)
		if !found {
			return sshKeyOwner{}, ErrForbidden
		}
		return sshKeyOwner{agent: a}, nil
	}
	a, err := b.manageableAgent(who, agent)
	if err != nil {
		return sshKeyOwner{}, err
	}
	return sshKeyOwner{agent: a}, nil
}

// SSHKeys lists an agent's keys or those of the caller when agent is empty.
func (b *Bridge) SSHKeys(who Actor, agent string) ([]SSHKeyView, error) {
	owner, err := b.keyOwner(who, agent)
	if err != nil {
		return nil, err
	}
	keys := owner.agent.SSHKeys
	switch {
	case owner.account != nil:
		keys = owner.account.SSHKeys
	case owner.token != "":
		t, _ := b.token(owner.token)
		keys = t.SSHKeys
	}
	out := []SSHKeyView{}
	for _, k := range keys {
		out = append(out, SSHKeyView(k))
	}
	return out, nil
}

// AddSSHKey gives an agent or the caller a public key for the SSH channel. A key for an agent, other than
// by the agent itself, is added by its owner, or an admin who holds what the agent may use (mayEquip).
func (b *Bridge) AddSSHKey(ctx context.Context, who Actor, agent, line, comment string) (SSHKeyView, error) {
	owner, err := b.keyOwner(who, agent)
	if err != nil {
		return SSHKeyView{}, err
	}
	if owner.account == nil && owner.token == "" {
		if p, ok := b.caller(who); !ok || p.Agent != owner.agent.Name {
			if err := b.mayEquip(who, owner.agent); err != nil {
				return SSHKeyView{}, err
			}
		}
	}
	k, err := store.ParseSSHKey(line)
	if err != nil {
		return SSHKeyView{}, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	if c := store.CleanComment(comment); c != "" {
		k.Comment = c
	} else if fields := strings.Fields(line); len(fields) > 2 {
		k.Comment = store.CleanComment(strings.Join(fields[2:], " "))
	}
	k.Added = b.now().UTC()
	if p, ok := b.caller(who); ok && p.Agent != "" && p.Agent == owner.agent.Name && who.Cred != "" {
		k.Via = who.Cred // an agent's own key lives as long as the credential it added it with
	}
	had := b.ownerHasKey(owner, k.Fingerprint)
	var holder string
	switch {
	case owner.account != nil:
		holder = owner.account.Username
		err = b.d.Store.AddAccountSSHKey(holder, k)
		if errors.Is(err, store.ErrDisabled) {
			err = ErrForbidden
		}
	case owner.token != "":
		holder = owner.token
		_, err = b.d.Store.AddTokenSSHKey(holder, k)
	default:
		holder = owner.agent.Owner + " / " + owner.agent.Name
		_, err = b.d.Store.AddAgentSSHKey(owner.agent.Name, k)
	}
	if err != nil {
		return SSHKeyView{}, err
	}
	if err := b.recordGrant(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "ssh_key_add", Target: holder, Detail: k.Fingerprint + " " + k.Comment}); err != nil {
		if !had { // not added without its record
			b.dropKey(owner, k.Fingerprint)
		}
		return SSHKeyView{}, err
	}
	b.echo(ctx, b.t("%s added SSH key %s to %s via %s", who.Name, k.Fingerprint, holder, b.viaName(who.Via)))
	return SSHKeyView(k), nil
}

// ownerHasKey reports whether the key's owner holds the key with this fingerprint already.
func (b *Bridge) ownerHasKey(owner sshKeyOwner, fp string) bool {
	has := func(keys []store.SSHKey) bool {
		return slices.ContainsFunc(keys, func(k store.SSHKey) bool { return k.Fingerprint == fp })
	}
	switch {
	case owner.account != nil:
		a, ok := b.d.Store.Account(owner.account.Username)
		return ok && has(a.SSHKeys)
	case owner.token != "":
		t, ok := b.token(owner.token)
		return ok && has(t.SSHKeys)
	default:
		a, ok := b.d.Store.Agent(owner.agent.Name)
		return ok && has(a.SSHKeys)
	}
}

// dropKey takes a key just added from its owner again.
func (b *Bridge) dropKey(owner sshKeyOwner, fp string) {
	switch {
	case owner.account != nil:
		_ = b.d.Store.RemoveAccountSSHKey(owner.account.Username, fp)
	case owner.token != "":
		_, _ = b.d.Store.RemoveTokenSSHKey(owner.token, fp)
	default:
		_, _ = b.d.Store.RemoveAgentSSHKey(owner.agent.Name, fp)
	}
}

// RemoveSSHKey takes a key, by its fingerprint, from an agent or from the caller.
func (b *Bridge) RemoveSSHKey(ctx context.Context, who Actor, agent, fp string) error {
	owner, err := b.keyOwner(who, agent)
	if err != nil {
		return err
	}
	var holder string
	switch {
	case owner.account != nil:
		holder = owner.account.Username
		err = b.d.Store.RemoveAccountSSHKey(holder, fp)
	case owner.token != "":
		holder = owner.token
		_, err = b.d.Store.RemoveTokenSSHKey(holder, fp)
	default:
		holder = owner.agent.Owner + " / " + owner.agent.Name
		_, err = b.d.Store.RemoveAgentSSHKey(owner.agent.Name, fp)
	}
	if err != nil {
		return err
	}
	b.Recheck()
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "ssh_key_remove", Target: holder, Detail: fp})
	b.echo(ctx, b.t("%s removed SSH key %s from %s via %s", who.Name, fp, holder, b.viaName(who.Via)))
	return nil
}

// ---- snippets

// Snippets are the ready-made ways to connect an agent with a token: Claude Code, Codex, curl, and the SSH
// configuration when the agent has a key and the channel is on.
func (b *Bridge) Snippets(secret, agent string) []Snippet {
	base := strings.TrimSuffix(b.d.Config.PublicURL, "/")
	if base == "" {
		base = "https://beacon.example.com"
	}
	out := []Snippet{
		{Key: "claude-code", Title: "Claude Code", Text: fmt.Sprintf(`claude mcp add --transport http beacon %s/mcp --header "Authorization: Bearer %s"`, base, secret)},
		{Key: "codex", Title: "Codex", Text: fmt.Sprintf("export BEACON_TOKEN=%s\ncodex mcp add beacon --url %s/mcp --bearer-token-env-var BEACON_TOKEN", secret, base)},
		{Key: "curl", Title: "curl", Text: fmt.Sprintf(`curl -H "Authorization: Bearer %s" %s/api/v1/token`, secret, base)},
	}
	if s := b.SSHConfig(agent); s != "" {
		out = append(out, Snippet{Key: "ssh", Title: "~/.ssh/config", Text: s})
	}
	return out
}

// SSHConfig is the ssh configuration for an agent that has a key: a block for each machine it reaches and
// the known_hosts line; empty without a key or without the channel.
func (b *Bridge) SSHConfig(agent string) string {
	a, ok := b.d.Store.Agent(agent)
	if !ok || len(a.SSHKeys) == 0 || b.d.SSHConfig == nil {
		return ""
	}
	return b.d.SSHConfig("agent:"+agent, "")
}

// PersonalSSHConfig gives a signed-in person the connection instructions for a machine.
func (b *Bridge) PersonalSSHConfig(who Actor, machine string) string {
	p, ok := b.caller(who)
	if !ok || !p.Person() || !p.Reaches(machine) || b.d.SSHConfig == nil {
		return ""
	}
	if a, ok := b.d.Store.Account(p.User); !ok || len(a.SSHKeys) == 0 {
		return ""
	}
	return b.d.SSHConfig(p.Key(), machine)
}
