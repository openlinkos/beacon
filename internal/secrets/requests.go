package secrets

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/settings"
	"github.com/openlinkos/beacon/internal/store"
)

// The request: an agent asking for an item, with a reason, uses and a duration. One request may
// name several items or pick them by tags; it is split by the level each item needs — the auto
// part becomes grants at once, the rest waits for a decision, and one call answers with both. A
// waiting request ends by a person's decision (the console or the Feishu card here; the review
// chain's other layers arrive with the next package), by its pending TTL, or by withdrawal.

// Request states.
const (
	ReqPending   = "pending"
	ReqGranted   = "granted"
	ReqDenied    = "denied"
	ReqExpired   = "expired"
	ReqWithdrawn = "withdrawn"
)

// Grant states.
const (
	GrantActive    = "active"
	GrantExhausted = "exhausted"
	GrantExpired   = "expired"
	GrantRevoked   = "revoked"
	GrantReleased  = "released"
)

// Decision is one decision on a request: who made it (a person's account, a review agent, a rule
// or the system), of what kind, what it decided, and why — a decision always carries its reason.
type Decision struct {
	By       string    `json:"by"`   // the account, the agent or the rule
	Kind     string    `json:"kind"` // person, agent, rule or system
	Via      string    `json:"via"`  // console, card, api, mcp or cli
	At       time.Time `json:"at"`
	Approve  bool      `json:"approve"`
	Note     string    `json:"note"`
	Uses     int64     `json:"uses,omitempty"`     // when the decision set them
	Duration int64     `json:"duration,omitempty"` // seconds; 0: never
}

// ReqItem is one item of a request, as the snapshot the design asks for: the item and its version
// when the request was made.
type ReqItem struct {
	ID         string `json:"id"`
	Collection string `json:"collection"` // the collection's name, for showing
	Title      string `json:"title"`
	Version    int64  `json:"version"`
	Level      string `json:"level"` // the approval this item needs
	Ref        string `json:"ref"`
}

// Request is one ask, with its items, its reason and its decisions. Level empty means the request
// was queued while the vault was sealed and has not been resolved against the policy yet. The
// chain fields carry the review chain the ask walks: the layers (snapshotted when the ask was
// made, so a change of the setting moves the asks that come later, not the one in flight), the
// layer it stands at, the approvals that counted, how many it needs, and when the layer it stands
// at runs out of time. Rows from before the chain arrived carry none of these, and are decided
// the old way: one decision of a person or a review agent closes them.
type Request struct {
	ID          string     `json:"id"`
	Agent       string     `json:"agent"`
	Owner       string     `json:"owner"`
	Via         string     `json:"via"`
	Items       []ReqItem  `json:"items"`
	Fields      []string   `json:"fields,omitempty"`
	Action      string     `json:"action"` // the action asked for (read, most often)
	Reason      string     `json:"reason"`
	Uses        int64      `json:"uses"`     // 0: unlimited
	Duration    int64      `json:"duration"` // seconds; 0: never
	Alert       string     `json:"alert,omitempty"`
	Context     string     `json:"context,omitempty"`
	Entitlement string     `json:"entitlement,omitempty"`
	Level       string     `json:"level"` // the strictest approval the items need
	Status      string     `json:"status"`
	Decisions   []Decision `json:"decisions,omitempty"`
	GrantID     string     `json:"grant_id,omitempty"`
	Created     time.Time  `json:"created"`
	ExpiresAt   time.Time  `json:"expires_at,omitzero"` // the pending TTL
	Chain       []string   `json:"chain,omitempty"`     // the layers this ask walks
	Step        int        `json:"step,omitempty"`      // where in the chain it stands
	Approvals   int        `json:"approvals,omitempty"` // approvals that counted
	Needed      int        `json:"needed,omitempty"`    // approvals it needs
	Deadline    time.Time  `json:"deadline,omitzero"`   // when the layer it stands at runs out
}

