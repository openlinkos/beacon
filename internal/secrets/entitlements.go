package secrets

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/store"
)

// The second axis of the design's authorisation model: the entitlement, after GCP PAM's — who is
// eligible (the principals), what they may ask for (the resources), which actions they may take,
// how strict the approval must at least be, and who reviews. Everything an agent can reach it
// reaches through one; nothing outside is not even visible (default deny). A deny entitlement
// matches first and refuses, whatever the others say. Every row is signed like the rest of the
// policy (the design's third invariant: a row that fails its signature is treated as absent and
// reported), and agent grouping tags are policy the same way, for they change what an agent may
// reach.

// Agent actions, weakest first: describe sees the overview, use works with a value without seeing
// it (the later package), read takes the value itself.
const (
	ActDescribe = "describe"
	ActUse      = "use"
	ActRead     = "read"
)

// Actions lists the agent actions, weakest first.
var Actions = []string{ActDescribe, ActUse, ActRead}

// actRank numbers the actions for comparing them.
var actRank = map[string]int{ActDescribe: 0, ActUse: 1, ActRead: 2}

// ValidAction reports whether s is an agent action.
func ValidAction(s string) bool { _, ok := actRank[s]; return ok }

// Principals is who an entitlement makes eligible: agents by name, agents wearing any of the
// agent tags, and every agent of the people named.
type Principals struct {
	Agents    []string `json:"agents,omitempty"`
	AgentTags []string `json:"agent_tags,omitempty"`
	AgentsOf  []string `json:"agents_of,omitempty"`
}

// Empty reports whether no principal is named.
func (p Principals) Empty() bool {
	return len(p.Agents) == 0 && len(p.AgentTags) == 0 && len(p.AgentsOf) == 0
}

// Resources is what an entitlement reaches: whole collections, items by their tags (globs, any
// match), single items by reference, and — narrowing further — only the fields named.
type Resources struct {
	Collections []string `json:"collections,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Items       []string `json:"items,omitempty"`
	Fields      []string `json:"fields,omitempty"`
}

// Empty reports whether no resource is named (an entitlement with none reaches nothing).
func (r Resources) Empty() bool {
	return len(r.Collections) == 0 && len(r.Tags) == 0 && len(r.Items) == 0 && len(r.Fields) == 0
}

// Entitlement is one row of the second axis. Approval is the floor a request must at least pass
// (it can only make the item's own level stricter, never looser); Expires ends the entitlement
// itself; Reviewers names the review chain this entitlement's requests walk (kept for the review
// package); Template marks the entitlements an owner may hand a new agent at onboarding.
type Entitlement struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Deny        bool       `json:"deny,omitempty"`
	Principals  Principals `json:"principals"`
	Resources   Resources  `json:"resources"`
	Actions     []string   `json:"actions"`
	Approval    string     `json:"approval,omitempty"` // one of Levels; empty: none
	Reviewers   []string   `json:"reviewers,omitempty"`
	Template    bool       `json:"template,omitempty"`
	Expires     time.Time  `json:"expires,omitzero"`
	Actor       string     `json:"actor,omitempty"`
	Via         string     `json:"via,omitempty"`
	At          time.Time  `json:"at"`
	Reason      string     `json:"reason,omitempty"`
	Sig         []byte     `json:"sig"`
}

func (e Entitlement) signedParts() []string {
	return []string{e.Name, e.Description, b01(e.Deny), strings.Join(e.Principals.Agents, "\x01"),
		strings.Join(e.Principals.AgentTags, "\x01"), strings.Join(e.Principals.AgentsOf, "\x01"),
		strings.Join(e.Resources.Collections, "\x01"), strings.Join(e.Resources.Tags, "\x01"),
		strings.Join(e.Resources.Items, "\x01"), strings.Join(e.Resources.Fields, "\x01"),
		strings.Join(e.Actions, "\x01"), e.Approval, strings.Join(e.Reviewers, "\x01"), b01(e.Template),
		i64(e.Expires.UnixNano()), e.Actor, e.Via, i64(e.At.UnixNano()), e.Reason}
}

func b01(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// AgentTags is one agent's grouping tags (role/on-call, platform/…), lower-case and layered like
// the items' own. They change what the agent may reach, so they are policy and signed.
type AgentTags struct {
	Agent string    `json:"agent"`
	Tags  []string  `json:"tags"`
	Actor string    `json:"actor,omitempty"`
	Via   string    `json:"via,omitempty"`
	At    time.Time `json:"at"`
	Sig   []byte    `json:"sig"`
}

func (t AgentTags) signedParts() []string {
	return []string{t.Agent, strings.Join(t.Tags, "\x01"), t.Actor, t.Via, i64(t.At.UnixNano())}
}

// ---- reading

// Entitlements lists the entitlements by name, verified rows only.
func (s *Service) Entitlements() []Entitlement {
	var out []Entitlement
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketEntitlements, kv.Range{}, func(_, v []byte) bool {
			var e Entitlement
			if json.Unmarshal(v, &e) == nil && s.entitlementVerifies(e) {
				out = append(out, e)
			}
			return true
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Entitlement finds one by name.
func (s *Service) Entitlement(name string) (Entitlement, bool) {
	var out Entitlement
	ok := false
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		v := tx.Get(bucketEntitlements, []byte(name))
		if v != nil && json.Unmarshal(v, &out) == nil && s.entitlementVerifies(out) {
			ok = true
		}
		return nil
	})
	return out, ok
}

func (s *Service) entitlementVerifies(e Entitlement) bool {
	sk, err := s.sk()
	if err != nil {
		return false
	}
	return verifyRow(sk, e.Sig, e.signedParts()...)
}

// TagsOfAgent lists one agent's grouping tags, verified; nil when the agent has none.
func (s *Service) TagsOfAgent(agent string) []string {
	var out []string
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		v := tx.Get(bucketAgentTags, []byte(agent))
		var t AgentTags
		if v != nil && json.Unmarshal(v, &t) == nil && s.agentTagsVerify(t) {
			out = t.Tags
		}
		return nil
	})
	return out
}

func (s *Service) agentTagsVerify(t AgentTags) bool {
	sk, err := s.sk()
	if err != nil {
		return false
	}
	return verifyRow(sk, t.Sig, t.signedParts()...)
}

// ---- writing

// entitlementNameRe is what an entitlement may be called.
var entitlementNameRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,39}$`)

