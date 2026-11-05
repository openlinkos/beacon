package secrets

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/signedhead"
	"github.com/openlinkos/beacon/internal/store"
)

// The use: an agent taking a value it holds a grant for. Every use carries a reason (required by
// default, two characters at least; when optional, the context is recorded instead), is rate
// limited per agent, and is written to the uses log and the grant's counter in one transaction —
// the design's first invariant, record before give: the transaction commits and only then is the
// value decrypted and returned. A use that cannot be recorded is refused, whatever the reason the
// write failed for. An otp field answers with its current code and the seconds it has left; the
// seed stays with the vault unless otp.seed_to_agents says otherwise.

// ErrThrottled is the rate limit's answer when the setting says throttle.
var ErrThrottled = fmt.Errorf("the agent has passed its uses per minute; the limit is rate.per_agent_per_minute")

// ErrSuspendedAgent is the anomaly consequence's answer while an agent is suspended.
var ErrSuspendedAgent = fmt.Errorf("the agent's grants are suspended (an anomaly rule fired); an operator lifts it")

// ErrSelfApproval is the design's second invariant: the requester cannot review its own request.
var ErrSelfApproval = fmt.Errorf("the requester cannot review its own request")

// ErrNoGrant is the answer when no live grant covers the ask.
var ErrNoGrant = fmt.Errorf("no grant covers this; request_secret asks for one")