// RequestIn is what request_secret takes: items by reference or by tags, the fields, the action,
// the reason, the uses and duration asked, the alert the request hangs on, the entitlement it
// means to use, and how long to wait for a decision.
type RequestIn struct {
	Items       []string      `json:"items,omitempty"`
	Tags        []string      `json:"tags,omitempty"`
	Collection  string        `json:"collection,omitempty"`
	Fields      []string      `json:"fields,omitempty"`
	Action      string        `json:"action,omitempty"` // read (default) or use
	Reason      string        `json:"reason"`
	Uses        *int64        `json:"uses,omitempty"`     // nil: the default; 0: unlimited
	Duration    *int64        `json:"duration,omitempty"` // seconds; nil: the default; 0: never
	Alert       string        `json:"alert,omitempty"`
	Context     string        `json:"context,omitempty"`
	Entitlement string        `json:"entitlement,omitempty"`
	Wait        time.Duration `json:"-"`
}

// Refusal is one item a request could not ask for. NotFound is the design's default deny: the
// agent is told the item is not there.
type Refusal struct {
	Ref      string `json:"ref"`
	Reason   string `json:"reason"`
	NotFound bool   `json:"not_found"`
}

// RequestOutcome is request_secret's answer: the grants already in force, the request waiting for
// a decision, and the items that could not be asked for.
type RequestOutcome struct {
	Granted []Grant   `json:"granted"`
	Pending *Request  `json:"pending,omitempty"`
	Refused []Refusal `json:"refused,omitempty"`
}

