package secrets

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/settings"
	"github.com/openlinkos/beacon/internal/signedhead"
	"github.com/openlinkos/beacon/internal/store"
)

// The grant: what a request turned into. It names the agent, the item, the fields, the action,
// the uses (unlimited when 0), the expiry (never when zero), the renewal mode, the alert it is
// bound to and the request and approver it came from. It is signed, like every policy row: a
// grant that fails its signature is treated as absent and reported. A grant ends by its uses
// running out, by its expiry, by revocation, by release, or — when it is bound to an alert — by
// the alert resolving and the grace passing.

// Grant is one grant, signed.
type Grant struct {
	ID           string    `json:"id"`
	Agent        string    `json:"agent"`
	Owner        string    `json:"owner"`
	Item         string    `json:"item"`       // the item's ID
	Collection   string    `json:"collection"` // the collection's ID
	Fields       []string  `json:"fields,omitempty"`
	Action       string    `json:"action"` // read or use
	Uses         int64     `json:"uses"`   // 0: unlimited
	Used         int64     `json:"used"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"` // zero: never
	Alert        string    `json:"alert,omitempty"`
	Renewal      string    `json:"renewal"` // auto, request or none
	Status       string    `json:"status"`
	Request      string    `json:"request,omitempty"`
	Approver     string    `json:"approver"`
	ApproverKind string    `json:"approver_kind"`
	Entitlement  string    `json:"entitlement,omitempty"`
	Level        string    `json:"level"`
	Reason       string    `json:"reason,omitempty"`
	NoticedAt    time.Time `json:"noticed_at,omitzero"` // when the expiry notice went out
	Actor        string    `json:"actor,omitempty"`
	Via          string    `json:"via,omitempty"`
	Created      time.Time `json:"created"`
	Sig          []byte    `json:"sig"`
}

func (g Grant) signedParts() []string {
	return []string{g.ID, g.Agent, g.Owner, g.Item, g.Collection, strings.Join(g.Fields, "\x01"), g.Action,
		i64(g.Uses), i64(g.Used), i64(g.ExpiresAt.UnixNano()), g.Alert, g.Renewal, g.Status, g.Request,
		g.Approver, g.ApproverKind, g.Entitlement, g.Level, i64(g.NoticedAt.UnixNano()), g.Actor, g.Via, i64(g.Created.UnixNano())}
}

// Left is how many uses remain (0 with Uses 0 means unlimited, not none; -1 when used up).
func (g Grant) Left() int64 {
	if g.Uses == 0 {
		return 0
	}
	return g.Uses - g.Used
}

// grantSpec is what a new grant is made of.
type grantSpec struct {
	action                            string
	fields                            []string
	alert, level, entitlement, reason string
	uses, duration                    int64
	approver, approverKind, request   string
}

// createGrant writes one grant row, signed. The caller holds the service's write lock.
func (s *Service) createGrant(agent agentInfo, item Item, col Collection, spec grantSpec) (Grant, error) {
	if err := s.refuseIfRotating(); err != nil {
		return Grant{}, err
	}
	sk, err := s.sk()
	if err != nil {
		return Grant{}, err
	}
	g := Grant{
		ID: mustID(), Agent: agent.Name, Owner: agent.Owner, Item: item.ID, Collection: col.ID,
		Fields: spec.fields, Action: spec.action, Uses: spec.uses, Alert: spec.alert,
		Renewal: s.renewalMode(spec.level), Status: GrantActive, Request: spec.request,
		Approver: spec.approver, ApproverKind: spec.approverKind, Entitlement: spec.entitlement,
		Level: spec.level, Reason: spec.reason, Actor: agent.Name, Via: "system", Created: s.now(),
	}
	if spec.duration > 0 {
		g.ExpiresAt = g.Created.Add(time.Duration(spec.duration) * time.Second)
	}
	g.Sig = signRow(sk, g.signedParts()...)
	var committed signedhead.Head
	err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		b, err := json.Marshal(g)
		if err != nil {
			return err
		}
		if err := tx.Put(bucketGrants, []byte(g.ID), b); err != nil {
			return err
		}
		h, err := s.commitGrantRow(tx, g, b)
		committed = h
		return err
	})
	if err != nil {
		return Grant{}, err
	}
	s.afterHeads(committed)
	_ = s.auditEntry(store.AuditEntry{Actor: spec.approver, Via: "system", Action: "secrets_grant",
		Target: item.Title, Detail: fmt.Sprintf("a grant for %s over %s in %s (%s)", agent.Name, item.Title, col.Name, spec.level)})
	return g, nil
}

