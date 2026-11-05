package secrets

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/settings"
	"github.com/openlinkos/beacon/internal/store"
)

// The review chain, after Teleport's access monitoring: rules first, then a review agent, then
// people — the order and the make-up a setting (review.chain) says, overridable per collection,
// tag and entitlement like every setting. A request that waits walks its layers one at a time:
//
//   - rules: Beacon's own deterministic conditions (review.rules), no model called. A matching
//     deny wins over everything; a matching approve settles the ask at once where a rule may
//     approve (the review level — person is people's by definition); otherwise the layer passes.
//   - agents: the review agent, an agent holding secrets:review, woken by the secret.request hook
//     the on-call hooks' own way. It may approve the levels review.agent.may_approve names, within
//     the uses and duration ceilings of its own, and must say why; holding the review scope is not
//     holding the least of the read scopes, so a reviewer that is not a requester sees no value.
//     Not deciding in review.agent.timeout meets the consequence the setting names.
//   - people: the Feishu card and the console. person-level asks are decided here and only here.
//
// The threshold (review.approvals_required, one or one for each level) says how many layers must
// approve; any denial ends the ask whoever it came from. The separation invariant always holds —
// the requester never reviews its own ask — and review.separation can stand the requester's owner
// aside too. What a review agent decides is pushed to the chat (review.notify_decisions), and a
// person may revoke a grant it brought about or take the scope away (the agent's own page).

// The layers of the chain.
const (
	LayerRules  = "rules"
	LayerAgents = "agents"
	LayerPeople = "people"
)

// Layers lists the layers there are, in the default order.
var Layers = []string{LayerRules, LayerAgents, LayerPeople}

// ReviewRule is one of the rules layer's conditions: everything it names must hold for it to
// match, and its decision is approve or deny — a matching deny beats any number of approves. The
// levels it applies to are its own (the levels that wait: review and person; empty: both); the
// rules themselves sit in the review.rules setting, so a collection, tag or entitlement may carry
// its own list the way every setting may.
type ReviewRule struct {
	Name          string      `json:"name"`
	Decide        string      `json:"decide"` // approve or deny
	Disabled      bool        `json:"disabled,omitempty"`
	Levels        []string    `json:"levels,omitempty"`        // review, person; empty: both
	AgentTags     []string    `json:"agent_tags,omitempty"`    // the requester wears any of these
	AlertService  bool        `json:"alert_service,omitempty"` // the ask hangs on a firing alert whose service label matches the items' service/<name> tags
	ReasonPattern string      `json:"reason_pattern,omitempty"`
	Window        *RuleWindow `json:"window,omitempty"`
	Fields        []string    `json:"fields,omitempty"` // the ask's fields stay within these
}

// RuleWindow is a rule's time window: from and to as HH:MM (to may sit before from, crossing
// midnight) and the days it holds, in the server's time.
type RuleWindow struct {
	Days []string `json:"days,omitempty"` // mon to sun; empty: every day
	From string   `json:"from"`
	To   string   `json:"to"`
}

// reviewRules reads the rules layer's list at a selector.
func (s *Service) reviewRules(sel settings.Selector) []ReviewRule {
	var out []ReviewRule
	if s.d.Settings == nil {
		return nil
	}
	r, _, err := s.d.Settings.Value("review.rules", sel)
	if err != nil {
		return nil
	}
	if err := json.Unmarshal(r.Value, &out); err != nil {
		s.d.Log.Warn("the review rules do not read back", "err", err)
		return nil
	}
	return out
}

// ruleVerdict is the rules layer's answer: what a matching rule decided, and which one.
type ruleVerdict struct {
	decision string // approve, deny or pass
	rule     string
	why      string
}

