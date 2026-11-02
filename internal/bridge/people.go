package bridge

import (
	"context"
	"errors"
	"fmt"
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
	ID       string    `json:"id"`
	Client   string    `json:"client"`
	Scope    string    `json:"scope"`
	Preset   string    `json:"preset"`             // what the person allowed it, within what the agent may do
	Machines []string  `json:"machines,omitempty"` // the machines allowed; none: the agent's
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"last_used,omitzero"`
	Expires  time.Time `json:"expires"`
}

// AgentView is an agent as its owner sees it: what its preset asks for (Wanted) and what it may do now
// (Scopes, within its owner's role), its machines, keys and credentials.
type AgentView struct {
	Name      string           `json:"name"`
	Owner     string           `json:"owner"`
	Actor     string           `json:"actor"`
	Preset    string           `json:"preset"`
	Wanted    []string         `json:"wanted"`
	Scopes    []string         `json:"scopes"`
	Machines  []string         `json:"machines,omitempty"`
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
// allows; TTL is its first token's lifetime (zero: no token, as for an OAuth client).
type AgentInput struct {
	Name     string
	Owner    string
	Preset   string
	Scopes   []string // PresetCustom only, from the command line
	Machines []string
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

// admin reports whether who may manage everyone: the command line, or accounts:write.
func (b *Bridge) admin(who Actor) bool {
	if who.Via == "cli" && who.Key == "" {
		return true
	}
	p, ok := b.caller(who)
	return ok && p.Has("accounts:write")
}

// mayManage reports whether who may manage the agents of owner: an admin, or owner themself.
func (b *Bridge) mayManage(who Actor, owner string) bool {
	if b.admin(who) {
		return true
	}
	p, ok := b.caller(who)
	return ok && p.Person() && p.User == owner
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
		CreatedBy: a.CreatedBy, LastSignIn: a.LastSignIn, Disabled: a.Disabled}
	for _, ag := range b.d.Store.Agents(a.Username) {
		v.Agents = append(v.Agents, ag.Name)
	}
	return v
}

// ---- accounts

// Users lists the accounts and the pending invitations; for an admin only.
func (b *Bridge) Users(who Actor) ([]UserView, []LinkView, error) {
	if !b.admin(who) {
		return nil, nil, fmt.Errorf("%w: listing people needs accounts:write", ErrForbidden)
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

// Invite makes an invitation to join with role, valid for a day; the link is shown only now.
func (b *Bridge) Invite(ctx context.Context, who Actor, role, note string) (LinkView, error) {
	if !b.admin(who) {
		return LinkView{}, fmt.Errorf("%w: inviting people needs accounts:write", ErrForbidden)
	}
	code, l, err := b.d.Store.CreateInvitation(role, note, who.Name)
	if err != nil {
		return LinkView{}, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_invite", Target: l.ID(), Detail: role + " " + l.Note})
	b.echo(ctx, b.t("%s invited someone to join as %s via %s", who.Name, b.roleName(role), b.viaName(who.Via)))
	return linkView(l, b.consoleURL("/join", code)), nil
}

// WithdrawInvitation deletes a pending invitation.
func (b *Bridge) WithdrawInvitation(ctx context.Context, who Actor, id string) error {
	if !b.admin(who) {
		return ErrForbidden
	}
	l, err := b.d.Store.WithdrawInvitation(id)
	if err != nil {
		return err
	}
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_invite_withdraw", Target: id, Detail: l.Role})
	b.echo(ctx, b.t("%s withdrew an invitation via %s", who.Name, b.viaName(who.Via)))
	return nil
}

// UpdateUser changes someone's role, display name or contact, or disables or enables them. A lowered role
// or a disabled account limits their sessions and agents at once; Beacon always keeps an enabled owner.
func (b *Bridge) UpdateUser(ctx context.Context, who Actor, username string, ch UserChange) (UserView, error) {
	if !b.admin(who) {
		return UserView{}, fmt.Errorf("%w: changing people needs accounts:write", ErrForbidden)
	}
	before, ok := b.d.Store.Account(username)
	if !ok {
		return UserView{}, fmt.Errorf("%w: no account %q", store.ErrNotFound, username)
	}
	a, err := b.d.Store.UpdateAccount(username, func(a *store.Account) error {
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
	})
	switch {
	case errors.Is(err, store.ErrLastOwner), errors.Is(err, store.ErrNotFound):
		return UserView{}, err
	case err != nil:
		return UserView{}, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	b.Recheck() // a lowered role or a disabled account limits the agents at once
	via := b.viaName(who.Via)
	if a.Role != before.Role {
		_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_role", Target: username, Detail: before.Role + " -> " + a.Role})
		b.echo(ctx, b.t("%s changed the role of %s to %s via %s", who.Name, username, b.roleName(a.Role), via))
	}
	if a.Disabled != before.Disabled {
		action, text := "user_enable", b.t("%s enabled %s via %s", who.Name, username, via)
		if a.Disabled {
			action, text = "user_disable", b.t("%s disabled %s via %s", who.Name, username, via)
		}
		_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: action, Target: username})
		b.echo(ctx, text)
	}
	if a.DisplayName != before.DisplayName || a.FeishuOpenID != before.FeishuOpenID {
		_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_update", Target: username})
	}
	return b.userView(a), nil
}

// ResetLink makes a one-time link, valid 30 minutes, with which username chooses a new password.
func (b *Bridge) ResetLink(ctx context.Context, who Actor, username string) (LinkView, error) {
	if !b.admin(who) {
		return LinkView{}, fmt.Errorf("%w: resetting a password needs accounts:write", ErrForbidden)
	}
	code, l, err := b.d.Store.CreateResetLink(username, who.Name)
	if err != nil {
		return LinkView{}, err
	}
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_reset_link", Target: username})
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

// Reset gives an account a new password with a reset link and ends its sessions.
func (b *Bridge) Reset(ctx context.Context, code, password string) (store.Account, error) {
	a, err := b.d.Store.Reset(code, password)
	if err != nil {
		return store.Account{}, err
	}
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: a.Username, Via: "console", Action: "user_reset", Target: a.Username})
	b.echo(ctx, b.t("%s chose a new password with a reset link", a.Username))
	return a, nil
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
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_password", Target: p.User})
	return nil
}

// ---- agents

func (b *Bridge) agentView(a store.Agent) AgentView {
	v := AgentView{Name: a.Name, Owner: a.Owner, Actor: a.Owner + " / " + a.Name, Preset: a.Preset, Wanted: a.Wanted(), Scopes: []string{},
		Machines: a.Machines, Client: a.Client, Created: a.Created, CreatedBy: a.CreatedBy}
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
		v.Grants = append(v.Grants, GrantView{ID: g.ID, Client: g.ClientName, Scope: g.Scope, Preset: g.Preset, Machines: g.Machines, Created: g.Created, LastUsed: g.LastUsed, Expires: g.Expires})
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
	a, ok := b.d.Store.Agent(name)
	if !ok {
		return AgentView{}, fmt.Errorf("%w: no agent %q", store.ErrNotFound, name)
	}
	if !b.mayManage(who, a.Owner) {
		return AgentView{}, ErrForbidden
	}
	return b.agentView(a), nil
}

// CreateAgent makes an agent for a person (in.Owner; the caller when it is a person), with a first
// token when in.TTL is set, and returns the token's secret, shown once, with the snippets that use it. A
// person makes agents for themself; an admin for anyone. The preset may not exceed the owner's role.
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
	if in.Preset != store.PresetCustom && !store.PresetWithin(in.Preset, owner.Role) {
		return AgentCreated{}, fmt.Errorf("%w: preset %s gives more than %s's role (%s) allows; choose %s or less", ErrBadInput, in.Preset, owner.Username, owner.Role, store.PresetFor(owner.Role))
	}
	if in.Preset == store.PresetCustom && !b.admin(who) {
		return AgentCreated{}, fmt.Errorf("%w: custom scopes are for the command line", ErrForbidden)
	}
	var keys []store.SSHKey
	for _, line := range in.SSHKeys {
		k, err := store.ParseSSHKey(line)
		if err != nil {
			return AgentCreated{}, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
		}
		if holder := b.d.Store.KeyHolder(k.Fingerprint); holder != "" {
			return AgentCreated{}, fmt.Errorf("%w: the key %s belongs to %s", ErrBadInput, k.Fingerprint, holder)
		}
		if m, ok := b.d.Store.MachineByConnectorKey(k.Fingerprint); ok {
			return AgentCreated{}, fmt.Errorf("%w: the key %s is machine %s's connector key", ErrBadInput, k.Fingerprint, m.Name)
		}
		keys = append(keys, k)
	}
	a, err := b.d.Store.CreateAgent(store.Agent{Name: in.Name, Owner: in.Owner, Preset: in.Preset, Scopes: in.Scopes, Machines: in.Machines, CreatedBy: who.Name})
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
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "agent_create", Target: a.Name,
		Detail: a.Owner + " " + a.Preset + " " + machinesText(a.Machines)})
	b.echo(ctx, b.t("%s made agent %s for %s (%s) via %s", who.Name, a.Name, a.Owner, b.presetName(a.Preset), b.viaName(who.Via)))
	out := AgentCreated{Agent: b.agentView(a)}
	if in.TTL > 0 {
		return b.issueAgentToken(ctx, who, a, in.TTL)
	}
	return out, nil
}

// machinesWords names the machines an agent reaches, for the chat.
func (b *Bridge) machinesWords(m []string) string {
	if len(m) == 0 {
		return b.t("all machines")
	}
	return strings.Join(m, ", ")
}

func machinesText(m []string) string {
	if len(m) == 0 {
		return "all machines"
	}
	return strings.Join(m, ",")
}

// UpdateAgent sets what an agent may do and the machines it reaches, for its owner or an admin. The
// preset may not exceed the owner's role; custom (a claimed token's scopes) stays as it is.
func (b *Bridge) UpdateAgent(ctx context.Context, who Actor, name, preset string, machines []string) error {
	a, ok := b.d.Store.Agent(name)
	if !ok {
		return fmt.Errorf("%w: no agent %q", store.ErrNotFound, name)
	}
	if !b.mayManage(who, a.Owner) {
		return ErrForbidden
	}
	owner, ok := b.d.Store.Account(a.Owner)
	if !ok {
		return ErrForbidden
	}
	if preset == "" {
		preset = a.Preset
	}
	if preset == store.PresetCustom && a.Preset != store.PresetCustom {
		return fmt.Errorf("%w: custom scopes are for the command line", ErrBadInput)
	}
	if preset != store.PresetCustom && !store.PresetWithin(preset, owner.Role) {
		return fmt.Errorf("%w: preset %s gives more than %s's role (%s) allows", ErrBadInput, preset, owner.Username, owner.Role)
	}
	before := a.Preset + " " + machinesText(a.Machines)
	a, err := b.d.Store.UpdateAgent(name, func(x *store.Agent) error {
		x.Preset, x.Machines = preset, machines
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	after := a.Preset + " " + machinesText(a.Machines)
	if before == after {
		return nil
	}
	b.Recheck()
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "agent_update", Target: name, Detail: before + " -> " + after})
	b.echo(ctx, b.t("%s changed agent %s to %s, %s, via %s", who.Name, name, b.presetName(a.Preset), b.machinesWords(a.Machines), b.viaName(who.Via)))
	return nil
}

// NewAgentToken gives an agent another static token, for its owner or an admin.
func (b *Bridge) NewAgentToken(ctx context.Context, who Actor, name string, ttl time.Duration) (AgentCreated, error) {
	a, ok := b.d.Store.Agent(name)
	if !ok {
		return AgentCreated{}, fmt.Errorf("%w: no agent %q", store.ErrNotFound, name)
	}
	if !b.mayManage(who, a.Owner) {
		return AgentCreated{}, ErrForbidden
	}
	return b.issueAgentToken(ctx, who, a, ttl)
}

func (b *Bridge) issueAgentToken(ctx context.Context, who Actor, a store.Agent, ttl time.Duration) (AgentCreated, error) {
	secret, t, err := b.d.Store.CreateAgentToken(a.Name, ttl, who.Name)
	if err != nil {
		return AgentCreated{}, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "agent_token", Target: t.Name,
		Detail: a.Name + " expires " + t.Expires.UTC().Format(time.DateOnly)})
	b.echo(ctx, b.t("%s gave agent %s a token, expires %s, via %s", who.Name, a.Name, t.Expires.In(b.d.Config.Location).Format(time.DateOnly), b.viaName(who.Via)))
	tv := AgentTokenView{Name: t.Name, Created: t.Created, Expires: t.Expires, Lifetime: store.FormatTTL(t.Lifetime)}
	a, _ = b.d.Store.Agent(a.Name)
	return AgentCreated{Agent: b.agentView(a), Token: &tv, Secret: secret, Snippets: b.Snippets(secret, a.Name)}, nil
}

// DeleteAgent forgets an agent with all its credentials, for its owner or an admin.
func (b *Bridge) DeleteAgent(ctx context.Context, who Actor, name string) error {
	a, ok := b.d.Store.Agent(name)
	if !ok {
		return fmt.Errorf("%w: no agent %q", store.ErrNotFound, name)
	}
	if !b.mayManage(who, a.Owner) {
		return ErrForbidden
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
	return b.RevokeToken(ctx, who, tokenName)
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
		return AgentView{}, fmt.Errorf("%w: claiming tokens needs accounts:write", ErrForbidden)
	}
	if preset != "" && preset != store.PresetCustom && !store.ValidPreset(preset) {
		return AgentView{}, fmt.Errorf("%w: unknown preset %q", ErrBadInput, preset)
	}
	a, _, err := b.d.Store.ClaimToken(token, owner, agent, preset, who.Name)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrExists):
		return AgentView{}, err
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
// may do: a new agent is made with the preset and machines allowed; an existing agent of the person's, by
// its name, is never changed, and the grant may do no more than it may (a preset no stronger than its
// own, machines among its own; all of them when none are named), so that allowing a client never widens
// an agent or its other credentials. The preset may not exceed the person's role either. It returns the
// agent and the grant's machines. Audited and echoed to the chat.
func (b *Bridge) OAuthAgent(ctx context.Context, who Actor, clientID, clientName, name, preset string, machines []string, until time.Time) (store.Agent, []string, error) {
	p, ok := b.caller(who)
	if !ok || !p.Person() {
		return store.Agent{}, nil, fmt.Errorf("%w: a person allows a client, signed in with their account", ErrForbidden)
	}
	owner, ok := b.d.Store.Account(p.User)
	if !ok {
		return store.Agent{}, nil, ErrForbidden
	}
	if !store.PresetWithin(preset, owner.Role) {
		return store.Agent{}, nil, fmt.Errorf("%w: preset %s gives more than your role (%s) allows", ErrBadInput, preset, owner.Role)
	}
	a, exists := b.d.Store.Agent(name)
	var err error
	switch {
	case exists && a.Owner != p.User:
		return store.Agent{}, nil, fmt.Errorf("%w: agent %s is someone else's; choose another name", store.ErrExists, name)
	case exists:
		if a.Preset != store.PresetCustom && !store.PresetAtMost(preset, a.Preset) {
			return store.Agent{}, nil, fmt.Errorf("%w: agent %s may do only what %s allows; choose that or less, or change the agent first", ErrBadInput, name, a.Preset)
		}
		if len(a.Machines) > 0 {
			if len(machines) == 0 {
				machines = slices.Clone(a.Machines)
			}
			for _, m := range machines {
				if !slices.Contains(a.Machines, m) {
					return store.Agent{}, nil, fmt.Errorf("%w: agent %s does not reach machine %s", ErrBadInput, name, m)
				}
			}
		}
	default:
		a, err = b.d.Store.CreateAgent(store.Agent{Name: name, Owner: p.User, Preset: preset, Machines: machines, Client: clientID, CreatedBy: who.Name})
	}
	if err != nil {
		if errors.Is(err, store.ErrExists) {
			return store.Agent{}, nil, err
		}
		return store.Agent{}, nil, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "oauth_allow", Target: a.Name,
		Detail: clientName + " " + preset + " " + machinesText(machines) + " until " + until.UTC().Format(time.DateOnly)})
	b.echo(ctx, b.t("%s allowed %s to use Beacon as agent %s (%s) until %s", who.Name, clean(clientName, maxShort, false), a.Name, b.presetName(preset), until.In(b.d.Config.Location).Format(time.DateOnly)))
	return a, machines, nil
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
func (b *Bridge) keyOwner(who Actor, agent string) (store.Agent, string, error) {
	if agent == "" {
		p, ok := b.caller(who)
		switch {
		case !ok:
			return store.Agent{}, "", ErrForbidden
		case p.Agent != "":
			agent = p.Agent
		case p.Token != "" && p.User == "":
			return store.Agent{}, p.Token, nil
		default:
			return store.Agent{}, "", fmt.Errorf("%w: a person reaches machines through an agent; add the key to one", ErrBadInput)
		}
		a, ok := b.d.Store.Agent(agent)
		if !ok {
			return store.Agent{}, "", store.ErrNotFound
		}
		return a, "", nil
	}
	a, ok := b.d.Store.Agent(agent)
	if !ok {
		return store.Agent{}, "", fmt.Errorf("%w: no agent %q", store.ErrNotFound, agent)
	}
	if !b.mayManage(who, a.Owner) {
		if p, ok := b.caller(who); !ok || p.Agent != agent {
			return store.Agent{}, "", ErrForbidden
		}
	}
	return a, "", nil
}

// SSHKeys lists the keys of an agent (the caller's own when ""), or of the caller's own token.
func (b *Bridge) SSHKeys(who Actor, agent string) ([]SSHKeyView, error) {
	a, token, err := b.keyOwner(who, agent)
	if err != nil {
		return nil, err
	}
	keys := a.SSHKeys
	if token != "" {
		t, _ := b.token(token)
		keys = t.SSHKeys
	}
	out := []SSHKeyView{}
	for _, k := range keys {
		out = append(out, SSHKeyView(k))
	}
	return out, nil
}

// AddSSHKey gives an agent (the caller's own when "") or the caller's own token a public key for the SSH
// channel. Shown to the agent's owner on the console and echoed to the chat.
func (b *Bridge) AddSSHKey(ctx context.Context, who Actor, agent, line, comment string) (SSHKeyView, error) {
	a, token, err := b.keyOwner(who, agent)
	if err != nil {
		return SSHKeyView{}, err
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
	if p, ok := b.caller(who); ok && p.Agent != "" && p.Agent == a.Name && who.Cred != "" {
		k.Via = who.Cred // an agent's own key lives as long as the credential it added it with
	}
	holder := token
	if token != "" {
		_, err = b.d.Store.AddTokenSSHKey(token, k)
	} else {
		holder = a.Owner + " / " + a.Name
		_, err = b.d.Store.AddAgentSSHKey(a.Name, k)
	}
	if err != nil {
		return SSHKeyView{}, err
	}
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "ssh_key_add", Target: holder, Detail: k.Fingerprint + " " + k.Comment})
	b.echo(ctx, b.t("%s added SSH key %s to %s via %s", who.Name, k.Fingerprint, holder, b.viaName(who.Via)))
	return SSHKeyView(k), nil
}

// RemoveSSHKey takes a key, by its fingerprint, from an agent (the caller's own when "") or the caller's
// own token.
func (b *Bridge) RemoveSSHKey(ctx context.Context, who Actor, agent, fp string) error {
	a, token, err := b.keyOwner(who, agent)
	if err != nil {
		return err
	}
	holder := token
	if token != "" {
		_, err = b.d.Store.RemoveTokenSSHKey(token, fp)
	} else {
		holder = a.Owner + " / " + a.Name
		_, err = b.d.Store.RemoveAgentSSHKey(a.Name, fp)
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
	return b.d.SSHConfig("agent:" + agent)
}