// FieldValue is one field's value as get_secret hands it back. An otp field carries the current
// code and the seconds it has left, never the seed (unless the setting gives it).
type FieldValue struct {
	Section   string `json:"section,omitempty"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	OTPExpiry int64  `json:"otp_expiry,omitempty"` // seconds the code has left
}

// UseResult is get_secret's answer: the values, the grant's shape after the use, and the level the
// grant needed.
type UseResult struct {
	Fields    []FieldValue `json:"fields"`
	Notes     string       `json:"notes,omitempty"`
	GrantID   string       `json:"grant_id"`
	UsesLeft  int64        `json:"uses_left"` // -1: unlimited
	ExpiresAt time.Time    `json:"expires_at,omitzero"`
	Level     string       `json:"level"`
}

// GetSecret is the agent's other door. The ref is a beacon:// reference (or collection/title,
// with an optional /field); the reason is required (use.reason); the field narrows to one field.
func (s *Service) GetSecret(agentName, via, ref, field, reason, alert, context, ip, client string) (UseResult, error) {
	var out UseResult
	if s.keys.sealed() {
		s.anomalyFired("sealed_state", "a use came while the vault was sealed")
		return out, ErrSealed
	}
	if err := s.refuseIfRotating(); err != nil {
		return out, err
	}
	if s.agentSuspended(agentName) {
		return out, ErrSuspendedAgent
	}
	agent, ok := s.agentInfoOf(agentName)
	if !ok {
		return out, fmt.Errorf("%w: no agent %q", ErrNotFound, agentName)
	}
	// the reason: required by default, and when optional the context is recorded in its place
	mode, minLength := s.useReasonMode()
	if mode == "required" && int64(len(strings.TrimSpace(reason))) < minLength {
		return out, fmt.Errorf("%w: the reason is at least %d characters", ErrBadInput, minLength)
	}
	if strings.TrimSpace(reason) == "" {
		reason = "context: " + context
	}
	// the rate limit comes first: it counts asks, not successes
	if over := s.rateTick(agentName); over {
		if s.settingString("rate.on_exceed", "throttle") == "alert" {
			_ = s.alarm("an agent passed its rate", agentName+" passed its uses per minute and was allowed on (rate.on_exceed: alert)")
		} else {
			return out, ErrThrottled
		}
	}
	// the reference: a field may ride inside it
	section := ""
	parsed, err := ParseRef(ref)
	if err == nil && parsed.Field != "" {
		ref = RefOfItem(parsed.Collection, parsed.Item)
		field, section = parsed.Field, parsed.Section
	}
	item, col, ok := s.Item(ref)
	if !ok {
		return out, fmt.Errorf("%w: no item %q", ErrNotFound, ref)
	}
	// never is never given to an agent, and looks like it is not there (the fourth invariant)
	if s.EffectiveLevel(item) == LevelNever {
		return out, fmt.Errorf("%w: there is no such item", ErrNotFound)
	}
	if field != "" && len(section) == 0 {
		// a bare field name may still carry a section once the item's fields are known; left empty
		// the grant check below treats it as the field wherever it sits
		_ = section
	}
	grant, ok := s.activeGrant(agentName, item.ID, field, ActRead)
	if !ok {
		return out, ErrNoGrant
	}
	if len(grant.Fields) > 0 && field != "" && !containsString(grant.Fields, field) {
		return out, fmt.Errorf("%w: the grant covers only some fields of this item", ErrNoGrant)
	}
	// the owner cap holds at every use, not only at the ask
	if !s.ownerMayRead(agent.Owner, col.ID) {
		return out, fmt.Errorf("%w: the agent's owner may no longer read this collection", ErrForbidden)
	}
	if err := s.grantPolicyHolds(agent, grant, item, col); err != nil {
		return out, err
	}
	// the transaction: the use record and the grant's counter commit together, or not at all.
	// Only this grant's row is named (or dropped) in the grants head; policy and eligibility stay.
	firstUse := false
	var committed signedhead.Head
	err = s.d.Store.Storage().UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		v := tx.Get(bucketGrants, []byte(grant.ID))
		var cur Grant
		if v == nil || json.Unmarshal(v, &cur) != nil || !s.grantVerifies(cur) || cur.Status != GrantActive {
			return ErrNoGrant
		}
		if !s.grantAccept(tx, cur.ID, v) {
			return ErrNoGrant
		}
		if cur.Uses > 0 && cur.Used >= cur.Uses {
			return ErrNoGrant
		}
		if !cur.ExpiresAt.IsZero() && s.now().After(cur.ExpiresAt) {
			return ErrNoGrant
		}
		sk, err := s.sk()
		if err != nil {
			return err
		}
		cur.Used++
		cur.LastUsed = s.now() // the hygiene sweep reads this: a grant in use is not idle
		cur.UnusedNoticedAt = time.Time{}
		if cur.Uses > 0 && cur.Used >= cur.Uses {
			cur.Status = GrantExhausted
		}
		b, err := putGrantTx(tx, cur, sk)
		if err != nil {
			return err
		}
		h, err := s.commitGrantRow(tx, cur, b)
		if err != nil {
			return err
		}
		committed = h
		// the record itself, in the same transaction: no record, no value
		use := events.Event{Actor: agentName, OnBehalf: agent.Owner, Via: via, Action: "secret.read",
			Object: item.Title, Detail: "read " + item.Title + " in " + col.Name, Reason: reason,
			Context: "collection=" + col.Name, IP: ip, Client: client}
		appendUse := s.d.AppendUse
		if appendUse == nil {
			appendUse = s.d.Store.UsesLog().AppendTx
		}
		if err := appendUse(raw, &use); err != nil {
			return fmt.Errorf("the use could not be recorded, so the value is not given: %w", err)
		}
		// the first time in a collection is an anomaly the design asks for; the mark makes it once
		firstUse = s.markFirstCollection(agentName, col.Name, tx)
		out.UsesLeft = -1
		if cur.Uses > 0 {
			out.UsesLeft = cur.Uses - cur.Used
		}
		out.ExpiresAt, out.GrantID, out.Level = cur.ExpiresAt, cur.ID, cur.Level
		return nil
	})
	if err != nil {
		return UseResult{}, err
	}
	s.afterHeads(committed)
	// committed: the value may be given now
	s.countUse(col.Name, agentName, ActRead)
	if firstUse {
		s.anomalyFiredAgent("first_collection_use", agentName+" took a value in "+col.Name+" for the first time", agentName)
	}
	ver, ok := s.Version(item.ID, item.Version)
	if !ok {
		return out, fmt.Errorf("%w: the item's current version is missing", ErrNotFound)
	}
	d, err := s.decryptDetailsOf(col, ver)
	if err != nil {
		return out, err
	}
	fields := []FieldValue{}
	seedToAgents := s.settingBool("otp.seed_to_agents", false)
	for _, f := range ver.Fields {
		if field != "" && f.Name != field && fieldKey(f.Section, f.Name) != field && fieldKey(f.Section, f.Name) != fieldKey(section, field) {
			continue
		}
		if len(grant.Fields) > 0 && !containsString(grant.Fields, f.Name) && !containsString(grant.Fields, fieldKey(f.Section, f.Name)) {
			continue
		}
		value, okV := d.Values[fieldKey(f.Section, f.Name)]
		if !okV {
			continue
		}
		fv := FieldValue{Section: f.Section, Name: f.Name, Type: f.Type, Value: value}
		if f.Type == FieldOTP {
			code, left, err := TOTPNow(value, s.now())
			if err != nil {
				return out, fmt.Errorf("%w: the field %s has no valid one-time-passcode seed", ErrInvalid, f.Name)
			}
			if !seedToAgents {
				fv.Value = code
				fv.OTPExpiry = int64(left.Seconds())
			} // with the setting on, the seed itself goes out (the deployment chose that)
		}
		fields = append(fields, fv)
	}
	out.Fields = fields
	if len(grant.Fields) == 0 || containsString(grant.Fields, "notes") {
		out.Notes = d.Notes
	}
	_ = alert // the grant's alert binding is set at the ask; a use naming one only reaches the record
	s.anomalyUsageSpike(agentName)
	return out, nil
}

// decryptDetailsOf opens a version's details outside a transaction.
func (s *Service) decryptDetailsOf(col Collection, ver ItemVersion) (*Details, error) {
	var out *Details
	err := s.d.Store.Storage().View(func(tx kv.Tx) error {
		d, err := s.decryptDetailsTx(tx, col, ver, ver.Item, ver.Version)
		out = d
		return err
	})
	return out, err
}

// useReasonMode reads use.reason: {mode, min_length}.
func (s *Service) useReasonMode() (string, int64) {
	if s.d.Settings != nil {
		if r, err := s.d.Settings.System("use.reason"); err == nil {
			var m struct {
				Mode      string `json:"mode"`
				MinLength int64  `json:"min_length"`
			}
			if json.Unmarshal(r.Value, &m) == nil {
				if m.MinLength <= 0 {
					m.MinLength = 2
				}
				return m.Mode, m.MinLength
			}
		}
	}
	return "required", 2
}

// settingBool reads a boolean setting with a default.
func (s *Service) settingBool(key string, def bool) bool {
	if s.d.Settings != nil {
		if r, err := s.d.Settings.System(key); err == nil {
			var v bool
			if json.Unmarshal(r.Value, &v) == nil {
				return v
			}
		}
	}
	return def
}

// ---- the rate limit

// rateWindow is one agent's minute: which minute, and how many uses it counted.
type rateWindow struct {
	minute int64
	count  int
}

// rateMu and rate guard the agents' windows.
var (
	rateMu sync.Mutex
	rate   = map[string]*rateWindow{}
)

// rateTick counts one use against the agent's minute and says whether the limit passed. The limit
// is rate.per_agent_per_minute (600 by default); the window is the wall-clock minute.
func (s *Service) rateTick(agent string) bool {
	limit := int(s.settingInt("rate.per_agent_per_minute", 600))
	if limit <= 0 {
		return false
	}
	minute := s.now().Unix() / 60
	rateMu.Lock()
	defer rateMu.Unlock()
	w, ok := rate[agent]
	if !ok || w.minute != minute {
		w = &rateWindow{minute: minute}
		rate[agent] = w
	}
	w.count++
	return w.count > limit
}

// RateReset drops the rate windows (the tests' helper between cases).
func RateReset() {
	rateMu.Lock()
	defer rateMu.Unlock()
	rate = map[string]*rateWindow{}
}

// MetricsReset drops the counters (the tests' helper between cases; the shuffle runs them in any
// order, so a case must not see another's uses).
func MetricsReset() {
	metricsMu.Lock()
	defer metricsMu.Unlock()
	usesTotal, reqsTotal = map[string]uint64{}, map[string]uint64{}
}

// ---- anomalies

// markFirstCollection marks that the agent has used the collection, inside the use's own
// transaction; true when this was the first time.
func (s *Service) markFirstCollection(agent, collection string, tx kv.Tx) bool {
	key := []byte("usedcol\x00" + agent + "\x00" + collection)
	if tx.Get(bucketMeta, key) != nil {
		return false
	}
	_ = tx.Put(bucketMeta, key, []byte("1"))
	return true
}

// anomalyRuleOn says whether one of the five anomaly rules is on (anomaly.rules).
func (s *Service) anomalyRuleOn(name string) bool {
	if s.d.Settings == nil {
		return true // the defaults have every rule on
	}
	r, err := s.d.Settings.System("anomaly.rules")
	if err != nil {
		return true
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(r.Value, &m) != nil {
		return true
	}
	raw, ok := m[name]
	if !ok {
		return false
	}
	var on bool
	return json.Unmarshal(raw, &on) == nil && on
}

// anomalyFired runs one anomaly rule's consequence: a notice by default, or the agent's grants
// suspended (anomaly.on_detect). The notice goes to the audit log and the chat. The suspend
// consequence needs the agent the rule is about; the rules that name no agent (a failed signature,
// a sealed vault) only ever notify.
func (s *Service) anomalyFired(rule, detail string) { s.anomalyFiredAgent(rule, detail, "") }

// anomalyFiredAgent is anomalyFired with the agent the rule is about, when there is one.
func (s *Service) anomalyFiredAgent(rule, detail, agent string) {
	if !s.anomalyRuleOn(rule) {
		return
	}
	_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_anomaly",
		Target: rule, Detail: detail})
	if agent != "" && s.settingString("anomaly.on_detect", "notify") == "suspend_agent" {
		s.suspendAgent(agent, rule)
		return
	}
	_ = s.alarm("a secret anomaly", detail+" ("+rule+")")
}

// anomalyDenial counts a denial and fires denial_spike when the last hour saw too many: more than
// five, and more than five times the previous seven days' hourly mean.
func (s *Service) anomalyDenial(agent string) {
	if !s.anomalyRuleOn("denial_spike") {
		return
	}
	now := s.now()
	var lastHour, before int
	for _, r := range s.Requests("") {
		if r.Agent != agent || r.Status != ReqDenied {
			continue
		}
		var at time.Time
		if len(r.Decisions) > 0 {
			at = r.Decisions[len(r.Decisions)-1].At
		}
		if at.IsZero() {
			at = r.Created
		}
		if now.Sub(at) <= time.Hour {
			lastHour++
		} else if now.Sub(at) <= 7*24*time.Hour {
			before++
		}
	}
	if lastHour >= 5 && float64(lastHour) > 5*(float64(before)/168+0.05) {
		s.anomalyFiredAgent("denial_spike", fmt.Sprintf("%s was denied %d times in the last hour", agent, lastHour), agent)
	}
}

// anomalyUsageSpike fires usage_spike when the agent's last day out-ran its previous seven days:
// more than the rule's multiple (five by default) of the daily mean of the week before. It runs
// on every tenth use, to keep the log's queries off the hot path.
func (s *Service) anomalyUsageSpike(agent string) {
	if !s.anomalyRuleOn("usage_spike") {
		return
	}
	rateMu.Lock()
	w := rate[agent]
	rateMu.Unlock()
	if w == nil || w.count == 0 || w.count%10 != 0 {
		return
	}
	times := float64(5.0)
	if s.d.Settings != nil {
		if r, err := s.d.Settings.System("anomaly.rules"); err == nil {
			var m struct {
				UsageSpike struct {
					TimesMean7d float64 `json:"times_mean_7d"`
				} `json:"usage_spike"`
			}
			if json.Unmarshal(r.Value, &m) == nil && m.UsageSpike.TimesMean7d > 0 {
				times = m.UsageSpike.TimesMean7d
			}
		}
	}
	log := s.d.Store.UsesLog()
	if log == nil {
		return
	}
	now := s.now()
	_, day, errD := log.Query(events.Filter{Actor: agent, From: now.Add(-24 * time.Hour), Limit: 1})
	_, week, errW := log.Query(events.Filter{Actor: agent, From: now.Add(-8 * 24 * time.Hour), To: now.Add(-24 * time.Hour), Limit: 1})
	if errD == nil && errW == nil && day > 10 && float64(day) > times*(float64(week)/7+1) {
		s.anomalyFiredAgent("usage_spike", fmt.Sprintf("%s used a secret %d times in the last day", agent, day), agent)
	}
}

// suspendAgent stops every live grant the agent holds, and tells its hook.
func (s *Service) suspendAgent(agent, rule string) {
	if s.keys.sealed() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refuseIfRotating(); err != nil {
		return
	}
	sk, err := s.sk()
	if err != nil {
		return
	}
	n := 0
	var committed signedhead.Head
	_ = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		res := s.loadGrants(tx)
		var dels []string
		if res.Status == signedhead.StatusOK || res.Status == signedhead.StatusCatchUp {
			for id := range res.Head.Entries {
				if isHeadKey([]byte(id)) {
					continue
				}
				v := tx.Get(bucketGrants, []byte(id))
				var g Grant
				if json.Unmarshal(v, &g) != nil || g.Agent != agent || g.Status != GrantActive {
					continue
				}
				g.Status = GrantRevoked
				g.Reason = "suspended: " + rule
				if _, err := putGrantTx(tx, g, sk); err != nil {
					return err
				}
				dels = append(dels, g.ID)
				n++
			}
		}
		if tx.Get(bucketMeta, []byte("suspended\x00"+agent)) == nil {
			_ = tx.Put(bucketMeta, []byte("suspended\x00"+agent), []byte(rule))
		}
		if n > 0 {
			h, err := s.commitGrants(tx, nil, dels)
			committed = h
			return err
		}
		return nil
	})
	s.afterHeads(committed)
	if n > 0 {
		s.sendSecretHook(agent, "secret.revoked", hookPayload{Reason: "suspended by the anomaly rule " + rule})
	}
	_ = s.alarm("an agent's grants were suspended", agent+" ("+rule+")")
}

// agentSuspended says whether the anomaly consequence suspended the agent.
func (s *Service) agentSuspended(agent string) bool {
	ok := false
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		ok = tx.Get(bucketMeta, []byte("suspended\x00"+agent)) != nil
		return nil
	})
	return ok
}

// SuspendedAgents lists the suspended agents and the rule that suspended them, for the console.
func (s *Service) SuspendedAgents() map[string]string {
	out := map[string]string{}
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketMeta, kv.Range{Prefix: []byte("suspended\x00")}, func(k, v []byte) bool {
			parts := strings.SplitN(string(k), "\x00", 2)
			if len(parts) == 2 {
				out[parts[1]] = string(v)
			}
			return true
		})
		return nil
	})
	return out
}

// UnsuspendAgent lifts a suspension by an operator's hand.
func (s *Service) UnsuspendAgent(who Actor, agent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.d.Store.Storage().Update(func(tx kv.Tx) error {
		return tx.Delete(bucketMeta, []byte("suspended\x00"+agent))
	})
	if err != nil {
		return err
	}
	_ = s.auditEntry(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "secrets_agent_unsuspend",
		Target: agent, Detail: "the suspension was lifted"})
	return nil
}

// ---- the metrics (beacon_secret_uses_total, beacon_secret_requests_total)

var (
	metricsMu sync.Mutex
	usesTotal = map[string]uint64{} // "collection\x00agent\x00action"
	reqsTotal = map[string]uint64{} // "decision\x00kind"
)

func (s *Service) countUse(collection, agent, action string) {
	metricsMu.Lock()
	usesTotal[collection+"\x00"+agent+"\x00"+action]++
	metricsMu.Unlock()
}

func (s *Service) countRequest(decision, kind string) {
	metricsMu.Lock()
	reqsTotal[decision+"\x00"+kind]++
	metricsMu.Unlock()
}

// MetricsSamples is the metrics the wiring registers: the uses and the requests as counters, the
// live grants and the ones close to their end as gauges.
func (s *Service) MetricsSamples() []metrics.Sample {
	var out []metrics.Sample
	metricsMu.Lock()
	for k, v := range usesTotal {
		parts := strings.SplitN(k, "\x00", 3)
		if len(parts) == 3 {
			out = append(out, metrics.Sample{Labels: metrics.Labels("collection", parts[0], "agent", parts[1], "action", parts[2]), Value: float64(v)})
		}
	}
	metricsMu.Unlock()
	return out
}

// RequestSamples is the requests' half of the metrics.
func (s *Service) RequestSamples() []metrics.Sample {
	var out []metrics.Sample
	metricsMu.Lock()
	for k, v := range reqsTotal {
		parts := strings.SplitN(k, "\x00", 2)
		if len(parts) == 2 {
			out = append(out, metrics.Sample{Labels: metrics.Labels("decision", parts[0], "decider_kind", parts[1]), Value: float64(v)})
		}
	}
	metricsMu.Unlock()
	return out
}

// GrantGauge counts the live grants and the ones within their notice window, for the two gauges
// the wiring registers.
func (s *Service) GrantGauge() (active, expiring int) {
	notice := s.settingDuration("grant.expiry_notice", 72*time.Hour)
	now := s.now()
	s.grants(func(g Grant) bool {
		if g.Status == GrantActive {
			active++
			if !g.ExpiresAt.IsZero() && now.After(g.ExpiresAt.Add(-notice)) {
				expiring++
			}
		}
		return true
	})
	return active, expiring
}