// putGrantTx writes a grant row inside a transaction the caller holds and returns the stored bytes.
func putGrantTx(tx kv.Tx, g Grant, sk []byte) ([]byte, error) {
	g.Sig = signRow(sk, g.signedParts()...)
	b, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	if err := tx.Put(bucketGrants, []byte(g.ID), b); err != nil {
		return nil, err
	}
	return b, nil
}

// commitGrantRow names an active grant in the grants head, or drops it when it is no longer active.
func (s *Service) commitGrantRow(tx kv.Tx, g Grant, raw []byte) (signedhead.Head, error) {
	if g.Status == GrantActive {
		return s.commitGrants(tx, map[string][]byte{g.ID: raw}, nil)
	}
	return s.commitGrants(tx, nil, []string{g.ID})
}

// grantVerifies is the signature check a grant passes to be served at all.
func (s *Service) grantVerifies(g Grant) bool {
	sk, err := s.sk()
	if err != nil {
		return false
	}
	return verifyRow(sk, g.Sig, g.signedParts()...)
}

// grants walks the active grant rows the grants head still names, verified ones only.
func (s *Service) grants(fn func(g Grant) bool) {
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		res := s.loadGrants(tx)
		if res.Status != signedhead.StatusOK && res.Status != signedhead.StatusCatchUp {
			return nil
		}
		for id := range res.Head.Entries {
			if isHeadKey([]byte(id)) {
				continue
			}
			v := tx.Get(bucketGrants, []byte(id))
			if !s.grantAccept(tx, id, v) {
				continue
			}
			var g Grant
			if json.Unmarshal(v, &g) != nil || !s.grantVerifies(g) || g.Status != GrantActive {
				continue
			}
			if !fn(g) {
				return nil
			}
		}
		return nil
	})
}

// grantPolicyHolds re-runs the policy around a live grant: the item must be normal, never is
// refused, a deny or a missing entitlement ends it, and a stricter approval or narrower fields
// than when it was granted refuse the use or the renewal.
func (s *Service) grantPolicyHolds(agent agentInfo, g Grant, item Item, col Collection) error {
	if item.State != StateNormal {
		return ErrNoGrant
	}
	if s.EffectiveLevel(item) == LevelNever {
		return fmt.Errorf("%w: there is no such item", ErrNotFound)
	}
	pol := s.decidePolicy(agent, item, col, g.Action, g.Entitlement)
	if !pol.OK {
		return ErrNoGrant
	}
	if LevelStrictness[pol.Level] > LevelStrictness[g.Level] {
		return ErrNoGrant
	}
	if !containsString(pol.Actions, g.Action) {
		return ErrNoGrant
	}
	if len(pol.Fields) > 0 {
		if len(g.Fields) == 0 {
			return ErrNoGrant
		}
		for _, f := range g.Fields {
			if !containsString(pol.Fields, f) {
				return ErrNoGrant
			}
		}
	}
	return nil
}

// GrantsOf lists an agent's grants, newest first.
func (s *Service) GrantsOf(agent string) []Grant {
	var out []Grant
	s.grants(func(g Grant) bool {
		if g.Agent == agent {
			out = append(out, g)
		}
		return true
	})
	sortGrants(out)
	return out
}

// AllGrants lists the grants the signed head still names (active grants), newest first, with a
// state filter (every named grant when empty).
func (s *Service) AllGrants(state string) []Grant {
	var out []Grant
	s.grants(func(g Grant) bool {
		if state == "" || g.Status == state {
			out = append(out, g)
		}
		return true
	})
	sortGrants(out)
	return out
}

func sortGrants(out []Grant) {
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
}

// grantLive says whether the grant stands: active, uses left (or unlimited), not past its expiry,
// and — when bound to an alert — the alert firing or inside its grace.
func (s *Service) grantLive(g Grant) bool {
	if g.Status != GrantActive {
		return false
	}
	if g.Uses > 0 && g.Used >= g.Uses {
		return false
	}
	if !g.ExpiresAt.IsZero() && s.now().After(g.ExpiresAt) {
		return false
	}
	if g.Alert != "" && s.d.AlertState != nil {
		firing, resolvedAt, ok := s.d.AlertState(g.Alert)
		if ok && !firing {
			grace := s.alertGrace()
			if resolvedAt.IsZero() || s.now().After(resolvedAt.Add(grace)) {
				return false
			}
		}
	}
	return true
}