// evaluateRules walks the rules for one waiting request. Denial first, as the design says: one
// matching deny settles it whatever the approves say. The rules that the request's own selector
// resolves (its collection, its tags, its entitlement) are the ones that run.
func (s *Service) evaluateRules(r Request, agent agentInfo) ruleVerdict {
	sel := s.selectorOf(r)
	for _, rule := range s.reviewRules(sel) {
		if rule.Decide == "deny" && rule.matches(r, agent, s) {
			return ruleVerdict{decision: "deny", rule: rule.Name,
				why: fmt.Sprintf("the rule %s matched: %s", rule.Name, rule.conditions())}
		}
	}
	for _, rule := range s.reviewRules(sel) {
		if rule.Decide == "approve" && rule.matches(r, agent, s) {
			// a rule approves only where a rule may: the review level. person is people's by
			// definition, so for a person-level ask an approve is a pass with a note.
			if r.Level == LevelPerson {
				return ruleVerdict{decision: "pass", rule: rule.Name,
					why: fmt.Sprintf("the rule %s matched, but person-level asks are people's to decide", rule.Name)}
			}
			return ruleVerdict{decision: "approve", rule: rule.Name,
				why: fmt.Sprintf("the rule %s matched: %s", rule.Name, rule.conditions())}
		}
	}
	return ruleVerdict{decision: "pass"}
}

// matches says whether every condition the rule names holds for the request.
func (rule ReviewRule) matches(r Request, agent agentInfo, s *Service) bool {
	if rule.Disabled {
		return false
	}
	if len(rule.Levels) > 0 && !containsString(rule.Levels, r.Level) {
		return false
	}
	if len(rule.AgentTags) > 0 {
		worn := false
		for _, want := range rule.AgentTags {
			for _, have := range agent.Tags {
				if tagPatternMatches(want, have) {
					worn = true
				}
			}
		}
		if !worn {
			return false
		}
	}
	if rule.AlertService && !s.requestOnFiringServiceAlert(r) {
		return false
	}
	if rule.ReasonPattern != "" {
		re, err := regexp.Compile(rule.ReasonPattern)
		if err != nil || !re.MatchString(r.Reason) {
			return false
		}
	}
	if rule.Window != nil && !rule.Window.holds(r.Created) {
		return false
	}
	if len(rule.Fields) > 0 {
		if len(r.Fields) == 0 {
			return false // an ask for every field is not an ask within some of them
		}
		for _, f := range r.Fields {
			if !containsString(rule.Fields, f) {
				return false
			}
		}
	}
	return true
}

// conditions lists the conditions a rule names, for the decision's note.
func (rule ReviewRule) conditions() string {
	var parts []string
	if len(rule.Levels) > 0 {
		parts = append(parts, "levels "+strings.Join(rule.Levels, ","))
	}
	if len(rule.AgentTags) > 0 {
		parts = append(parts, "agent tags "+strings.Join(rule.AgentTags, ","))
	}
	if rule.AlertService {
		parts = append(parts, "a firing alert of the items' service")
	}
	if rule.ReasonPattern != "" {
		parts = append(parts, "the reason matches "+rule.ReasonPattern)
	}
	if rule.Window != nil {
		w := rule.Window.From + "-" + rule.Window.To
		if len(rule.Window.Days) > 0 {
			w += " on " + strings.Join(rule.Window.Days, ",")
		}
		parts = append(parts, "the window "+w)
	}
	if len(rule.Fields) > 0 {
		parts = append(parts, "fields "+strings.Join(rule.Fields, ","))
	}
	if len(parts) == 0 {
		return "every ask"
	}
	return strings.Join(parts, "; ")
}

// holds says whether the time sits in the window, in the server's time (the design keeps the
// rules deterministic, and a window's clock is the server's own).
func (w *RuleWindow) holds(t time.Time) bool {
	day := strings.ToLower(t.Weekday().String()[:3])
	if len(w.Days) > 0 && !containsString(w.Days, day) {
		return false
	}
	minute := t.Hour()*60 + t.Minute()
	from, to := hhmm(w.From), hhmm(w.To)
	if from < to {
		return minute >= from && minute < to
	}
	return minute >= from || minute < to // the window crosses midnight
}

func hhmm(s string) int {
	h, m, _ := strings.Cut(s, ":")
	hh, _ := strconv.Atoi(h)
	mm, _ := strconv.Atoi(m)
	return hh*60 + mm
}