// RequestSecret is the agent's door. It walks the five steps for every item named or picked by
// tags, splits them by the approval each needs, grants the auto part at once and leaves the rest
// waiting; one call tells the agent which is which. The reason is required (its shortest length
// is a setting, ten characters by default), the uses and duration are clamped by the settings at
// every level, and the owner caps what the agent may ask for. A sealed vault refuses (the
// default) or queues the ask for when it is unsealed again (sealed.on_request).
func (s *Service) RequestSecret(agentName, via string, in RequestIn) (RequestOutcome, error) {
	out := RequestOutcome{Granted: []Grant{}}
	if s.agentSuspended(agentName) {
		return out, ErrSuspendedAgent
	}
	if min := s.reasonMinLength(); min > 0 && int64(len(strings.TrimSpace(in.Reason))) < min {
		return out, fmt.Errorf("%w: the reason is at least %d characters", ErrBadInput, min)
	}
	agent, ok := s.agentInfoOf(agentName)
	if !ok {
		return out, fmt.Errorf("%w: no agent %q", ErrNotFound, agentName)
	}
	if in.Action == "" {
		in.Action = ActRead
	}
	if in.Action != ActRead && in.Action != ActUse {
		return out, fmt.Errorf("%w: a request asks for read or use", ErrBadInput)
	}
	if s.keys.sealed() {
		s.anomalyFired("sealed_state", "a request came while the vault was sealed")
		if !s.sealedQueues() {
			return out, ErrSealed
		}
		// queue: the ask waits with its raw references; the sweep resolves it against the policy
		// once the vault is unsealed again
		req := Request{ID: mustID(), Agent: agent.Name, Owner: agent.Owner, Via: via,
			Action: in.Action, Reason: in.Reason, Alert: in.Alert, Context: in.Context,
			Entitlement: in.Entitlement, Status: ReqPending, Created: s.now(), ExpiresAt: s.now().Add(s.pendingTTL())}
		for _, ref := range in.Items {
			req.Items = append(req.Items, ReqItem{Ref: ref, Title: ref})
		}
		if err := s.putRequest(req); err != nil {
			return out, err
		}
		_ = s.auditEntry(store.AuditEntry{Actor: agent.Name, Via: via, Action: "secrets_request", Target: req.ID,
			Detail: "queued while the vault was sealed"})
		out.Pending = &req
		return out, nil
	}
	// gather the items: the references named, then the tag and collection pick
	type picked struct {
		item Item
		ref  string // the caller's reference, when they named it; empty for a tag pick
	}
	var picks []picked
	seen := map[string]bool{}
	for _, ref := range in.Items {
		item, _, ok := s.Item(ref)
		if !ok || item.State != StateNormal {
			out.Refused = append(out.Refused, hiddenRefusal(ref))
			continue
		}
		if !seen[item.ID] {
			seen[item.ID] = true
			picks = append(picks, picked{item: item, ref: ref})
		}
	}
	if len(in.Tags) > 0 || in.Collection != "" {
		for _, item := range s.itemsBy(in.Collection, in.Tags) {
			if !seen[item.ID] {
				seen[item.ID] = true
				picks = append(picks, picked{item: item})
			}
		}
	}
	if len(picks) == 0 {
		if len(out.Refused) == 0 {
			return out, fmt.Errorf("%w: a request names items or tags", ErrBadInput)
		}
		return out, nil
	}
	// walk the five steps per item and split by the approval each needs
	type ask struct {
		item        Item
		col         Collection
		level       string
		fields      []string
		entitlement string
		uses        int64
		duration    int64
	}
	var autoParts, waiting []ask
	level := LevelAuto
	for _, p := range picks {
		item := p.item
		col, ok := s.collectionByID(item.Collection)
		if !ok {
			if p.ref != "" {
				out.Refused = append(out.Refused, hiddenRefusal(p.ref))
			}
			continue
		}
		pol := s.decidePolicy(agent, item, col, in.Action, in.Entitlement)
		if !pol.OK || !s.ownerMayRead(agent.Owner, col.ID) {
			if p.ref != "" {
				out.Refused = append(out.Refused, hiddenRefusal(p.ref))
			}
			continue // a tag pick that is out of reach is simply not there
		}
		if !containsString(pol.Actions, in.Action) {
			ref := p.ref
			if ref == "" {
				ref = RefOfItem(col.Name, item.Title)
			}
			out.Refused = append(out.Refused, Refusal{Ref: ref, Reason: "the item allows only " + strings.Join(pol.Actions, ", ")})
			continue
		}
		fields, extra := grantFields(in.Fields, pol.Fields)
		if extra {
			ref := p.ref
			if ref == "" {
				ref = RefOfItem(col.Name, item.Title)
			}
			out.Refused = append(out.Refused, Refusal{Ref: ref, Reason: "a field is out of the entitlement"})
			continue
		}
		entName := pol.Entitlement.Name
		sel := settings.Selector{Collection: col.Name, Tags: item.Tags, Entitlement: entName}
		uses, duration, err := s.resolveUsesDuration(in, sel, pol.Level)
		if err != nil {
			return out, err
		}
		if LevelStrictness[pol.Level] > LevelStrictness[level] {
			level = pol.Level
		}
		a := ask{item: item, col: col, level: pol.Level, fields: fields, entitlement: entName, uses: uses, duration: duration}
		if pol.Level == LevelAuto {
			autoParts = append(autoParts, a)
		} else {
			waiting = append(waiting, a)
		}
	}
	if len(autoParts) == 0 && len(waiting) == 0 {
		return out, nil // everything was refused; the refusals say why
	}
	var pendingID string
	var pendingReq Request
	var writeErr error
	func() { // the writes, under the lock
		s.mu.Lock()
		defer s.mu.Unlock()
		sk, e := s.sk()
		if e != nil {
			writeErr = e
			return
		}
		_ = sk
		for _, a := range autoParts {
			grant, gerr := s.createGrant(agent, a.item, a.col, grantSpec{
				action: in.Action, fields: a.fields, uses: a.uses, duration: a.duration,
				alert: in.Alert, level: a.level, entitlement: a.entitlement, reason: in.Reason,
				approver: "policy", approverKind: "system",
			})
			if gerr != nil {
				writeErr = gerr
				return
			}
			out.Granted = append(out.Granted, grant)
			s.countRequest("granted", "policy") // the level itself decided: no person, no rule
		}
		if len(waiting) > 0 {
			req := Request{
				ID: mustID(), Agent: agent.Name, Owner: agent.Owner, Via: via,
				Fields: in.Fields, Action: in.Action, Reason: in.Reason,
				Uses: waiting[0].uses, Duration: waiting[0].duration, Alert: in.Alert, Context: in.Context,
				Entitlement: in.Entitlement, Status: ReqPending, Created: s.now(), Level: level,
				ExpiresAt: s.now().Add(s.pendingTTL()),
			}
			for _, a := range waiting {
				req.Items = append(req.Items, ReqItem{ID: a.item.ID, Collection: a.col.Name, Title: a.item.Title,
					Version: a.item.Version, Level: a.level, Ref: RefOfItem(a.col.Name, a.item.Title)})
			}
			if perr := s.putRequest(req); perr == nil {
				pendingID = req.ID
				pendingReq = req
				_ = s.auditEntry(store.AuditEntry{Actor: agent.Name, Via: via, Action: "secrets_request",
					Target: req.Items[0].Title,
					Detail: fmt.Sprintf("%s asked for %d item(s) at %s: %s", agent.Name, len(req.Items), level, in.Reason)})
				s.countRequest("pending", "agent")
			}
		}
	}()
	if writeErr != nil && len(out.Granted) == 0 && pendingID == "" {
		return out, writeErr
	}
	if pendingID != "" {
		// the review chain takes the waiting part from here: the rules layer runs at once, so an
		// ask the rules settle comes back granted in this very answer, and the rest stands at the
		// layer that decides it (the review agent's hook, or the card)
		final, granted := s.enterRequest(pendingReq)
		switch {
		case final.Status == ReqGranted && granted != nil:
			out.Granted = append(out.Granted, *granted)
		case final.Status == ReqDenied:
			why := "denied"
			if n := len(final.Decisions); n > 0 {
				why = final.Decisions[n-1].Note
			}
			for _, it := range final.Items {
				ref := it.Ref
				if ref == "" {
					ref = RefOfItem(it.Collection, it.Title)
				}
				out.Refused = append(out.Refused, Refusal{Ref: ref, Reason: why})
			}
		default:
			out.Pending = &final
			if in.Wait > 0 {
				out.Pending = s.waitRequest(pendingID, in.Wait)
			}
		}
	}
	// the auto part's grants are in place; the hooks go out
	for _, g := range out.Granted {
		dur := int64(0)
		if !g.ExpiresAt.IsZero() {
			dur = int64(g.ExpiresAt.Sub(g.Created).Seconds())
		}
		s.sendSecretHook(g.Agent, "secret.granted", hookPayload{Grant: g.ID, Item: g.Item,
			Level: g.Level, Uses: g.Uses, Duration: dur, Reason: in.Reason})
	}
	return out, nil
}