// activeGrant finds the live grant an agent holds over an item for an action, covering the field
// asked for. Several may cover; the newest wins.
func (s *Service) activeGrant(agent, itemID, field, action string) (Grant, bool) {
	var out Grant
	found := false
	s.grants(func(g Grant) bool {
		if g.Agent == agent && g.Item == itemID && g.Action == action && s.grantLive(g) &&
			(len(g.Fields) == 0 || field == "" || containsString(g.Fields, field)) {
			out, found = g, true
		}
		return true
	})
	return out, found
}

// holdsGrant says whether the agent holds any live grant over the item (the search shows it).
func (s *Service) holdsGrant(agent, itemID string) bool {
	_, ok := s.activeGrant(agent, itemID, "", ActRead)
	if ok {
		return true
	}
	_, ok = s.activeGrant(agent, itemID, "", ActUse)
	return ok
}

// RenewGrant renews one of the agent's grants. The renewal mode comes from the level the grant
// needed (grant.renewal): auto renews at once, none refuses, and request asks again — a new
// waiting request that carries this grant's reason, which a person then decides.
func (s *Service) RenewGrant(agentName, id, reason, via string) (Grant, *Request, error) {
	if s.keys.sealed() {
		return Grant{}, nil, ErrSealed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refuseIfRotating(); err != nil {
		return Grant{}, nil, err
	}
	sk, err := s.sk()
	if err != nil {
		return Grant{}, nil, err
	}
	var g Grant
	err = s.d.Store.Storage().View(func(tx kv.Tx) error {
		v := tx.Get(bucketGrants, []byte(id))
		if v == nil || json.Unmarshal(v, &g) != nil || !s.grantVerifies(g) {
			return fmt.Errorf("%w: no grant %q of yours", ErrNotFound, id)
		}
		if g.Agent != agentName {
			return fmt.Errorf("%w: no grant %q of yours", ErrNotFound, id)
		}
		if g.Status != GrantActive {
			return fmt.Errorf("%w: the grant is %s", ErrConflict, g.Status)
		}
		return nil
	})
	if err != nil {
		return Grant{}, nil, err
	}
	item, col, ok := s.Item(g.Item)
	if !ok {
		return Grant{}, nil, fmt.Errorf("%w: the item is gone", ErrNotFound)
	}
	ag, agOK := s.agentInfoOf(g.Agent)
	if !agOK {
		return Grant{}, nil, fmt.Errorf("%w: no grant %q of yours", ErrNotFound, id)
	}
	if err := s.grantPolicyHolds(ag, g, item, col); err != nil {
		return Grant{}, nil, err
	}
	if !s.ownerMayRead(ag.Owner, col.ID) {
		return Grant{}, nil, fmt.Errorf("%w: the agent's owner may no longer read this collection", ErrForbidden)
	}
	if reason == "" {
		reason = g.Reason // a renewal may ride on the reason it started with
	}
	mode := g.Renewal
	if mode == "" {
		mode = s.renewalMode(g.Level)
	}
	var out Grant
	var outReq *Request
	var committed signedhead.Head
	err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		v := tx.Get(bucketGrants, []byte(id))
		var cur Grant
		if v == nil || json.Unmarshal(v, &cur) != nil || !s.grantVerifies(cur) || cur.Agent != agentName {
			return fmt.Errorf("%w: no grant %q of yours", ErrNotFound, id)
		}
		if cur.Status != GrantActive {
			return fmt.Errorf("%w: the grant is %s", ErrConflict, cur.Status)
		}
		switch mode {
		case "none":
			return fmt.Errorf("%w: this grant does not renew", ErrForbidden)
		case "auto":
			d := s.renewalDuration(cur.Level)
			if d > 0 {
				cur.ExpiresAt = s.now().Add(time.Duration(d) * time.Second)
			} else {
				cur.ExpiresAt = time.Time{}
			}
			cur.NoticedAt = time.Time{}
			b, err := putGrantTx(tx, cur, sk)
			if err != nil {
				return err
			}
			h, err := s.commitGrantRow(tx, cur, b)
			if err != nil {
				return err
			}
			committed, out = h, cur
			return nil
		default: // request: a new ask, decided like any other
			item, _, ok := s.itemTx(tx, cur.Item)
			if !ok {
				return fmt.Errorf("%w: the item is gone", ErrNotFound)
			}
			dur := int64(0)
			if !cur.ExpiresAt.IsZero() {
				dur = int64(cur.ExpiresAt.Sub(s.now()).Seconds())
				if dur < 0 {
					dur = 0
				}
			}
			req := Request{ID: mustID(), Agent: cur.Agent, Owner: cur.Owner, Via: via,
				Action: cur.Action, Reason: reason, Uses: cur.Uses, Duration: dur, Alert: cur.Alert,
				Entitlement: cur.Entitlement, Status: ReqPending, Level: cur.Level, Fields: cur.Fields,
				Created: s.now(), ExpiresAt: s.now().Add(s.pendingTTL()),
				Items: []ReqItem{{ID: item.ID, Title: item.Title, Version: item.Version, Level: cur.Level,
					Ref: RefOfItem(s.collectionName(item.Collection), item.Title)}}}
			b, err := json.Marshal(req)
			if err != nil {
				return err
			}
			if err := tx.Put(bucketRequests, []byte(req.ID), b); err != nil {
				return err
			}
			out, outReq = cur, &req
			return nil
		}
	})
	if err != nil {
		return Grant{}, nil, err
	}
	s.afterHeads(committed)
	if outReq != nil {
		s.pushApprovalCard(*outReq)
		_ = s.auditEntry(store.AuditEntry{Actor: agentName, Via: via, Action: "secrets_grant_renew",
			Target: id, Detail: "renewal asked again: " + reason})
		return out, outReq, nil
	}
	_ = s.auditEntry(store.AuditEntry{Actor: agentName, Via: via, Action: "secrets_grant_renew",
		Target: id, Detail: "renewed: " + reason})
	return out, nil, nil
}