// requestOnFiringServiceAlert says whether the ask hangs on an alert group that is firing now and
// whose service label every item of the ask answers with a service/<name> tag — the on-call
// pattern the design names: the keys of the service that is shouting.
func (s *Service) requestOnFiringServiceAlert(r Request) bool {
	if r.Alert == "" || s.d.AlertInfo == nil {
		return false
	}
	firing, labels, ok := s.d.AlertInfo(r.Alert)
	if !ok || !firing {
		return false
	}
	service := labels["service"]
	if service == "" {
		return false
	}
	for _, it := range r.Items {
		if it.ID == "" { // a queued ask the policy has not resolved yet
			return false
		}
		item, _, found := s.Item(it.ID)
		if !found || !containsString(item.Tags, "service/"+service) {
			return false
		}
	}
	return true
}

// selectorOf is the settings selector a request resolves at: its first item's collection and
// tags, and the entitlement it goes through.
func (s *Service) selectorOf(r Request) settings.Selector {
	sel := settings.Selector{Entitlement: r.Entitlement}
	if len(r.Items) > 0 && r.Items[0].ID != "" {
		if item, _, ok := s.Item(r.Items[0].ID); ok {
			if col, found := s.collectionByID(item.Collection); found {
				sel.Collection = col.ID
			}
			sel.Tags = item.Tags
		}
	}
	return sel
}

// ---- the chain itself