// resolveUsesDuration fits the ask to the settings at the selector: the defaults when the ask
// names nothing, the ceilings never passed (unlimited is 0).
func (s *Service) resolveUsesDuration(in RequestIn, sel settings.Selector, level string) (int64, int64, error) {
	uses := s.settingCountAt("request.default_uses", sel, 0)
	if in.Uses != nil {
		uses = *in.Uses
	}
	if uses < 0 {
		return 0, 0, fmt.Errorf("%w: the uses are a count or 0 for unlimited", ErrBadInput)
	}
	if max := s.settingCountAt("request.max_uses", sel, 0); max > 0 && (uses == 0 || uses > max) {
		uses = max
	}
	duration := int64(30 * 24 * time.Hour / time.Second)
	if d, ok := s.settingLevelsDurationAt("request.default_duration", level, sel); ok {
		duration = d
	} else {
		switch level {
		case LevelAuto:
			duration = 0 // no expiry
		case LevelPerson:
			duration = int64(7 * 24 * time.Hour / time.Second)
		}
	}
	if in.Duration != nil {
		duration = *in.Duration
	}
	if duration < 0 {
		return 0, 0, fmt.Errorf("%w: the duration is seconds or 0 for no expiry", ErrBadInput)
	}
	if max := s.settingDurationAt("request.max_duration", sel, 0); max > 0 && (duration == 0 || duration > max) {
		duration = max
	}
	return uses, duration, nil
}