// ReleaseGrant lets the agent give a grant back early.
func (s *Service) ReleaseGrant(agentName, id, via string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refuseIfRotating(); err != nil {
		return err
	}
	sk, err := s.sk()
	if err != nil {
		return err
	}
	var released Grant
	var committed signedhead.Head
	err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		v := tx.Get(bucketGrants, []byte(id))
		var g Grant
		if v == nil || json.Unmarshal(v, &g) != nil || !s.grantVerifies(g) || g.Agent != agentName {
			return fmt.Errorf("%w: no grant %q of yours", ErrNotFound, id)
		}
		if g.Status != GrantActive {
			return fmt.Errorf("%w: the grant is %s", ErrConflict, g.Status)
		}
		g.Status = GrantReleased
		b, err := putGrantTx(tx, g, sk)
		if err != nil {
			return err
		}
		h, err := s.commitGrantRow(tx, g, b)
		if err != nil {
			return err
		}
		committed, released = h, g
		return nil
	})
	if err != nil {
		return err
	}
	s.afterHeads(committed)
	_ = s.auditEntry(store.AuditEntry{Actor: agentName, Via: via, Action: "secrets_grant_release", Target: id,
		Detail: "the grant was released"})
	s.sendSecretHook(released.Agent, "secret.revoked", hookPayload{Grant: released.ID, Item: released.Item, Reason: "released"})
	return nil
}

// RevokeGrant ends a grant by an operator's hand.
func (s *Service) RevokeGrant(who Actor, id, reason string) error {
	if s.keys.sealed() {
		return ErrSealed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refuseIfRotating(); err != nil {
		return err
	}
	sk, err := s.sk()
	if err != nil {
		return err
	}
	var revoked Grant
	var committed signedhead.Head
	err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		v := tx.Get(bucketGrants, []byte(id))
		var g Grant
		if v == nil || json.Unmarshal(v, &g) != nil || !s.grantVerifies(g) {
			return fmt.Errorf("%w: no grant %q", ErrNotFound, id)
		}
		if g.Status != GrantActive {
			return fmt.Errorf("%w: the grant is %s", ErrConflict, g.Status)
		}
		g.Status = GrantRevoked
		b, err := putGrantTx(tx, g, sk)
		if err != nil {
			return err
		}
		h, err := s.commitGrantRow(tx, g, b)
		if err != nil {
			return err
		}
		committed, revoked = h, g
		return nil
	})
	if err != nil {
		return err
	}
	s.afterHeads(committed)
	_ = s.auditEntry(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "secrets_grant_revoke", Target: id,
		Detail: "revoked: " + reason})
	s.sendSecretHook(revoked.Agent, "secret.revoked", hookPayload{Grant: revoked.ID, Item: revoked.Item, Reason: reason})
	return nil
}