// SetEntitlement adds or changes an entitlement. Only a person holding the deployment's admin
// scope reaches this through the surfaces; the row is signed, and every change re-assesses the
// grants the change tightens around (the design's grant.on_policy_tightened).
func (s *Service) SetEntitlement(who Actor, e Entitlement) (Entitlement, error) {
	if s.keys.sealed() {
		return Entitlement{}, ErrSealed
	}
	if err := checkEntitlement(e); err != nil {
		return Entitlement{}, err
	}
	var putErr error
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		sk, err := s.sk()
		if err != nil {
			putErr = err
			return
		}
		e.Actor, e.Via, e.At = who.Name, who.Via, s.now()
		e.Sig = signRow(sk, e.signedParts()...)
		putErr = s.d.Store.Storage().Update(func(tx kv.Tx) error {
			b, err := json.Marshal(e)
			if err != nil {
				return err
			}
			return tx.Put(bucketEntitlements, []byte(e.Name), b)
		})
	}()
	if putErr != nil {
		return Entitlement{}, putErr
	}
	s.echo(who, "set the entitlement", e.Name, "secrets_entitlement_set")
	s.reassessGrants() // outside the lock: it takes the lock itself per grant
	return e, nil
}

// DeleteEntitlement removes one; the grants that reached through it are re-assessed.
func (s *Service) DeleteEntitlement(who Actor, name string) error {
	if s.keys.sealed() {
		return ErrSealed
	}
	var err error
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
			if tx.Get(bucketEntitlements, []byte(name)) == nil {
				return fmt.Errorf("%w: no entitlement %q", ErrNotFound, name)
			}
			return tx.Delete(bucketEntitlements, []byte(name))
		})
	}()
	if err != nil {
		return err
	}
	s.echo(who, "removed the entitlement", name, "secrets_entitlement_delete")
	s.reassessGrants() // outside the lock: it takes the lock itself per grant
	return nil
}

// checkEntitlement is the shape check done before any lock is taken.
func checkEntitlement(e Entitlement) error {
	if !entitlementNameRe.MatchString(e.Name) {
		return fmt.Errorf("%w: an entitlement's name is 2 to 40 lower-case letters, digits, dots, dashes and underscores", ErrBadInput)
	}
	for _, a := range e.Principals.Agents {
		if err := store.ValidAgentName(a); err != nil {
			return fmt.Errorf("%w: %w", ErrBadInput, err)
		}
	}
	for _, t := range e.Principals.AgentTags {
		if err := checkTagPattern(t); err != nil {
			return err
		}
	}
	for _, t := range e.Resources.Tags {
		if err := checkTagPattern(t); err != nil {
			return err
		}
	}
	for _, a := range e.Actions {
		if !ValidAction(a) {
			return fmt.Errorf("%w: an action is one of %s", ErrBadInput, joinStrings(Actions))
		}
	}
	if e.Approval != "" && !ValidLevel(e.Approval) {
		return fmt.Errorf("%w: the approval floor is one of %s", ErrBadInput, joinStrings(Levels))
	}
	if !e.Deny {
		if e.Resources.Empty() {
			return fmt.Errorf("%w: an entitlement names what it reaches (collections, tags, items or fields)", ErrBadInput)
		}
		if len(e.Actions) == 0 {
			return fmt.Errorf("%w: an entitlement names its actions", ErrBadInput)
		}
		if e.Principals.Empty() {
			return fmt.Errorf("%w: an entitlement names who it makes eligible (agents, agent tags or agents of someone)", ErrBadInput)
		}
	}
	return nil
}

