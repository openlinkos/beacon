package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

// Who acts in Beacon. A person signs in with an account (accounts.go); an agent is owned by a person and
// acts with one of its credentials: static tokens (lbt_…) and OAuth grants (oauth.go), and its SSH keys
// on the SSH channel. A token of its own, not an agent's, is a machine's (a probe, a host, a heartbeat,
// the rule engine, a change sender, a metrics reader) or, from before accounts, a legacy token, which
// keeps working and can be claimed as an agent of a person.
//
// What a principal may do is resolved afresh on every request (Principal): an agent may do what its
// preset allows and its owner's role allows, the intersection, so that demoting or disabling a person
// limits their agents at once.

var bucketAgents = []byte("agents")

// PeopleScopes are the scopes of people and agents: the REST API, MCP and the SSH channel. The others
// in Scopes are machines', on the metrics gateway.
var PeopleScopes = []string{"status:read", "alerts:write", "incidents:write", "maintenance:write",
	"machines:ssh", "machines:write", "sessions:read", "config:write", "accounts:write", "secrets:admin"}

// Agent presets: what an agent may do, as a role says it for a person. An agent never does more than its
// owner's role allows. PresetCustom keeps a list of scopes of its own (a claimed legacy token's, or one
// an expert chose on the command line).
const PresetCustom = "custom"

// Presets are the agents' presets, weakest first.
var Presets = []string{"readonly", "oncall", "operator"}

var presetRoles = map[string]string{"readonly": "viewer", "oncall": "oncall", "operator": "operator"}

// PresetScopes are the scopes a preset gives; none for custom or an unknown one.
func PresetScopes(preset string) []string { return RoleScopes(presetRoles[preset]) }

// ValidPreset reports whether preset is one of Presets.
func ValidPreset(preset string) bool { return slices.Contains(Presets, preset) }

// PresetFor is the strongest preset within a role: what a person's agents get unless they choose less.
func PresetFor(role string) string {
	switch role {
	case "viewer":
		return "readonly"
	case "oncall":
		return "oncall"
	case "":
		return ""
	}
	return "operator"
}

// PresetAtMost reports whether preset gives nothing beyond the preset most.
func PresetAtMost(preset, most string) bool {
	return ValidPreset(preset) && ValidPreset(most) && slices.Index(Presets, preset) <= slices.Index(Presets, most)
}

// PresetWithin reports whether preset gives nothing beyond role.
func PresetWithin(preset, role string) bool {
	return ValidPreset(preset) && slices.Index(Presets, preset) <= slices.Index(Presets, PresetFor(role))
}

// ClosestPreset is the preset with exactly these scopes, or PresetCustom: claiming a token never widens
// what it may do.
func ClosestPreset(scopes []string) string {
	want := slices.Clone(scopes)
	slices.Sort(want)
	want = slices.Compact(want)
	for _, p := range Presets {
		got := PresetScopes(p)
		slices.Sort(got)
		if slices.Equal(got, want) {
			return p
		}
	}
	return PresetCustom
}

// Agent is a program that acts for a person: Owner. Its preset (or Scopes, for PresetCustom) says what
// it may do, never more than its owner's role; Machines are the machines it reaches on the SSH channel
// (none: all), and SSHKeys the keys it signs in there with. Client is the OAuth client it was made for,
// when a person allowed one to use Beacon.
type Agent struct {
	Name      string    `json:"name"`
	Owner     string    `json:"owner"`
	Preset    string    `json:"preset"`
	Scopes    []string  `json:"scopes,omitempty"`
	Machines  []string  `json:"machines,omitempty"`
	SSHKeys   []SSHKey  `json:"ssh_keys,omitempty"`
	Client    string    `json:"client,omitempty"`
	Created   time.Time `json:"created"`
	CreatedBy string    `json:"created_by,omitempty"`
}

// Wanted are the scopes its preset asks for, before its owner's role limits them.
func (a Agent) Wanted() []string {
	if a.Preset == PresetCustom {
		return a.Scopes
	}
	return PresetScopes(a.Preset)
}

var agentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,39}$`)

// ValidAgentName checks an agent's name: 2 to 40 lower-case letters, digits, dots, dashes and
// underscores, starting with a letter or a digit.
func ValidAgentName(name string) error {
	if !agentNameRe.MatchString(name) {
		return fmt.Errorf("agent name %q: use 2 to 40 lower-case letters, digits, dots, dashes and underscores, starting with a letter or a digit", name)
	}
	return nil
}

// AgentName makes an agent's name from a free text such as an OAuth client's name ("Claude Code" gives
// claude-code); "agent" when nothing is left.
func AgentName(text string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(text) {
		if b.Len() >= 32 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) < 2 {
		return "agent"
	}
	return name
}

// checkAgent checks an agent before it is stored: its name, its owner, its preset or scopes, its
// machines.
func checkAgent(tx kv.Tx, a Agent) error {
	if err := ValidAgentName(a.Name); err != nil {
		return err
	}
	if _, ok := getAccount(tx, a.Owner); !ok {
		return fmt.Errorf("%w: no account %q", ErrNotFound, a.Owner)
	}
	switch {
	case a.Preset == PresetCustom:
		if len(a.Scopes) == 0 {
			return errors.New("a custom agent needs at least one scope")
		}
		for _, sc := range a.Scopes {
			if !slices.Contains(PeopleScopes, sc) {
				return fmt.Errorf("an agent's scope is one of %s, not %q", strings.Join(PeopleScopes, ", "), sc)
			}
		}
	case ValidPreset(a.Preset):
		if len(a.Scopes) > 0 {
			return errors.New("only a custom agent lists scopes")
		}
	default:
		return fmt.Errorf("unknown preset %q; presets are %s, or custom", a.Preset, strings.Join(Presets, ", "))
	}
	for _, m := range a.Machines {
		if !MachineName.MatchString(m) {
			return fmt.Errorf("machine %q: names are lower case letters, digits and dashes", m)
		}
	}
	return nil
}

func getAgent(tx kv.Tx, name string) (Agent, bool) {
	var a Agent
	v := tx.Get(bucketAgents, []byte(name))
	return a, v != nil && json.Unmarshal(v, &a) == nil
}

func putAgent(tx kv.Tx, a Agent) error {
	v, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return tx.Put(bucketAgents, []byte(a.Name), v)
}

// CreateAgent stores a new agent.
func (s *Store) CreateAgent(a Agent) (Agent, error) {
	a.Created = s.now().UTC()
	a.Machines = compactNames(a.Machines)
	a.Scopes = compactNames(a.Scopes)
	a.SSHKeys = nil
	err := s.db.Update(func(tx kv.Tx) error {
		if err := checkAgent(tx, a); err != nil {
			return err
		}
		if _, taken := getAgent(tx, a.Name); taken {
			return fmt.Errorf("%w: an agent is called %q already", ErrExists, a.Name)
		}
		return putAgent(tx, a)
	})
	if err != nil {
		return Agent{}, err
	}
	return a, nil
}

func compactNames(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

// Agent returns the agent called name.
func (s *Store) Agent(name string) (Agent, bool) {
	var a Agent
	ok := false
	_ = s.db.View(func(tx kv.Tx) error {
		a, ok = getAgent(tx, name)
		return nil
	})
	return a, ok
}

// Agents lists the agents by name; owner "" lists everyone's.
func (s *Store) Agents(owner string) []Agent {
	var out []Agent
	_ = s.db.View(func(tx kv.Tx) error {
		tx.Range(bucketAgents, kv.Range{}, func(_, v []byte) bool {
			var a Agent
			if json.Unmarshal(v, &a) == nil && (owner == "" || a.Owner == owner) {
				out = append(out, a)
			}
			return true
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// UpdateAgent changes the agent called name with change, which cannot alter its name, owner, keys or
// creation.
func (s *Store) UpdateAgent(name string, change func(*Agent) error) (Agent, error) {
	var out Agent
	err := s.db.Update(func(tx kv.Tx) error {
		a, ok := getAgent(tx, name)
		if !ok {
			return ErrNotFound
		}
		owner, keys, created, by := a.Owner, a.SSHKeys, a.Created, a.CreatedBy
		if err := change(&a); err != nil {
			return err
		}
		a.Name, a.Owner, a.SSHKeys, a.Created, a.CreatedBy = name, owner, keys, created, by
		a.Machines, a.Scopes = compactNames(a.Machines), compactNames(a.Scopes)
		if a.Preset != PresetCustom {
			a.Scopes = nil
		}
		if err := checkAgent(tx, a); err != nil {
			return err
		}
		out = a
		return putAgent(tx, a)
	})
	return out, err
}

// DeleteAgent forgets an agent with every credential it has: its tokens (and their console sign-ins),
// its OAuth grants and their access tokens. What it did stays on record.
func (s *Store) DeleteAgent(name string) error {
	s.mu.Lock()
	var names []string
	for _, t := range s.tokens {
		if t.Agent == name {
			names = append(names, t.Name)
		}
	}
	err := s.db.Update(func(tx kv.Tx) error {
		if tx.Get(bucketAgents, []byte(name)) == nil {
			return ErrNotFound
		}
		for _, n := range names {
			if err := tx.Delete(bucketTokens, []byte(n)); err != nil {
				return err
			}
		}
		if err := deleteGrants(tx, func(g Grant) bool { return g.Agent == name }); err != nil {
			return err
		}
		return tx.Delete(bucketAgents, []byte(name))
	})
	if err == nil {
		for h, t := range s.tokens {
			if t.Agent == name {
				delete(s.tokens, h)
			}
		}
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	for _, n := range names {
		s.DeleteConsoleSessions(n)
	}
	return nil
}

// CreateAgentToken gives the agent a new static token for ttl and returns its secret, which is not
// stored and cannot be shown again. The token is called after the agent (name, name-2, …); what it may
// do is the agent's.
func (s *Store) CreateAgentToken(agent string, ttl time.Duration, by string) (string, Token, error) {
	if _, ok := s.Agent(agent); !ok {
		return "", Token{}, fmt.Errorf("%w: no agent %q", ErrNotFound, agent)
	}
	if ttl < time.Hour || ttl > 2*366*24*time.Hour {
		return "", Token{}, errors.New("a token must expire between 1 hour and 2 years from now")
	}
	secret, err := newSecret()
	if err != nil {
		return "", Token{}, err
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	var t Token
	err = s.db.Update(func(tx kv.Tx) error {
		if _, ok := getAgent(tx, agent); !ok { // deleted meanwhile
			return fmt.Errorf("%w: no agent %q", ErrNotFound, agent)
		}
		name := agent
		for i := 2; tx.Get(bucketTokens, []byte(name)) != nil; i++ {
			name = fmt.Sprintf("%s-%d", agent, i)
		}
		t = Token{Name: name, Hash: hashToken(secret), Agent: agent, Created: now, CreatedBy: by, Expires: now.Add(ttl), Lifetime: ttl}
		b, _ := json.Marshal(t)
		return tx.Put(bucketTokens, []byte(name), b)
	})
	if err != nil {
		return "", Token{}, err
	}
	s.tokens[t.Hash] = t
	return secret, t, nil
}

// AgentTokens lists the agent's tokens by name.
func (s *Store) AgentTokens(agent string) []Token {
	var out []Token
	for _, t := range s.Tokens() {
		if t.Agent == agent {
			out = append(out, t)
		}
	}
	return out
}

// KeyHolder names who holds the SSH key with this fingerprint: "token NAME", "agent NAME", or "".
func (s *Store) KeyHolder(fingerprint string) string {
	for _, t := range s.Tokens() {
		if slices.ContainsFunc(t.SSHKeys, func(k SSHKey) bool { return k.Fingerprint == fingerprint }) {
			return "token " + t.Name
		}
	}
	for _, a := range s.Agents("") {
		if slices.ContainsFunc(a.SSHKeys, func(k SSHKey) bool { return k.Fingerprint == fingerprint }) {
			return "agent " + a.Name
		}
	}
	return ""
}

// dropKeys removes from every agent the keys it added with the credential cred (SSHKey.Via), which is
// going away.
func dropKeys(tx kv.Tx, cred string) error {
	if cred == "" {
		return nil
	}
	var changed []Agent
	tx.Range(bucketAgents, kv.Range{}, func(_, v []byte) bool {
		var a Agent
		if json.Unmarshal(v, &a) == nil && slices.ContainsFunc(a.SSHKeys, func(k SSHKey) bool { return k.Via == cred }) {
			a.SSHKeys = slices.DeleteFunc(a.SSHKeys, func(k SSHKey) bool { return k.Via == cred })
			changed = append(changed, a)
		}
		return true
	})
	for _, a := range changed {
		if err := putAgent(tx, a); err != nil {
			return err
		}
	}
	return nil
}

// keyVia is the credential the agent named by key (agent:NAME) added its key fp with; none for a key a
// person added, or another principal.
func (s *Store) keyVia(key, fp string) string {
	name, ok := strings.CutPrefix(key, "agent:")
	if !ok {
		return ""
	}
	a, ok := s.Agent(name)
	if !ok {
		return ""
	}
	for _, k := range a.SSHKeys {
		if k.Fingerprint == fp {
			return k.Via
		}
	}
	return ""
}

// keyLive reports whether an agent's key still works: the credential it was added with, if any, has
// not been revoked or expired.
func (s *Store) keyLive(k SSHKey, p Principal) bool {
	return k.Via == "" || s.CredentialLive(k.Via, p)
}

// MaxSSHKeys bounds the keys one agent holds.
const MaxSSHKeys = 20

// AddAgentSSHKey gives an agent a key for the SSH channel. A key belongs to one principal and is never a
// machine's connector key; adding a key the agent holds already is no change.
func (s *Store) AddAgentSSHKey(agent string, k SSHKey) (Agent, error) {
	if holder := s.KeyHolder(k.Fingerprint); holder != "" && holder != "agent "+agent {
		return Agent{}, fmt.Errorf("%w: the key %s belongs to %s", ErrExists, k.Fingerprint, holder)
	}
	if m, ok := s.MachineByConnectorKey(k.Fingerprint); ok {
		return Agent{}, fmt.Errorf("%w: the key %s is machine %s's connector key", ErrExists, k.Fingerprint, m.Name)
	}
	var out Agent
	err := s.db.Update(func(tx kv.Tx) error {
		a, ok := getAgent(tx, agent)
		if !ok {
			return ErrNotFound
		}
		if !slices.ContainsFunc(a.SSHKeys, func(x SSHKey) bool { return x.Fingerprint == k.Fingerprint }) {
			if len(a.SSHKeys) >= MaxSSHKeys {
				return fmt.Errorf("an agent holds at most %d keys; remove one first", MaxSSHKeys)
			}
			a.SSHKeys = append(a.SSHKeys, k)
		}
		out = a
		return putAgent(tx, a)
	})
	return out, err
}

// RemoveAgentSSHKey takes a key, by its fingerprint, from an agent.
func (s *Store) RemoveAgentSSHKey(agent, fingerprint string) (Agent, error) {
	var out Agent
	err := s.db.Update(func(tx kv.Tx) error {
		a, ok := getAgent(tx, agent)
		if !ok {
			return ErrNotFound
		}
		n := len(a.SSHKeys)
		a.SSHKeys = slices.DeleteFunc(a.SSHKeys, func(x SSHKey) bool { return x.Fingerprint == fingerprint })
		if len(a.SSHKeys) == n {
			return fmt.Errorf("%w: the agent has no key %s", ErrNotFound, fingerprint)
		}
		out = a
		return putAgent(tx, a)
	})
	return out, err
}

// Legacy reports whether a token is a legacy one: not an agent's, and with only people's scopes, so
// that it can be claimed as an agent. A machine's token is never legacy.
func Legacy(t Token) bool {
	return t.Agent == "" && len(t.Scopes) > 0 && !slices.ContainsFunc(t.Scopes, func(sc string) bool { return !slices.Contains(PeopleScopes, sc) })
}

// ClaimToken makes a legacy token an agent's: agentName (made for owner with preset if it does not
// exist; preset "" takes the token's closest, custom when none matches exactly). The token keeps its
// secret, expiry and lifetime; its machines and SSH keys move to the agent, and what it may do is the
// agent's from then on. Claiming into an existing agent of the owner keeps that agent's preset and
// machines and adds the token's keys.
func (s *Store) ClaimToken(name, owner, agentName, preset, by string) (Agent, Token, error) {
	if agentName == "" {
		agentName = name
	}
	if err := ValidAgentName(agentName); err != nil {
		return Agent{}, Token{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var t Token
	found := false
	for _, x := range s.tokens {
		if x.Name == name {
			t, found = x, true
		}
	}
	if !found {
		return Agent{}, Token{}, fmt.Errorf("%w: no token %q", ErrNotFound, name)
	}
	if !Legacy(t) {
		return Agent{}, Token{}, fmt.Errorf("token %s is not a legacy token: it is an agent's already, or a machine's", name)
	}
	if preset == "" {
		preset = ClosestPreset(t.Scopes)
	}
	var a Agent
	err := s.db.Update(func(tx kv.Tx) error {
		existing, ok := getAgent(tx, agentName)
		switch {
		case ok && existing.Owner != owner:
			return fmt.Errorf("%w: agent %s is %s's", ErrExists, agentName, existing.Owner)
		case ok:
			// into an existing agent only when that gives the token nothing it could not do already
			wider := slices.ContainsFunc(existing.Wanted(), func(sc string) bool { return !slices.Contains(t.Scopes, sc) })
			if len(t.Machines) > 0 && (len(existing.Machines) == 0 || slices.ContainsFunc(existing.Machines, func(m string) bool { return !slices.Contains(t.Machines, m) })) {
				wider = true
			}
			if wider {
				return fmt.Errorf("agent %s may do more than token %s may now; claim the token as an agent of its own", agentName, name)
			}
			a = existing
		default:
			a = Agent{Name: agentName, Owner: owner, Preset: preset, Machines: compactNames(t.Machines), Created: s.now().UTC(), CreatedBy: by}
			if preset == PresetCustom {
				a.Scopes = compactNames(t.Scopes)
			}
			if err := checkAgent(tx, a); err != nil {
				return err
			}
		}
		for _, k := range t.SSHKeys {
			if !slices.ContainsFunc(a.SSHKeys, func(x SSHKey) bool { return x.Fingerprint == k.Fingerprint }) {
				a.SSHKeys = append(a.SSHKeys, k)
			}
		}
		if err := putAgent(tx, a); err != nil {
			return err
		}
		t.Agent, t.Scopes, t.Machines, t.SSHKeys = agentName, nil, nil, nil
		b, _ := json.Marshal(t)
		return tx.Put(bucketTokens, []byte(t.Name), b)
	})
	if err != nil {
		return Agent{}, Token{}, err
	}
	s.tokens[t.Hash] = t
	return a, t, nil
}

// ---- principals

// Principal is who acts and what it may do now: a person (User), an agent (Agent, owned by User), or a
// token of its own (Token alone: a machine's, or a legacy token). Token or Grant is the credential the
// request came with, if any; Expires when that credential expires (for an agent signed in by its SSH
// key, when its last credential does). Scopes and Machines are what it may do now.
type Principal struct {
	User     string    `json:"user,omitempty"`
	Agent    string    `json:"agent,omitempty"`
	Preset   string    `json:"preset,omitempty"`
	Token    string    `json:"token,omitempty"`
	Grant    string    `json:"grant,omitempty"`
	Client   string    `json:"client,omitempty"` // the OAuth client's name, for a grant
	Scopes   []string  `json:"scopes"`
	Machines []string  `json:"machines,omitempty"`
	Subject  string    `json:"subject,omitempty"`
	Expires  time.Time `json:"expires,omitzero"`
}

// Name is how the audit log, the chat and the console name the principal: the person, "person / agent"
// for an agent, or the token's name.
func (p Principal) Name() string {
	switch {
	case p.Agent != "":
		return p.User + " / " + p.Agent
	case p.User != "":
		return p.User
	}
	return p.Token
}

// Key names the principal for a later look-up (Store.Principal): user:NAME, agent:NAME or token:NAME.
func (p Principal) Key() string {
	switch {
	case p.Agent != "":
		return "agent:" + p.Agent
	case p.User != "":
		return "user:" + p.User
	}
	return "token:" + p.Token
}

// Cred names the credential the principal came with, for CredentialLive: token:NAME or grant:ID; none for
// a person.
func (p Principal) Cred() string {
	switch {
	case p.Grant != "":
		return "grant:" + p.Grant
	case p.Token != "":
		return "token:" + p.Token
	}
	return ""
}

// CredentialLive reports whether the credential named by cred (Principal.Cred, or key:FINGERPRINT for an
// SSH key) still works for p: the token exists and has not expired, the grant has not been revoked or
// expired, the key is still p's.
func (s *Store) CredentialLive(cred string, p Principal) bool {
	kind, name, _ := strings.Cut(cred, ":")
	now := s.now()
	switch kind {
	case "token":
		for _, t := range s.Tokens() {
			if t.Name == name {
				return now.Before(t.Expires) && t.Agent == p.Agent
			}
		}
	case "grant":
		g, ok := s.Grant(name)
		return ok && now.Before(g.Expires) && g.Agent == p.Agent
	case "key":
		if p.Agent == "" {
			return s.KeyHolder(name) == "token "+p.Token
		}
		a, ok := s.Agent(p.Agent)
		if !ok {
			return false
		}
		for _, k := range a.SSHKeys {
			if k.Fingerprint == name {
				return k.Via != cred && s.keyLive(k, p)
			}
		}
	}
	return false
}

// Has reports whether the principal may use scope now.
func (p Principal) Has(scope string) bool { return slices.Contains(p.Scopes, scope) }

// Reaches reports whether the principal may reach machine through the SSH channel.
func (p Principal) Reaches(machine string) bool {
	return p.Has("machines:ssh") && (len(p.Machines) == 0 || slices.Contains(p.Machines, machine))
}

// Person reports whether the principal is a person signed in to the console.
func (p Principal) Person() bool { return p.User != "" && p.Agent == "" }

func intersect(a, b []string) []string {
	out := []string{}
	for _, x := range a {
		if slices.Contains(b, x) && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// agentPrincipal resolves an agent: its owner must exist and be enabled; it may do what its preset and
// its owner's role both allow.
func agentPrincipal(tx kv.Tx, name string) (Principal, bool) {
	a, ok := getAgent(tx, name)
	if !ok {
		return Principal{}, false
	}
	owner, ok := getAccount(tx, a.Owner)
	if !ok || owner.Disabled {
		return Principal{}, false
	}
	return Principal{User: a.Owner, Agent: a.Name, Preset: a.Preset, Scopes: intersect(a.Wanted(), RoleScopes(owner.Role)), Machines: slices.Clone(a.Machines)}, true
}

// grantPrincipal resolves an agent reached through one of its OAuth grants: the agent, within the
// grant's own preset and machines.
func grantPrincipal(tx kv.Tx, g Grant) (Principal, bool) {
	p, ok := agentPrincipal(tx, g.Agent)
	if !ok {
		return Principal{}, false
	}
	if g.Preset != "" {
		p.Scopes = intersect(p.Scopes, PresetScopes(g.Preset))
	}
	machines, none := capMachines(p.Machines, g.Machines)
	p.Machines = machines
	if none { // the agent and the grant have no machine in common: it reaches none
		p.Scopes = slices.DeleteFunc(p.Scopes, func(s string) bool { return s == "machines:ssh" })
	}
	p.Grant, p.Client = g.ID, g.ClientName
	return p, true
}

// capMachines is the machines both lists allow, where an empty list allows all; none reports that two
// lists that each name machines have none in common.
func capMachines(a, b []string) (machines []string, none bool) {
	switch {
	case len(a) == 0:
		return slices.Clone(b), false
	case len(b) == 0:
		return slices.Clone(a), false
	}
	for _, m := range a {
		if slices.Contains(b, m) {
			machines = append(machines, m)
		}
	}
	return machines, len(machines) == 0
}

// PrincipalWith resolves a principal by its key (Principal.Key) and the credential it came with
// (Principal.Cred): an agent reached through an OAuth grant may do what that grant allows, which can be
// less than the agent may.
func (s *Store) PrincipalWith(key, cred string) (Principal, bool) {
	if fp, isKey := strings.CutPrefix(cred, "key:"); isKey { // a key an agent added through a grant has that grant's ceiling
		if via := s.keyVia(key, fp); strings.HasPrefix(via, "grant:") {
			return s.PrincipalWith(key, via)
		}
		return s.Principal(key)
	}
	id, isGrant := strings.CutPrefix(cred, "grant:")
	if !isGrant {
		return s.Principal(key)
	}
	var p Principal
	ok := false
	now := s.now()
	_ = s.db.View(func(tx kv.Tx) error {
		gs := grants(tx, func(g Grant) bool { return g.ID == id })
		if len(gs) == 0 || !now.Before(gs[0].Expires) || "agent:"+gs[0].Agent != key {
			return nil
		}
		if p, ok = grantPrincipal(tx, gs[0]); ok {
			p.Expires = gs[0].Expires
		}
		return nil
	})
	return p, ok
}

// AccountPrincipal resolves a person: an enabled account, which may do what its role allows.
func (s *Store) AccountPrincipal(username string) (Principal, bool) {
	a, ok := s.Account(username)
	if !ok || a.Disabled {
		return Principal{}, false
	}
	return Principal{User: a.Username, Scopes: RoleScopes(a.Role)}, true
}

// TokenPrincipal resolves a token that has been verified: an agent's, or one of its own.
func (s *Store) TokenPrincipal(t Token) (Principal, bool) {
	if t.Agent == "" {
		return Principal{Token: t.Name, Scopes: slices.Clone(t.Scopes), Machines: slices.Clone(t.Machines), Subject: t.Subject, Expires: t.Expires}, true
	}
	var p Principal
	ok := false
	_ = s.db.View(func(tx kv.Tx) error {
		p, ok = agentPrincipal(tx, t.Agent)
		return nil
	})
	p.Token, p.Expires = t.Name, t.Expires
	return p, ok
}

// AgentPrincipal resolves an agent by name, for its SSH keys and for later checks: it must have a
// credential that has not expired, and Expires is when the last one does.
func (s *Store) AgentPrincipal(name string) (Principal, bool) {
	var p Principal
	ok := false
	now := s.now()
	_ = s.db.View(func(tx kv.Tx) error {
		if p, ok = agentPrincipal(tx, name); !ok {
			return nil
		}
		for _, g := range grants(tx, func(g Grant) bool { return g.Agent == name }) {
			if now.Before(g.Expires) && g.Expires.After(p.Expires) {
				p.Expires = g.Expires
			}
		}
		return nil
	})
	if !ok {
		return Principal{}, false
	}
	for _, t := range s.AgentTokens(name) {
		if now.Before(t.Expires) && t.Expires.After(p.Expires) {
			p.Expires = t.Expires
		}
	}
	if p.Expires.IsZero() {
		return Principal{}, false
	}
	return p, true
}

// Principal resolves a key made by Principal.Key, afresh.
func (s *Store) Principal(key string) (Principal, bool) {
	kind, name, ok := strings.Cut(key, ":")
	if !ok || name == "" {
		return Principal{}, false
	}
	switch kind {
	case "user":
		return s.AccountPrincipal(name)
	case "agent":
		return s.AgentPrincipal(name)
	case "token":
		for _, t := range s.Tokens() {
			if t.Name == name && t.Agent == "" && s.now().Before(t.Expires) {
				return s.TokenPrincipal(t)
			}
		}
	}
	return Principal{}, false
}

// Authenticate resolves a bearer secret: a static token (lbt_…) or an OAuth access token (lba_…).
func (s *Store) Authenticate(secret string) (Principal, bool) {
	switch {
	case strings.HasPrefix(secret, TokenPrefix):
		t, ok := s.VerifyToken(secret)
		if !ok {
			return Principal{}, false
		}
		return s.TokenPrincipal(t)
	case strings.HasPrefix(secret, AccessPrefix):
		return s.verifyAccess(secret)
	}
	return Principal{}, false
}

// PrincipalBySSHKey resolves the holder of an SSH key: a token of its own that has not expired, or an
// agent with a live credential.
func (s *Store) PrincipalBySSHKey(fingerprint string) (Principal, bool) {
	if t, ok := s.TokenBySSHKey(fingerprint); ok && t.Agent == "" {
		return s.TokenPrincipal(t)
	}
	for _, a := range s.Agents("") {
		for _, k := range a.SSHKeys {
			if k.Fingerprint != fingerprint {
				continue
			}
			p, ok := s.AgentPrincipal(a.Name)
			if !ok || !s.keyLive(k, p) {
				return Principal{}, false
			}
			if strings.HasPrefix(k.Via, "grant:") { // no more than the grant it was added through
				return s.PrincipalWith(p.Key(), k.Via)
			}
			return p, true
		}
	}
	return Principal{}, false
}

// AddTokenSSHKey gives a token of its own (not an agent's: AddAgentSSHKey) a key for the SSH channel,
// under the same rule: a key belongs to one principal and is never a machine's connector key.
func (s *Store) AddTokenSSHKey(name string, k SSHKey) (Token, error) {
	if holder := s.KeyHolder(k.Fingerprint); holder != "" && holder != "token "+name {
		return Token{}, fmt.Errorf("%w: the key %s belongs to %s", ErrExists, k.Fingerprint, holder)
	}
	if m, ok := s.MachineByConnectorKey(k.Fingerprint); ok {
		return Token{}, fmt.Errorf("%w: the key %s is machine %s's connector key", ErrExists, k.Fingerprint, m.Name)
	}
	return s.UpdateToken(name, func(t *Token) error {
		if t.Agent != "" {
			return fmt.Errorf("token %s is agent %s's: its keys are the agent's", name, t.Agent)
		}
		if !slices.ContainsFunc(t.SSHKeys, func(x SSHKey) bool { return x.Fingerprint == k.Fingerprint }) {
			if len(t.SSHKeys) >= MaxSSHKeys {
				return fmt.Errorf("a token holds at most %d keys; remove one first", MaxSSHKeys)
			}
			t.SSHKeys = append(t.SSHKeys, k)
		}
		return nil
	})
}

// RemoveTokenSSHKey takes a key, by its fingerprint, from a token of its own.
func (s *Store) RemoveTokenSSHKey(name, fingerprint string) (Token, error) {
	return s.UpdateToken(name, func(t *Token) error {
		n := len(t.SSHKeys)
		t.SSHKeys = slices.DeleteFunc(t.SSHKeys, func(x SSHKey) bool { return x.Fingerprint == fingerprint })
		if len(t.SSHKeys) == n {
			return fmt.Errorf("%w: the token has no key %s", ErrNotFound, fingerprint)
		}
		return nil
	})
}