// renewalMode is grant.renewal for a level: auto levels renew on their own, the rest ask again.
func (s *Service) renewalMode(level string) string {
	if s.d.Settings != nil {
		if r, _, err := s.d.Settings.Value("grant.renewal", settings.Selector{}); err == nil {
			var m map[string]string
			if json.Unmarshal(r.Value, &m) == nil {
				if v, ok := m[level]; ok {
					return v
				}
			}
		}
	}
	switch level {
	case LevelAuto:
		return "auto"
	default:
		return "request"
	}
}

// renewalDuration is what an auto renewal lasts: the level's default duration.
func (s *Service) renewalDuration(level string) int64 {
	d, _ := s.settingLevelsDurationAt("request.default_duration", level, settings.Selector{})
	return d
}

// alertGrace is how long a bound grant outlives its alert (grant.alert_grace).
func (s *Service) alertGrace() time.Duration {
	return s.settingDuration("grant.alert_grace", 15*time.Minute)
}

// ---- the policy around the grants changes

// reassessGrants walks the live grants and re-runs the policy around each: the item's level, the
// entitlement, the actions the item allows, and the owner's access. What tightened ends the grant
// the way grant.on_policy_tightened says (revoked, kept until expiry, or only notified); what
// loosened changes nothing.
func (s *Service) reassessGrants() {
	if s.keys.sealed() {
		return
	}
	onTightened := s.settingString("grant.on_policy_tightened", "revoke")
	onOwnerLost := s.settingString("grant.on_owner_access_lost", "revoke")
	type toTighten struct {
		g    Grant
		mode string
		why  string
	}
	// snapshot first: Item, decidePolicy and ownerMayRead each open their own View, and a
	// nested View under grants() waits on the SQLite read pool
	var live []Grant
	s.grants(func(g Grant) bool {
		if g.Status == GrantActive {
			live = append(live, g)
		}
		return true
	})
	var queue []toTighten
	for _, g := range live {
		item, col, ok := s.Item(g.Item)
		agent, agentOK := s.agentInfoOf(g.Agent)
		var why string
		mode := onTightened
		switch {
		case !ok:
			why = "the item is gone"
		case !agentOK:
			why = "the agent is gone"
		case item.State != StateNormal:
			why = "the item is not in a normal state"
		case s.EffectiveLevel(item) == LevelNever:
			why = "the item is never given to an agent"
		default:
			pol := s.decidePolicy(agent, item, col, g.Action, g.Entitlement)
			switch {
			case !pol.OK:
				why = "the policy no longer covers the agent's reach: " + pol.Reason
			case LevelStrictness[pol.Level] > LevelStrictness[g.Level]:
				why = "the approval needed is now stricter"
			case !containsString(pol.Actions, g.Action):
				why = "the item no longer allows " + g.Action
			case len(pol.Fields) > 0 && (len(g.Fields) == 0 || !fieldsCovered(g.Fields, pol.Fields)):
				why = "the entitlement now names fewer fields"
			}
			if why == "" && !s.ownerMayRead(agent.Owner, col.ID) {
				// the owner lost the collection: its own setting says what happens
				why, mode = "the owner may no longer read the collection", onOwnerLost
			}
		}
		if why != "" {
			queue = append(queue, toTighten{g: g, mode: mode, why: why})
		}
	}
	for _, t := range queue {
		s.tightenGrant(t.g, t.mode, t.why)
	}
}