// chainFor resolves the layers a request walks: review.chain at the request's own selector, with
// the layers that cannot decide it taken out — person-level asks are people's by definition, and
// an agents layer with no review agent configured could only ever time out.
func (s *Service) chainFor(level string, sel settings.Selector) []string {
	want := s.settingStringsAt("review.chain", sel, Layers)
	var out []string
	for _, l := range want {
		if containsString(Layers, l) && !containsString(out, l) {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		out = append(out, Layers...)
	}
	drop := func(layer string) {
		var keep []string
		for _, l := range out {
			if l != layer {
				keep = append(keep, l)
			}
		}
		out = keep
	}
	if level == LevelPerson {
		drop(LayerAgents)
	}
	if containsString(out, LayerAgents) && len(s.reviewAgentNames()) == 0 {
		drop(LayerAgents)
	}
	return out
}

// reviewAgentNames lists the agents that hold the review scope, for waking and for the chain.
func (s *Service) reviewAgentNames() []string {
	if s.d.ReviewAgents == nil {
		return nil
	}
	return s.d.ReviewAgents()
}

// approvalsNeeded reads review.approvals_required at a selector: a number, or one for each level.
func (s *Service) approvalsNeeded(level string, sel settings.Selector) int {
	need := 1
	if s.d.Settings != nil {
		if r, _, err := s.d.Settings.Value("review.approvals_required", sel); err == nil {
			if n, err := jsonIntTry(r.Value); err == nil {
				need = int(n)
			} else {
				var m map[string]json.RawMessage
				if json.Unmarshal(r.Value, &m) == nil {
					if raw, ok := m[level]; ok {
						if n, err := jsonIntTry(raw); err == nil {
							need = int(n)
						}
					}
				}
			}
		}
	}
	if need < 1 {
		need = 1
	}
	return need
}

func jsonIntTry(v json.RawMessage) (int64, error) {
	var n int64
	if err := json.Unmarshal(v, &n); err != nil {
		return 0, err
	}
	return n, nil
}

// agentMayApprove says whether a review agent may approve the level (review.agent.may_approve);
// person is people's by definition, whatever the setting says.
func (s *Service) agentMayApprove(level string) bool {
	if level == LevelPerson {
		return false
	}
	return containsString(s.settingStrings("review.agent.may_approve", []string{LevelReview}), level)
}

// agentCeilings are the uses and duration a review agent may grant (review.agent.max_uses and
// max_duration; 0: unlimited).
func (s *Service) agentCeilings() (uses, duration int64) {
	return s.settingCountAt("review.agent.max_uses", settings.Selector{}, 0),
		s.settingDurationAt("review.agent.max_duration", settings.Selector{}, 0)
}

// clampAgentGrant fits what a review agent grants under its ceilings.
func clampAgentGrant(uses, duration, maxUses, maxDuration int64) (int64, int64) {
	if maxUses > 0 && (uses == 0 || uses > maxUses) {
		uses = maxUses
	}
	if maxDuration > 0 && (duration == 0 || duration > maxDuration) {
		duration = maxDuration
	}
	return uses, duration
}

// enterRequest starts a waiting request on its chain: it snapshots the layers and the threshold,
// then walks them from the first. It answers the request as it stands after the rules layer ran —
// granted, denied or waiting at a layer with a decider — and the grant, when the rules settled it
// at once. The caller holds no lock; this takes the write lock itself.
func (s *Service) enterRequest(r Request) (Request, *Grant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sel := s.selectorOf(r)
	r.Chain = s.chainFor(r.Level, sel)
	r.Needed = s.approvalsNeeded(r.Level, sel)
	r.Step, r.Approvals, r.Deadline = 0, 0, time.Time{}
	agent, ok := s.agentInfoOf(r.Agent)
	if !ok {
		_ = s.putRequest(r) // the sweep's own handling finds the agent gone
		return r, nil
	}
	for r.Step < len(r.Chain) {
		if r.Chain[r.Step] != LayerRules {
			s.enterHumanLayer(&r)
			return r, nil
		}
		v := s.evaluateRules(r, agent)
		switch v.decision {
		case "deny":
			r.Decisions = append(r.Decisions, Decision{By: v.rule, Kind: "rule", Via: "rules",
				At: s.now(), Note: v.why})
			r.Status = ReqDenied
			s.closeDenied(r)
			return r, nil
		case "approve":
			who := DecideActor{Name: v.rule, Kind: "rule", Via: "rules"}
			final, grant, err := s.grantRequest(who, r, v.why, r.Uses, r.Duration)
			if err != nil {
				s.d.Log.Warn("the rules layer could not grant", "request", r.ID, "err", err)
				r.Decisions = append(r.Decisions, Decision{By: "beacon", Kind: "system", Via: "rules",
					At: s.now(), Note: "the rules approved, but the grant could not be made: " + err.Error()})
				r.Status = ReqDenied
				s.closeDenied(r)
				return r, nil
			}
			return final, &grant
		default: // pass: the next layer, with the rule's note when one matched
			if v.rule != "" {
				r.Decisions = append(r.Decisions, Decision{By: v.rule, Kind: "rule", Via: "rules",
					At: s.now(), Note: v.why})
			}
			r.Step++
		}
	}
	// the chain holds no layer that decides (rules alone that passed, or both human layers taken
	// out): the pending TTL and its consequence take over, as for any ask nobody answers
	r.Deadline = time.Time{}
	_ = s.putRequest(r)
	return r, nil
}

// enterHumanLayer stands a request at the agents or people layer: the deadline of the layer's own,
// the row written, and the layer woken — the review agents' hook, or the approval card. The
// caller holds the write lock; rules layers are never re-entered (they ran when the ask arrived).
func (s *Service) enterHumanLayer(r *Request) {
	switch r.layer() {
	case LayerAgents:
		r.Deadline = s.now().Add(s.agentTimeout())
		_ = s.putRequest(*r)
		s.wakeReviewAgents(*r)
	case LayerPeople:
		r.Deadline = s.now().Add(s.peopleTimeout())
		_ = s.putRequest(*r)
		s.pushApprovalCard(*r)
	default:
		r.Deadline = time.Time{}
		_ = s.putRequest(*r)
	}
}

// wakeReviewAgents sends the secret.request event to every agent holding the review scope: the
// same delivery and signature the on-call hooks carry, so the bot's harness verifies it the same
// way. The payload names the ask and the asker; get_request brings the context.
func (s *Service) wakeReviewAgents(r Request) {
	if len(r.Items) == 0 {
		return
	}
	first := r.Items[0].Ref
	if first == "" {
		first = r.Items[0].Title
	}
	for _, name := range s.reviewAgentNames() {
		if name == r.Agent {
			continue // the requester never reviews its own ask, so it is not woken for it
		}
		s.sendSecretHook(name, "secret.request", hookPayload{Request: r.ID, Requester: r.Agent,
			Item: first, Level: r.Level, Uses: r.Uses, Duration: r.Duration, Reason: r.Reason, Action: r.Action})
	}
}

// agentTimeout is how long the agents layer has (review.agent.timeout, ten minutes by default).
func (s *Service) agentTimeout() time.Duration {
	if d := s.settingDuration("review.agent.timeout", 10*time.Minute); d > 0 {
		return d
	}
	return 10 * time.Minute
}

// peopleTimeout is how long the people layer has (review.people.timeout, a day by default).
func (s *Service) peopleTimeout() time.Duration {
	if d := s.settingDuration("review.people.timeout", 24*time.Hour); d > 0 {
		return d
	}
	return 24 * time.Hour
}

// Pass hands a waiting request to the next layer of its chain without deciding it — the review
// agent's own move when the ask is not one it can judge. Only a review agent may pass, at its own
// layer, with the reason every decision carries.
func (s *Service) Pass(who DecideActor, requestID, note string) (Request, error) {
	if s.keys.sealed() {
		return Request{}, ErrSealed
	}
	if who.Kind != "agent" {
		return Request{}, fmt.Errorf("%w: only a review agent passes a request on", ErrBadInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.Request(requestID)
	if !ok {
		return Request{}, fmt.Errorf("%w: no request %q", ErrNotFound, requestID)
	}
	if r.Status != ReqPending {
		return Request{}, fmt.Errorf("%w: the request is %s already", ErrConflict, r.Status)
	}
	if r.layer() != LayerAgents {
		return Request{}, fmt.Errorf("%w: the chain is not at the review agent's layer", ErrConflict)
	}
	if who.Name == r.Agent {
		return Request{}, ErrSelfApproval
	}
	if s.d.ReviewAgent == nil || !s.d.ReviewAgent(who.Name) {
		return Request{}, fmt.Errorf("%w: only a review agent decides for agents", ErrForbidden)
	}
	if s.separationOwner() {
		if a, ok := s.agentInfoOf(who.Name); ok && a.Owner == r.Owner {
			return Request{}, ErrSelfApproval
		}
	}
	if strings.TrimSpace(note) == "" {
		return Request{}, fmt.Errorf("%w: a decision says why", ErrBadInput)
	}
	r.Decisions = append(r.Decisions, Decision{By: who.Name, Kind: who.Kind, Via: who.Via,
		At: s.now(), Note: "passed on: " + note})
	s.nextLayer(&r)
	_ = s.auditEntry(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "secrets_request_pass",
		Target: r.ID, Detail: fmt.Sprintf("passed %s's request for %d item(s) on: %s", r.Agent, len(r.Items), note)})
	s.notifyDecision("passed on", who.Name, r)
	return r, nil
}

// layer is the layer a request stands at ("" at the chain's end, and for the rows written before
// the chain arrived, which the old single-decision way still decides).
func (r Request) layer() string {
	if r.Step < len(r.Chain) {
		return r.Chain[r.Step]
	}
	return ""
}

// nextLayer moves a request past the layer it stands at, to the next human layer that decides —
// the rules are not re-run — and writes the row and wakes the layer. The caller holds the lock.
func (s *Service) nextLayer(r *Request) {
	for r.Step < len(r.Chain) {
		r.Step++
		if r.Step >= len(r.Chain) {
			break
		}
		if r.Chain[r.Step] != LayerRules {
			s.enterHumanLayer(r)
			return
		}
	}
	r.Deadline = time.Time{}
	_ = s.putRequest(*r)
}

// notifyDecision tells the chat what a review agent decided (review.notify_decisions).
func (s *Service) notifyDecision(what, agent string, r Request) {
	if s.d.Bot == nil || !containsString(s.settingStrings("review.notify_decisions", []string{"feishu"}), "feishu") {
		return
	}
	if len(r.Items) == 0 {
		return
	}
	first := r.Items[0].Title
	if len(r.Items) > 1 {
		first = fmt.Sprintf("%s and %d more", first, len(r.Items)-1)
	}
	_ = s.d.Bot.Send(s.ctx, s.t("The review agent %s %s %s's request for %s.", agent, what, r.Agent, first))
}

// ---- the sweep's half of the chain: the layers' timeouts

// sweepReviewLayers walks the waiting requests whose layer ran out of time and applies the
// consequence its setting names. It answers how many requests it moved.
func (s *Service) sweepReviewLayers(now time.Time) int {
	moved := 0
	for _, r := range s.Requests(ReqPending) {
		layer := r.layer()
		if layer == "" || r.Deadline.IsZero() || !now.After(r.Deadline) {
			continue
		}
		switch layer {
		case LayerAgents:
			switch s.settingString("review.agent.on_timeout", "next") {
			case "deny":
				if _, _, err := s.Decide(DecideActor{Name: "beacon", Kind: "system", Via: "system"}, r.ID, false,
					"the review agent did not decide in time; the setting says deny", nil, nil); err == nil {
					moved++
				}
			case "approve":
				if _, _, err := s.Decide(DecideActor{Name: "beacon", Kind: "system", Via: "system"}, r.ID, true,
					"the review agent did not decide in time; the setting says approve", nil, nil); err == nil {
					moved++
				}
			case "next":
				if s.advanceTimedOut(r.ID, layer) {
					moved++
				}
			default: // wait: the layer keeps waiting; the pending TTL remains the backstop
			}
		case LayerPeople:
			switch s.settingString("review.people.on_timeout", "expire") {
			case "deny":
				if _, _, err := s.Decide(DecideActor{Name: "beacon", Kind: "system", Via: "system"}, r.ID, false,
					"people did not decide in time; the setting says deny", nil, nil); err == nil {
					moved++
				}
			case "approve":
				if _, _, err := s.Decide(DecideActor{Name: "beacon", Kind: "system", Via: "system"}, r.ID, true,
					"people did not decide in time; the setting says approve", nil, nil); err == nil {
					moved++
				}
			default: // expire: the ask lapses, as one nobody answered
				if s.expireByTimeout(r.ID) {
					moved++
				}
			}
		}
	}
	return moved
}

// advanceTimedOut moves a request whose layer timed out to the next layer, under the write lock
// and only if it still stands where the sweep found it.
func (s *Service) advanceTimedOut(id, layer string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.Request(id)
	if !ok || cur.Status != ReqPending || cur.layer() != layer || cur.Deadline.IsZero() || !s.now().After(cur.Deadline) {
		return false
	}
	cur.Decisions = append(cur.Decisions, Decision{By: "beacon", Kind: "system", Via: "system", At: s.now(),
		Note: "the " + layer + " layer ran out of time; the ask moved on"})
	s.nextLayer(&cur)
	_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_request_timeout",
		Target: cur.ID, Detail: "the " + layer + " layer ran out of time; the ask moved to the next layer"})
	return true
}