// itemsBy picks the normal-state items by collection and tags (any item wearing every tag).
func (s *Service) itemsBy(collection string, tags []string) []Item {
	var out []Item
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketItems, kv.Range{}, func(_, v []byte) bool {
			var r signedItem
			if json.Unmarshal(v, &r) != nil || !s.itemVerifies(r) || r.State != StateNormal {
				return true
			}
			if collection != "" {
				col, ok := s.collectionByIDTx(tx, r.Collection)
				if !ok || (col.Name != collection && col.ID != collection) {
					return true
				}
			}
			for _, t := range tags {
				if !containsString(r.Tags, t) {
					return true
				}
			}
			out = append(out, r.Item)
			return true
		})
		return nil
	})
	return out
}

// putRequest writes a request row.
func (s *Service) putRequest(r Request) error {
	return s.d.Store.Storage().Update(func(tx kv.Tx) error {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return tx.Put(bucketRequests, []byte(r.ID), b)
	})
}

// Request finds one request by ID.
func (s *Service) Request(id string) (Request, bool) {
	var out Request
	ok := false
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		v := tx.Get(bucketRequests, []byte(id))
		ok = v != nil && json.Unmarshal(v, &out) == nil
		return nil
	})
	return out, ok
}

// Requests lists requests by state (every state when empty), newest first.
func (s *Service) Requests(state string) []Request {
	var out []Request
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketRequests, kv.Range{}, func(_, v []byte) bool {
			var r Request
			if json.Unmarshal(v, &r) == nil && (state == "" || r.Status == state) {
				out = append(out, r)
			}
			return true
		})
		return nil
	})
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Withdraw takes a waiting request back; only the agent that asked may.
func (s *Service) Withdraw(agentName, id, via string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.Request(id)
	if !ok || r.Agent != agentName {
		return fmt.Errorf("%w: no request %q of yours", ErrNotFound, id)
	}
	if r.Status != ReqPending {
		return fmt.Errorf("%w: only a waiting request can be withdrawn", ErrConflict)
	}
	r.Status = ReqWithdrawn
	if err := s.putRequest(r); err != nil {
		return err
	}
	_ = s.auditEntry(store.AuditEntry{Actor: agentName, Via: via, Action: "secrets_request_withdraw", Target: id,
		Detail: "the request was withdrawn"})
	return nil
}

