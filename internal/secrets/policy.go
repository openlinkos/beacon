package secrets

import (
	"encoding/json"
	"time"

	"github.com/openlinkos/beacon/internal/settings"
)

// The two axes meet here. Whether an agent may do an action to an item is decided by the design's
// five steps, in order:
//
//  1. any deny entitlement matching: refuse;
//  2. no allowing entitlement covering it, or the action not among the entitlement's: refuse, and
//     the agent is told the item does not exist;
//  3. the item's effective level is never: refuse;
//  4. the approval a request must pass is the stricter of the item's level and the entitlement's
//     own floor;
//  5. the uses and duration come from the request, clamped by what the settings allow at every
//     level (system, collection, tags, entitlement).
//
// The final actions are the entitlement's intersected with what the item, its tags and its
// collection allow: any of them may narrow an item to use only.
//
// Several entitlements may cover one item: the one with the widest approval floor wins; at equal
// floors, the one with the larger ceilings (the most uses, the longest duration its settings
// give). A request may name the entitlement it means to use.

// policyDecision is the five steps' answer.
type policyDecision struct {
	// OK: the agent may ask. False with Reason for why not; NotFound says the agent is told the
	// item is not there (the design's default deny: what is not in scope does not exist).
	OK       bool
	NotFound bool
	Reason   string
	// Level is the approval a request must pass (step 4).
	Level string
	// Entitlement is the entitlement the request goes through (the selection rule).
	Entitlement Entitlement
	// Actions are the actions the agent may take on the item.
	Actions []string
	// Fields are the fields the entitlement narrows to; empty: every field.
	Fields []string
}

// decidePolicy walks the five steps for agent, item and action. The item's effective level and
// the collection come in already resolved; namedEntitlement, when not empty, is the entitlement
// the request pointed at (it must cover, or the answer is a refusal).
func (s *Service) decidePolicy(agent agentInfo, item Item, col Collection, action, namedEntitlement string) policyDecision {
	now := s.now()
	var allowing []Entitlement
	for _, e := range s.Entitlements() {
		if !e.coversPrincipal(agent, now) {
			continue
		}
		if e.Deny && e.coversResource(item, col) {
			// step 1: a deny matching wins over everything
			return policyDecision{OK: false, NotFound: true, Reason: "a deny rule keeps this out of the agent's reach"}
		}
		if !e.Deny && e.coversResource(item, col) {
			allowing = append(allowing, e)
		}
	}
	// step 2: no allowing entitlement, or the action outside it
	if len(allowing) == 0 {
		return policyDecision{OK: false, NotFound: true, Reason: "no entitlement covers this"}
	}
	if namedEntitlement != "" {
		var named *Entitlement
		for i := range allowing {
			if allowing[i].Name == namedEntitlement {
				named = &allowing[i]
				break
			}
		}
		if named == nil {
			return policyDecision{OK: false, NotFound: true, Reason: "the named entitlement does not cover this"}
		}
		allowing = []Entitlement{*named}
	}
	var covering []Entitlement
	for _, e := range allowing {
		if e.allowsAction(action) {
			covering = append(covering, e)
		}
	}
	if len(covering) == 0 {
		return policyDecision{OK: false, NotFound: true, Reason: "the action is not among what the entitlements allow"}
	}
	// the actions the item, its tags and its collection leave, met with what the entitlements allow
	actions := intersectActions(s.itemActions(item, col), covering)
	if !containsString(actions, action) {
		return policyDecision{OK: false, NotFound: true, Reason: "the action is not among what the item allows"}
	}
	// step 3: never is not given to any agent, whatever covers it — and looks like it is not there
	level := s.EffectiveLevel(item)
	if level == LevelNever {
		return policyDecision{OK: false, NotFound: true, Reason: "there is no such item"}
	}
	// step 4: the approval is the stricter of the item's level and the entitlement's floor
	chosen := selectEntitlement(s, covering, level)
	approval := level
	if LevelStrictness[chosen.Approval] > LevelStrictness[approval] {
		approval = chosen.Approval
	}
	return policyDecision{
		OK: true, Level: approval, Entitlement: chosen,
		Actions: actions,
		Fields:  chosen.Resources.Fields,
	}
}

// selectEntitlement picks among the entitlements covering an item: the widest approval floor
// first (auto before review before person), and at equal floors the larger ceilings — the most
// uses and the longest duration the settings allow that entitlement to give.
func selectEntitlement(s *Service, covering []Entitlement, level string) Entitlement {
	best := covering[0]
	for _, e := range covering[1:] {
		if entitlementWider(s, e, best, level) {
			best = e
		}
	}
	return best
}

// entitlementWider says whether e gives an easier way through than best: a wider approval floor,
// or at an equal floor more uses, or the same uses for longer. Unlimited uses and endless
// durations count widest.
func entitlementWider(s *Service, e, best Entitlement, level string) bool {
	floor, bestFloor := LevelStrictness[approvalFloor(e)], LevelStrictness[approvalFloor(best)]
	if floor != bestFloor {
		return floor < bestFloor
	}
	uses, dur := s.entitlementCeilings(e, level)
	bestUses, bestDur := s.entitlementCeilings(best, level)
	if c := compareCeilings(uses, bestUses); c != 0 {
		return c > 0
	}
	return compareCeilings(dur, bestDur) > 0
}