// tightenGrant ends or flags a grant the policy tightened around, the way the setting says.
func (s *Service) tightenGrant(g Grant, mode, why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refuseIfRotating(); err != nil {
		return
	}
	sk, err := s.sk()
	if err != nil {
		return
	}
	var revoked Grant
	var committed signedhead.Head
	_ = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		v := tx.Get(bucketGrants, []byte(g.ID))
		var cur Grant
		if v == nil {
			return nil // gone already: nothing to tighten
		}
		if err := json.Unmarshal(v, &cur); err != nil {
			return fmt.Errorf("the grant's row is unreadable: %w", err)
		}
		if cur.Status != GrantActive {
			return nil // ended already: nothing to tighten
		}
		switch mode {
		case "keep_until_expiry", "notify":
			// the grant stands; the change is said, and the audit carries why
		default: // revoke
			cur.Status = GrantRevoked
			b, err := putGrantTx(tx, cur, sk)
			if err != nil {
				return err
			}
			h, err := s.commitGrantRow(tx, cur, b)
			if err != nil {
				return err
			}
			committed, revoked = h, cur
		}
		return nil
	})
	s.afterHeads(committed)
	// the record and the echo go out after the transaction: the audit log shares the one write
	// connection, so it is never written inside one
	if revoked.ID != "" {
		s.sendSecretHook(revoked.Agent, "secret.revoked", hookPayload{Grant: revoked.ID, Item: revoked.Item, Reason: why})
	}
	_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_grant_tightened",
		Target: g.ID, Detail: why + "; the setting says " + mode})
	if s.d.Bot != nil {
		_ = s.d.Bot.Send(s.ctx, s.t("The policy around a grant tightened: %s", why))
	}
}

// notifyItemChanged tells the agents holding a live grant over an item that its value changed, so
// they take it again (grant.on_item_changed; the new value needs no new ask — the grant is over
// the item, not a version).
func (s *Service) notifyItemChanged(item Item, oldVersion int64) {
	if s.settingString("grant.on_item_changed", "notify_holders") == "none" {
		return
	}
	notified := map[string]bool{}
	s.grants(func(g Grant) bool {
		if g.Item == item.ID && g.Status == GrantActive && !notified[g.Agent] {
			notified[g.Agent] = true
			s.sendSecretHook(g.Agent, "secret.changed", hookPayload{Item: item.ID, Grant: g.ID,
				Version: item.Version, Reason: fmt.Sprintf("the item changed; version %d is current", item.Version)})
		}
		return true
	})
	if len(notified) > 0 && s.d.Bot != nil {
		_ = s.d.Bot.Send(s.ctx, s.t("The secret \"%s\" changed; %d agent(s) were told to take it again.",
			item.Title, len(notified)))
	}
	_ = oldVersion
}

// ---- the clock's work

// Sweep is what time does to the grants and the waiting requests: grants past their expiry end,
// the ones close to it are noticed (secret.expiring, three days ahead, grants under a day left
// alone), waiting requests past their pending TTL meet request.on_pending_expiry, grants bound to
// a resolved alert end with their grace, and requests queued while the vault was sealed are
// resolved against the policy again. The wiring runs it every minute; it answers how many things
// it changed, for the log.
func fieldsCovered(have, allowed []string) bool {
	for _, f := range have {
		if !containsString(allowed, f) {
			return false
		}
	}
	return true
}