// checkTagPattern checks one tag pattern: a tag's shape, or the same with a * ending a layer.
func checkTagPattern(t string) error {
	if tagRe.MatchString(t) || globTagRe.MatchString(t) {
		return nil
	}
	return fmt.Errorf("%w: the tag pattern %q is not lower-case and layered with / (a * may stand for a layer's rest)", ErrBadInput, t)
}

// globTagRe is a tag pattern ending in *: env/*, service/dns/*.
var globTagRe = regexp.MustCompile(`^[a-z0-9][a-z0-9 _\-]*(/[a-z0-9][a-z0-9 _\-]*)*/\*$`)

// SetAgentTags sets one agent's grouping tags (none removes the row). The tags change what the
// agent may reach, so the change re-assesses its grants.
func (s *Service) SetAgentTags(who Actor, agent string, tags []string) error {
	if s.keys.sealed() {
		return ErrSealed
	}
	if err := store.ValidAgentName(agent); err != nil {
		return fmt.Errorf("%w: %w", ErrBadInput, err)
	}
	tags = compactLower(tags)
	for _, t := range tags {
		if !tagRe.MatchString(t) {
			return fmt.Errorf("%w: the tag %q is not lower-case, layered with /", ErrBadInput, t)
		}
	}
	var err error
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		sk, e := s.sk()
		if e != nil {
			err = e
			return
		}
		err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
			if len(tags) == 0 {
				return tx.Delete(bucketAgentTags, []byte(agent))
			}
			row := AgentTags{Agent: agent, Tags: tags, Actor: who.Name, Via: who.Via, At: s.now()}
			row.Sig = signRow(sk, row.signedParts()...)
			b, mErr := json.Marshal(row)
			if mErr != nil {
				return mErr
			}
			return tx.Put(bucketAgentTags, []byte(agent), b)
		})
	}()
	if err != nil {
		return err
	}
	s.echo(who, "set the agent's tags", agent+" ("+strings.Join(tags, ", ")+")", "secrets_agent_tags_set")
	s.reassessGrants() // outside the lock: it takes the lock itself per grant
	return nil
}

// ---- matching

// agentInfo is an agent as the policy sees it: its name, its owner and its grouping tags.
type agentInfo struct {
	Name  string
	Owner string
	Tags  []string
}

// agentInfoOf gathers an agent's name, owner and tags; ok false when there is no such agent.
func (s *Service) agentInfoOf(name string) (agentInfo, bool) {
	a, ok := s.d.Store.Agent(name)
	if !ok {
		return agentInfo{}, false
	}
	return agentInfo{Name: a.Name, Owner: a.Owner, Tags: s.TagsOfAgent(a.Name)}, true
}

// coversPrincipal reports whether the entitlement's principals take in the agent.
func (e Entitlement) coversPrincipal(a agentInfo, now time.Time) bool {
	if !e.Expires.IsZero() && now.After(e.Expires) {
		return false // the entitlement itself has passed its time
	}
	for _, n := range e.Principals.Agents {
		if n == a.Name {
			return true
		}
	}
	for _, owner := range e.Principals.AgentsOf {
		if owner == a.Owner {
			return true
		}
	}
	for _, want := range e.Principals.AgentTags {
		for _, have := range a.Tags {
			if tagPatternMatches(want, have) {
				return true
			}
		}
	}
	return false
}

// coversResource reports whether the entitlement reaches the item in its collection.
func (e Entitlement) coversResource(item Item, col Collection) bool {
	for _, c := range e.Resources.Collections {
		if c == col.Name || c == col.ID {
			return true
		}
	}
	for _, pat := range e.Resources.Tags {
		for _, t := range item.Tags {
			if tagPatternMatches(pat, t) {
				return true
			}
		}
	}
	for _, ref := range e.Resources.Items {
		if ref == item.ID || strings.EqualFold(ref, item.Title) ||
			strings.EqualFold(ref, col.Name+"/"+item.Title) || strings.EqualFold(ref, col.ID+"/"+item.ID) {
			return true
		}
	}
	// fields only, no collection, tag or item named: every collection's items, narrowed to the fields
	if len(e.Resources.Collections) == 0 && len(e.Resources.Tags) == 0 && len(e.Resources.Items) == 0 && len(e.Resources.Fields) > 0 {
		return true
	}
	return false
}

// allowsAction reports whether the action is among the entitlement's.
func (e Entitlement) allowsAction(action string) bool { return containsString(e.Actions, action) }

// tagPatternMatches matches a tag against a pattern: the name itself, a layer of the tree (env
// takes env/prod), or a prefix ending in * (service/dns/* takes service/dns and everything under).
func tagPatternMatches(pattern, tag string) bool {
	if pattern == tag {
		return true
	}
	if prefix, ok := strings.CutSuffix(pattern, "/*"); ok {
		return tag == prefix || strings.HasPrefix(tag, prefix+"/")
	}
	return false
}