// compareCeilings compares two ceilings of one kind: 0 is unlimited and widest; otherwise the
// larger number is wider.
func compareCeilings(a, b int64) int {
	switch {
	case a == b:
		return 0
	case a == 0:
		return 1
	case b == 0:
		return -1
	case a > b:
		return 1
	default:
		return -1
	}
}

// approvalFloor is the entitlement's own floor, as a level (auto when it has none, so a floorless
// entitlement counts widest).
func approvalFloor(e Entitlement) string {
	if e.Approval == "" {
		return LevelAuto
	}
	return e.Approval
}

// entitlementCeilings are the uses and duration the settings allow this entitlement to give at
// this level (unlimited uses are 0; unlimited duration is 0). A nil settings service leaves the
// system's defaults.
func (s *Service) entitlementCeilings(e Entitlement, level string) (uses int64, dur int64) {
	sel := settings.Selector{Entitlement: e.Name}
	return s.settingCountAt("request.max_uses", sel, 0), s.settingDurationAt("request.max_duration", sel, 0)
}

// itemActions is what the item, its tags and its collection leave of the actions; each may narrow
// an item to use only. Empty item, tag or collection actions leave everything.
func (s *Service) itemActions(item Item, col Collection) []string {
	allowed := []string{ActDescribe, ActUse, ActRead}
	narrow := func(to []string) {
		if len(to) == 0 {
			return
		}
		var out []string
		for _, a := range allowed {
			if containsString(to, a) {
				out = append(out, a)
			}
		}
		allowed = out
	}
	narrow(item.Actions)
	narrow(col.Actions)
	for _, t := range item.Tags {
		if tag, ok := s.tagByName(t); ok {
			narrow(tag.Actions)
		}
	}
	return allowed
}

// intersectActions leaves what every covering entitlement allows of the actions the item leaves.
func intersectActions(allowed []string, covering []Entitlement) []string {
	out := append([]string(nil), allowed...)
	for _, e := range covering {
		var next []string
		for _, a := range out {
			if e.allowsAction(a) {
				next = append(next, a)
			}
		}
		out = next
	}
	return out
}

// ownerMayRead is the owner cap: an agent asks for no more than its owner holds over the
// collection. An owner role holds manage everywhere; anyone else needs read or more.
func (s *Service) ownerMayRead(owner, collectionID string) bool {
	acc, ok := s.d.Store.Account(owner)
	if !ok || acc.Disabled {
		return false
	}
	return s.May(Actor{Name: owner, Role: acc.Role}, collectionID, PermRead)
}

// grantFields is the fields a grant may carry: the intersection of what the ask named and what
// the entitlement allows. An empty ask takes the entitlement's fields; an empty entitlement
// allows every field the ask named (or every field, when the ask named none). outOfRange is true
// when the ask named a field the entitlement does not allow — the whole item is then refused.
func grantFields(asked, allowed []string) (fields []string, outOfRange bool) {
	if len(allowed) == 0 {
		return append([]string(nil), asked...), false
	}
	if len(asked) == 0 {
		return append([]string(nil), allowed...), false
	}
	var out []string
	for _, f := range asked {
		if containsString(allowed, f) {
			out = append(out, f)
		} else {
			outOfRange = true
		}
	}
	return out, outOfRange
}

// settingCountAt reads a count setting with a selector (0: unlimited); def when there are no
// settings or the row does not answer.
func (s *Service) settingCountAt(key string, sel settings.Selector, def int64) int64 {
	if s.d.Settings == nil {
		return def
	}
	r, _, err := s.d.Settings.Value(key, sel)
	if err != nil {
		return def
	}
	var str string
	if err := json.Unmarshal(r.Value, &str); err == nil && str == "unlimited" {
		return 0
	}
	var n int64
	if err := json.Unmarshal(r.Value, &n); err != nil || n < 0 {
		return def
	}
	return n
}

// settingDurationAt reads a duration setting with a selector, in seconds (0: unlimited); def when
// there are no settings or the row does not answer.
func (s *Service) settingDurationAt(key string, sel settings.Selector, def int64) int64 {
	if s.d.Settings == nil {
		return def
	}
	r, _, err := s.d.Settings.Value(key, sel)
	if err != nil {
		return def
	}
	var str string
	if err := json.Unmarshal(r.Value, &str); err != nil {
		return def
	}
	if str == "unlimited" || str == "never" {
		return 0
	}
	if d, err := settings.ParseDuration(str); err == nil {
		return int64(d / time.Second)
	}
	return def
}

// settingLevelsDurationAt reads a by-level setting (request.default_duration and the like): the
// value the level names, in seconds (0: the level's answer is never or unlimited).
func (s *Service) settingLevelsDurationAt(key, level string, sel settings.Selector) (int64, bool) {
	if s.d.Settings == nil {
		return 0, false
	}
	r, _, err := s.d.Settings.Value(key, sel)
	if err != nil {
		return 0, false
	}
	var m map[string]string
	if err := json.Unmarshal(r.Value, &m); err != nil {
		return 0, false
	}
	v, ok := m[level]
	if !ok {
		return 0, false
	}
	if v == "never" || v == "unlimited" {
		return 0, true
	}
	if d, err := settings.ParseDuration(v); err == nil {
		return int64(d / time.Second), true
	}
	return 0, false
}