// expireByTimeout lets a request lapse where the people layer's time ran out.
func (s *Service) expireByTimeout(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.Request(id)
	if !ok || cur.Status != ReqPending || cur.layer() != LayerPeople || cur.Deadline.IsZero() || !s.now().After(cur.Deadline) {
		return false
	}
	cur.Status = ReqExpired
	cur.Decisions = append(cur.Decisions, Decision{By: "beacon", Kind: "system", Via: "system", At: s.now(),
		Note: "people did not decide in time"})
	if err := s.putRequest(cur); err != nil {
		return false
	}
	s.countRequest("denied", "system")
	s.sendSecretHook(cur.Agent, "secret.denied", hookPayload{Request: cur.ID, Item: refsOf(cur.Items)[0],
		Reason: "nobody decided in time"})
	_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_request_timeout",
		Target: cur.ID, Detail: "the people layer ran out of time; the ask expired"})
	return true
}

// settingStringsAt reads a list-of-words setting at a selector, with a default.
func (s *Service) settingStringsAt(key string, sel settings.Selector, def []string) []string {
	if s.d.Settings == nil {
		return def
	}
	r, _, err := s.d.Settings.Value(key, sel)
	if err != nil {
		return def
	}
	var out []string
	if json.Unmarshal(r.Value, &out) != nil || len(out) == 0 {
		return def
	}
	return out
}

// settingStrings reads a list-of-words setting at the system level, with a default.
func (s *Service) settingStrings(key string, def []string) []string {
	if s.d.Settings == nil {
		return def
	}
	r, err := s.d.Settings.System(key)
	if err != nil {
		return def
	}
	var out []string
	if json.Unmarshal(r.Value, &out) != nil {
		return def
	}
	return out
}