func (s *Service) Sweep() int {
	if s.keys.sealed() {
		return 0
	}
	s.reassessGrants()
	changed := 0
	now := s.now()
	notice := s.settingDuration("grant.expiry_notice", 72*time.Hour)

	// grants: expiry, notices, the alert grace
	var endIDs []string
	var noticeIDs []string
	s.grants(func(g Grant) bool {
		if g.Status != GrantActive {
			return true
		}
		if !g.ExpiresAt.IsZero() && now.After(g.ExpiresAt) {
			endIDs = append(endIDs, g.ID)
			return true
		}
		if !g.ExpiresAt.IsZero() && g.NoticedAt.IsZero() && notice > 0 {
			total := g.ExpiresAt.Sub(g.Created)
			if total >= 24*time.Hour && now.After(g.ExpiresAt.Add(-notice)) {
				noticeIDs = append(noticeIDs, g.ID)
			}
		}
		if g.Alert != "" && s.d.AlertState != nil {
			if firing, resolvedAt, ok := s.d.AlertState(g.Alert); ok && !firing {
				grace := s.alertGrace()
				if resolvedAt.IsZero() || now.After(resolvedAt.Add(grace)) {
					endIDs = append(endIDs, g.ID)
				}
			}
		}
		return true
	})
	if len(endIDs)+len(noticeIDs) > 0 {
		s.mu.Lock()
		sk, err := s.sk()
		if err == nil && s.refuseIfRotating() == nil {
			var ended, noticed []Grant
			var committed signedhead.Head
			_ = s.d.Store.Storage().Update(func(tx kv.Tx) error {
				puts := map[string][]byte{}
				var dels []string
				for _, id := range endIDs {
					v := tx.Get(bucketGrants, []byte(id))
					var g Grant
					if v == nil || json.Unmarshal(v, &g) != nil || g.Status != GrantActive {
						continue
					}
					g.Status = GrantExpired
					if _, err := putGrantTx(tx, g, sk); err != nil {
						return err
					}
					dels = append(dels, g.ID)
					ended = append(ended, g)
				}
				for _, id := range noticeIDs {
					v := tx.Get(bucketGrants, []byte(id))
					var g Grant
					if v == nil || json.Unmarshal(v, &g) != nil || g.Status != GrantActive {
						continue
					}
					g.NoticedAt = now
					b, err := putGrantTx(tx, g, sk)
					if err != nil {
						return err
					}
					puts[g.ID] = b
					noticed = append(noticed, g)
				}
				if len(ended)+len(noticed) == 0 {
					return nil
				}
				h, err := s.commitGrants(tx, puts, dels)
				committed = h
				return err
			})
			s.afterHeads(committed)
			for _, g := range ended {
				_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_grant_end",
					Target: g.ID, Detail: "the grant ended"})
				changed++
			}
			for _, g := range noticed {
				s.sendSecretHook(g.Agent, "secret.expiring", hookPayload{Grant: g.ID, Item: g.Item,
					ExpiresAt: g.ExpiresAt, Reason: "the grant is close to its end"})
				changed++
			}
		}
		s.mu.Unlock()
	}

	// waiting requests: the pending TTL, and the ones queued while sealed
	onExpiry := s.settingString("request.on_pending_expiry", "expire")
	var resolved, expired []Request
	for _, r := range s.Requests(ReqPending) {
		if r.Level == "" && !s.keys.sealed() {
			resolved = append(resolved, r) // queued while sealed; the policy can see again
			continue
		}
		if !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt) {
			expired = append(expired, r)
		}
	}
	for _, r := range resolved {
		out, err := s.RequestSecret(r.Agent, r.Via, RequestIn{Items: refsOf(r.Items), Action: r.Action,
			Reason: r.Reason, Alert: r.Alert, Context: r.Context, Entitlement: r.Entitlement})
		if err == nil {
			r.Status = ReqWithdrawn // the queued ask was replaced by the fresh one
			r.Decisions = append(r.Decisions, Decision{By: "beacon", Kind: "system", Via: "system", At: now,
				Note: fmt.Sprintf("resolved against the policy after the unseal: %d granted, %d waiting", len(out.Granted), boolInt(out.Pending != nil))})
			_ = s.putRequest(r)
			changed++
		}
	}
	for _, r := range expired {
		switch onExpiry {
		case "approve":
			if _, _, err := s.Decide(DecideActor{Name: "beacon", Kind: "system", Via: "system"}, r.ID, true,
				"nobody decided in time; the setting says approve", nil, nil); err == nil {
				changed++
				continue
			}
			fallthrough // a decision that cannot be made ends the request all the same
		case "deny":
			r.Status = ReqDenied
			r.Decisions = append(r.Decisions, Decision{By: "beacon", Kind: "system", Via: "system", At: now,
				Note: "nobody decided in time; the setting says deny"})
		default: // expire
			r.Status = ReqExpired
			r.Decisions = append(r.Decisions, Decision{By: "beacon", Kind: "system", Via: "system", At: now,
				Note: "nobody decided in time"})
		}
		_ = s.putRequest(r)
		s.countRequest("denied", "system")
		s.sendSecretHook(r.Agent, "secret.denied", hookPayload{Request: r.ID, Item: refsOf(r.Items)[0],
			Reason: "nobody decided in time"})
		changed++
	}
	return changed
}

// refsOf lists a request's items as references again.
func refsOf(items []ReqItem) []string {
	var out []string
	for _, it := range items {
		ref := it.Ref
		if ref == "" {
			ref = it.Title
		}
		out = append(out, ref)
	}
	return out
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// settingString reads a string setting, with a default.
func (s *Service) settingString(key, def string) string {
	if s.d.Settings != nil {
		if r, err := s.d.Settings.System(key); err == nil {
			var str string
			if json.Unmarshal(r.Value, &str) == nil {
				return str
			}
		}
	}
	return def
}