// waitRequest polls a request until it leaves pending or the wait runs out.
func (s *Service) waitRequest(id string, wait time.Duration) *Request {
	deadline := s.now().Add(wait)
	for {
		r, ok := s.Request(id)
		if !ok {
			return nil
		}
		if r.Status != ReqPending {
			return &r
		}
		if !s.now().Before(deadline) {
			return &r
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// DecideActor is who decides: a person (with their role), a review agent, or a rule (the next
// package). Kind is person, agent or rule.
type DecideActor struct {
	Name string
	Kind string // person, agent or rule
	Via  string
	Role string // a person's role
}

// Decide closes or moves a waiting request. Who may, at which layer of the ask's chain:
//
//   - a review agent (the secrets:review scope), at the chain's agents layer — the levels
//     review.agent.may_approve names, within the uses and duration ceilings of its own. person-level
//     asks are people's by definition, so no agent approves them;
//   - a person of the operator or owner role, or a reviewer the entitlement names, at the people
//     layer — the card's and the console's decisions;
//   - the system, when a layer ran out of time and its setting says what to do then.
//
// The requester itself never decides its own request — the design's second invariant — and with
// review.separation at owner, neither does the requester's owner, nor a review agent of that
// owner. Every decision carries its reason. A denial ends the ask whoever it came from. An
// approval counts toward the ask's threshold (review.approvals_required): the last one needed
// makes the grants, the rest move the ask to the next layer. Approving may set the uses and
// duration, inside the ceilings the settings give; uses 1 is the card's "approve once". Requests
// from before the chain arrived (no layers on the row) close on one decision, the old way.
func (s *Service) Decide(who DecideActor, requestID string, approve bool, note string, uses, duration *int64) (Request, Grant, error) {
	if s.keys.sealed() {
		return Request{}, Grant{}, ErrSealed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.Request(requestID)
	if !ok {
		return Request{}, Grant{}, fmt.Errorf("%w: no request %q", ErrNotFound, requestID)
	}
	if r.Status != ReqPending {
		return Request{}, Grant{}, fmt.Errorf("%w: the request is %s already", ErrConflict, r.Status)
	}
	if r.Level == "" {
		return Request{}, Grant{}, fmt.Errorf("%w: the request is still being resolved against the policy", ErrConflict)
	}
	if strings.TrimSpace(note) == "" {
		return Request{}, Grant{}, fmt.Errorf("%w: a decision says why", ErrBadInput)
	}
	layer := r.layer()
	switch who.Kind {
	case "agent":
		// the invariant: the requester cannot review its own request
		if who.Name == r.Agent {
			return Request{}, Grant{}, ErrSelfApproval
		}
		if s.d.ReviewAgent == nil || !s.d.ReviewAgent(who.Name) {
			return Request{}, Grant{}, fmt.Errorf("%w: only a review agent decides for agents", ErrForbidden)
		}
		if s.separationOwner() {
			if a, ok := s.agentInfoOf(who.Name); ok && a.Owner == r.Owner {
				return Request{}, Grant{}, ErrSelfApproval
			}
		}
		if layer != "" && layer != LayerAgents {
			return Request{}, Grant{}, fmt.Errorf("%w: the chain is at its %s layer, not the review agent's", ErrConflict, layer)
		}
		if approve && !s.agentMayApprove(r.Level) {
			return Request{}, Grant{}, fmt.Errorf("%w: the %s level is not one a review agent may approve", ErrForbidden, r.Level)
		}
	case "person":
		if !s.personMayDecide(who, r) {
			return Request{}, Grant{}, fmt.Errorf("%w: an operator, an owner or a named reviewer decides", ErrForbidden)
		}
		// the stricter separation, when configured: the requester's owner stands aside too
		if s.separationOwner() && who.Name == r.Owner {
			return Request{}, Grant{}, ErrSelfApproval
		}
		if layer != "" && layer != LayerPeople {
			return Request{}, Grant{}, fmt.Errorf("%w: the chain is at its %s layer, not people's", ErrConflict, layer)
		}
	case "system":
		// the timeout's consequences (the sweep), acting as the configuration says
	default:
		return Request{}, Grant{}, fmt.Errorf("%w: a decision comes from a person or a review agent", ErrBadInput)
	}
	if !approve {
		r.Status = ReqDenied
		r.Decisions = append(r.Decisions, Decision{By: who.Name, Kind: who.Kind, Via: who.Via, At: s.now(), Note: note})
		s.closeDenied(r)
		if who.Kind == "agent" {
			s.notifyDecision("denied", who.Name, r)
		}
		return r, Grant{}, nil
	}
	// the uses and duration the decision gives, inside each item's ceilings — and, for a review
	// agent, inside its own as well
	in := RequestIn{Uses: &r.Uses, Duration: &r.Duration}
	if uses != nil {
		in.Uses = uses
	}
	if duration != nil {
		in.Duration = duration
	}
	outUses, outDuration, err := s.decisionBounds(r, in, who)
	if err != nil {
		return r, Grant{}, err
	}
	// the threshold: one more approval may not be the one the ask waits for
	if layer != "" && r.Approvals+1 < r.Needed {
		r.Approvals++
		r.Decisions = append(r.Decisions, Decision{By: who.Name, Kind: who.Kind, Via: who.Via, At: s.now(),
			Approve: true, Note: note, Uses: outUses, Duration: outDuration})
		s.nextLayer(&r)
		_ = s.auditEntry(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "secrets_request_approve",
			Target: r.ID,
			Detail: fmt.Sprintf("approved %s's request for %d item(s) (%d of %d approvals): %s",
				r.Agent, len(r.Items), r.Approvals, r.Needed, note)})
		s.countRequest("pending", who.Kind)
		if who.Kind == "agent" {
			s.notifyDecision("approved", who.Name, r)
		}
		return r, Grant{}, nil
	}
	askedUses, askedDuration := r.Uses, r.Duration
	if in.Uses != nil {
		askedUses = *in.Uses
	}
	if in.Duration != nil {
		askedDuration = *in.Duration
	}
	return s.grantRequest(who, r, note, askedUses, askedDuration)
}

// decisionBounds is the uses and duration a mid-chain approval records: the first item's
// ceilings, then a review agent's own.
func (s *Service) decisionBounds(r Request, in RequestIn, who DecideActor) (int64, int64, error) {
	sel := settings.Selector{Entitlement: r.Entitlement, Request: r.ID}
	if len(r.Items) > 0 {
		if item, col, found := s.Item(r.Items[0].ID); found {
			sel.Collection, sel.Tags = col.Name, item.Tags
		}
	}
	outUses, outDuration, err := s.resolveUsesDuration(in, sel, r.Level)
	if err != nil {
		return 0, 0, err
	}
	if who.Kind == "agent" {
		maxUses, maxDuration := s.agentCeilings()
		outUses, outDuration = clampAgentGrant(outUses, outDuration, maxUses, maxDuration)
	}
	return outUses, outDuration, nil
}

// grantRequest makes the grants a settled request turns into and closes it granted: the policy is
// re-run over each item (it may have tightened while the ask waited), uses and duration are
// clamped per item, and the agent's hook hears of the grant. The caller holds the write lock.
func (s *Service) grantRequest(who DecideActor, r Request, note string, uses, duration int64) (Request, Grant, error) {
	in := RequestIn{Uses: &uses, Duration: &duration}
	agent, ok := s.agentInfoOf(r.Agent)
	if !ok {
		return r, Grant{}, fmt.Errorf("%w: the requesting agent is gone", ErrNotFound)
	}
	var first Grant
	var lastUses, lastDuration int64
	for _, it := range r.Items {
		item, col, found := s.Item(it.ID)
		if !found || item.State != StateNormal {
			return r, Grant{}, fmt.Errorf("%w: an item of the request is gone", ErrNotFound)
		}
		// the policy may have tightened while the request waited
		pol := s.decidePolicy(agent, item, col, r.Action, r.Entitlement)
		if !pol.OK || pol.Level == LevelNever {
			return r, Grant{}, fmt.Errorf("%w: the policy tightened while the request waited; ask again", ErrConflict)
		}
		fields, extra := grantFields(r.Fields, pol.Fields)
		if extra {
			return r, Grant{}, fmt.Errorf("%w: a field is out of the entitlement", ErrForbidden)
		}
		sel := settings.Selector{Collection: col.Name, Tags: item.Tags, Entitlement: pol.Entitlement.Name, Request: r.ID}
		outUses, outDuration, err := s.resolveUsesDuration(in, sel, pol.Level)
		if err != nil {
			return r, Grant{}, err
		}
		if who.Kind == "agent" {
			maxUses, maxDuration := s.agentCeilings()
			outUses, outDuration = clampAgentGrant(outUses, outDuration, maxUses, maxDuration)
		}
		grant, err := s.createGrant(agent, item, col, grantSpec{
			action: r.Action, fields: fields, uses: outUses, duration: outDuration,
			alert: r.Alert, level: pol.Level, entitlement: pol.Entitlement.Name, reason: r.Reason,
			approver: who.Name, approverKind: who.Kind, request: r.ID,
		})
		if err != nil {
			return r, Grant{}, err
		}
		lastUses, lastDuration = outUses, outDuration
		if first.ID == "" {
			first = grant
		}
	}
	r.Status = ReqGranted
	r.Uses, r.Duration = lastUses, lastDuration
	r.GrantID = first.ID
	r.Decisions = append(r.Decisions, Decision{By: who.Name, Kind: who.Kind, Via: who.Via, At: s.now(), Approve: true,
		Note: note, Uses: lastUses, Duration: lastDuration})
	if err := s.putRequest(r); err != nil {
		return r, Grant{}, err
	}
	s.countRequest("granted", who.Kind)
	_ = s.auditEntry(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "secrets_request_approve", Target: r.ID,
		Detail: fmt.Sprintf("approved %s's request for %d item(s): %s", r.Agent, len(r.Items), note)})
	s.sendSecretHook(r.Agent, "secret.granted", hookPayload{Request: r.ID, Grant: first.ID,
		Item: r.Items[0].Ref, Level: r.Level, Uses: lastUses, Duration: lastDuration, Reason: note})
	if who.Kind == "agent" {
		s.notifyDecision("approved", who.Name, r)
	}
	return r, first, nil
}

// closeDenied writes a denied request away: the row, the counters, the audit entry, the agent's
// hook and the denial-spike anomaly. The caller holds the write lock and has set the status and
// appended the decision.
func (s *Service) closeDenied(r Request) {
	if err := s.putRequest(r); err != nil {
		s.d.Log.Warn("a denied request could not be written", "request", r.ID, "err", err)
	}
	if len(r.Decisions) == 0 {
		return
	}
	last := r.Decisions[len(r.Decisions)-1]
	s.countRequest("denied", last.Kind)
	_ = s.auditEntry(store.AuditEntry{Actor: last.By, Via: last.Via, Action: "secrets_request_deny", Target: r.ID,
		Detail: fmt.Sprintf("denied %s's request for %d item(s): %s", r.Agent, len(r.Items), last.Note)})
	s.sendSecretHook(r.Agent, "secret.denied", hookPayload{Request: r.ID, Item: r.Items[0].Ref, Reason: last.Note})
	s.anomalyDenial(r.Agent)
}

// hiddenRefusal is the design's default deny: an item the agent may not see is the same as one
// that is not there, with the same words and no title.
func hiddenRefusal(ref string) Refusal {
	return Refusal{Ref: ref, Reason: "there is no such item", NotFound: true}
}

// personMayDecide says whether the person may decide this request: an operator or an owner, or a
// reviewer the request's entitlement names (people:NAME).
func (s *Service) personMayDecide(who DecideActor, r Request) bool {
	switch who.Role {
	case "owner", "operator":
		return true
	}
	if r.Entitlement != "" {
		if e, ok := s.Entitlement(r.Entitlement); ok {
			for _, rev := range e.Reviewers {
				if name, found := strings.CutPrefix(rev, "people:"); found && name == who.Name {
					return true
				}
			}
		}
	}
	return false
}

// separationOwner reads review.separation: whether the requester's owner must stand aside too.
func (s *Service) separationOwner() bool {
	if s.d.Settings == nil {
		return false
	}
	r, err := s.d.Settings.System("review.separation")
	if err != nil {
		return false
	}
	var str string
	return json.Unmarshal(r.Value, &str) == nil && str == "owner"
}

// reasonMinLength is the shortest a request's reason may be (request.reason_min_length).
func (s *Service) reasonMinLength() int64 { return s.settingInt("request.reason_min_length", 10) }

// pendingTTL is how long a request may wait (request.pending_ttl, a day by default).
func (s *Service) pendingTTL() time.Duration {
	if d := s.settingDuration("request.pending_ttl", 24*time.Hour); d > 0 {
		return d
	}
	return 24 * time.Hour
}

// sealedQueues says whether a sealed vault queues requests (sealed.on_request) rather than
// refusing them.
func (s *Service) sealedQueues() bool {
	if s.d.Settings == nil {
		return false
	}
	r, err := s.d.Settings.System("sealed.on_request")
	if err != nil {
		return false
	}
	var str string
	return json.Unmarshal(r.Value, &str) == nil && str == "queue"
}
