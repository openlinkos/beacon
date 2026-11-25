package store

import (
	"bytes"
	"encoding/json"
	"slices"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

var bucketOutcomes = []byte("incident_outcomes")

// IncidentOutcome is the bounded internal record of one resolved alert episode.
// Its ID starts with the UTC resolution timestamp so the bucket is ordered newest-first.
type IncidentOutcome struct {
	ID                   string                       `json:"id"`
	Group                string                       `json:"group"`
	Title                string                       `json:"title"`
	Severity             string                       `json:"severity"`
	Services             []string                     `json:"services"`
	Started              time.Time                    `json:"started_at"`
	Resolved             time.Time                    `json:"resolved_at"`
	AuditUntil           time.Time                    `json:"audit_until,omitzero"`
	Participation        []Participation              `json:"participation"`
	Verification         *Verification                `json:"verification,omitempty"`
	PassingCheck         *Verification                `json:"passing_check,omitempty"`
	Sessions             []string                     `json:"related_sessions,omitempty"`
	AgentDeliveries      []OutcomeAgentDelivery       `json:"agent_deliveries,omitempty"`
	HumanAcknowledgement *OutcomeHumanAcknowledgement `json:"human_acknowledgement,omitempty"`

	deliveryReporters map[string]string
}

// OutcomeHumanAcknowledgement retains the latest explicit acknowledgement by an identified person.
// The person's later notes or checks do not replace this evidence.
type OutcomeHumanAcknowledgement struct {
	By  string    `json:"by"`
	Via string    `json:"via"`
	At  time.Time `json:"at"`
}

// OutcomeAgentDelivery preserves the observable handoff to one configured hook. Transport diagnostics stay on the
// live alert, and reporter bindings stay in the private delivery index.
type OutcomeAgentDelivery struct {
	ID        string    `json:"id,omitempty"`
	Hook      string    `json:"hook"`
	Event     string    `json:"event"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	QueuedAt  time.Time `json:"queued_at,omitzero"`
	DoneAt    time.Time `json:"done_at,omitzero"`
	StartedAt time.Time `json:"started_at,omitzero"`
	Run       *AgentRun `json:"run,omitempty"`
}

// Participation is the latest alert action recorded for one kind of actor, with no note text.
type Participation struct {
	Kind   string    `json:"kind"` // person, agent or unattributed
	By     string    `json:"by"`
	Via    string    `json:"via"`
	Action string    `json:"action"`
	At     time.Time `json:"at"`
}

// NamedAcknowledgement reports whether this is an explicit acknowledgement by a known person.
func (p Participation) NamedAcknowledgement() bool {
	if p.Kind != "person" || p.Action != "ack" || p.By == "" || p.At.IsZero() {
		return false
	}
	switch p.Via {
	case "console", "api", "mcp", "feishu":
		return true
	}
	return false
}

// AgentReportRecorded reports evidence of a programme report for the alert trigger.
func (o IncidentOutcome) AgentReportRecorded() bool {
	return slices.ContainsFunc(o.AgentDeliveries, func(d OutcomeAgentDelivery) bool {
		return d.Event == "firing" && d.Run != nil
	})
}

func (o IncidentOutcome) HasHumanAcknowledgement() bool { return o.HumanAcknowledgement != nil }
func (o IncidentOutcome) HasPassingCheck() bool         { return o.PassingCheck != nil }

// ValidOutcomeEvidence reports which presence filters may be used in a history query.
func ValidOutcomeEvidence(v string) bool {
	switch v {
	case "", "agent_report", "no_agent_report", "human_ack", "no_human_ack", "passing_check", "no_passing_check":
		return true
	}
	return false
}

func (o IncidentOutcome) matchesEvidence(v string) bool {
	switch v {
	case "":
		return true
	case "agent_report":
		return o.AgentReportRecorded()
	case "no_agent_report":
		return !o.AgentReportRecorded()
	case "human_ack":
		return o.HasHumanAcknowledgement()
	case "no_human_ack":
		return !o.HasHumanAcknowledgement()
	case "passing_check":
		return o.HasPassingCheck()
	case "no_passing_check":
		return !o.HasPassingCheck()
	}
	return false
}

// Verification counts the target-location observations collected by an on-demand check.
type Verification struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"` // when Beacon finished collecting the results
	By        string    `json:"by"`
	Via       string    `json:"via"`
	ActorKind string    `json:"actor_kind,omitempty"` // absent if the record did not identify the caller's kind
	Passed    int       `json:"passed"`
	Failed    int       `json:"failed"`
	Missing   int       `json:"missing"`
}

func putOutcome(tx kv.Tx, outcome IncidentOutcome) error {
	raw, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	if err := tx.Put(bucketOutcomes, []byte(outcome.ID), raw); err != nil {
		return err
	}
	return linkOutcomeDeliveries(tx, outcome)
}

// EnsureIncidentOutcome backfills a missing summary or evidence from a retained group.
func (s *Store) EnsureIncidentOutcome(outcome IncidentOutcome) error {
	return s.db.Update(func(tx kv.Tx) error {
		raw := tx.Get(bucketOutcomes, []byte(outcome.ID))
		if raw == nil {
			return putOutcome(tx, outcome)
		}
		if outcome.PassingCheck == nil && len(outcome.AgentDeliveries) == 0 && !outcome.HasHumanAcknowledgement() {
			return nil
		}
		var saved IncidentOutcome
		if err := json.Unmarshal(raw, &saved); err != nil {
			return err
		}
		changed := false
		if saved.PassingCheck == nil && outcome.PassingCheck != nil {
			saved.PassingCheck, changed = outcome.PassingCheck, true
		}
		if len(saved.AgentDeliveries) == 0 && len(outcome.AgentDeliveries) > 0 {
			saved.AgentDeliveries, changed = outcome.AgentDeliveries, true
		}
		if !saved.HasHumanAcknowledgement() && outcome.HasHumanAcknowledgement() {
			saved.HumanAcknowledgement, changed = outcome.HumanAcknowledgement, true
		}
		saved.deliveryReporters = outcome.deliveryReporters
		if !changed {
			return linkOutcomeDeliveries(tx, saved)
		}
		return putOutcome(tx, saved)
	})
}

// DropResolvedGroup preserves the compact outcome, then removes the rich alert group atomically.
func (s *Store) DropResolvedGroup(g Group, outcome *IncidentOutcome) error {
	return s.db.Update(func(tx kv.Tx) error {
		raw := tx.Get(bucketGroups, []byte(g.ID))
		if raw == nil {
			return nil
		}
		var current Group
		if err := json.Unmarshal(raw, &current); err != nil {
			return err
		}
		if current.Firing() || current.Episode != g.Episode || !current.ResolvedAt.Equal(g.ResolvedAt) || !current.Updated.Equal(g.Updated) {
			return kv.ErrCompareFailed
		}
		if outcome != nil {
			if err := putOutcome(tx, *outcome); err != nil {
				return err
			}
		}
		key := []byte(g.ID)
		if err := tx.Delete(bucketSlackNotices, key); err != nil {
			return err
		}
		return tx.Delete(bucketGroups, key)
	})
}

// IncidentOutcome returns a retained summary under its episode ID.
func (s *Store) IncidentOutcome(id string, since time.Time) (IncidentOutcome, error) {
	var got IncidentOutcome
	err := s.db.View(func(tx kv.Tx) error {
		raw := tx.Get(bucketOutcomes, []byte(id))
		if raw == nil {
			return ErrNotFound
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			return err
		}
		if got.Resolved.Before(since) {
			return ErrNotFound
		}
		return nil
	})
	return got, err
}

// OutcomeFilter pages backwards in resolution time, optionally matching an alert group, severity or evidence.
type OutcomeFilter struct {
	Since    time.Time
	Before   string
	Group    string
	Severity string
	Evidence string
	Limit    int
}

// IncidentOutcomeForEpisode finds a retained summary from an exact group and episode reference.
func (s *Store) IncidentOutcomeForEpisode(group, episode string, since time.Time) (IncidentOutcome, error) {
	suffix, lower := []byte("-"+group+"-"+episode), []byte(outcomeTime(since))
	var found IncidentOutcome
	err := s.db.View(func(tx kv.Tx) error {
		result := ErrNotFound
		tx.Range(bucketOutcomes, kv.Range{Reverse: true}, func(k, v []byte) bool {
			if bytes.Compare(k, lower) < 0 {
				return false
			}
			if !bytes.HasSuffix(k, suffix) {
				return true
			}
			result = json.Unmarshal(v, &found)
			return false
		})
		return result
	})
	return found, err
}

// IncidentOutcomes returns up to Limit+1 rows. The extra row tells callers whether to offer another page.
func (s *Store) IncidentOutcomes(q OutcomeFilter) ([]IncidentOutcome, error) {
	out := make([]IncidentOutcome, 0, q.Limit+1)
	err := s.db.View(func(tx kv.Tx) error {
		var decodeErr error
		tx.Range(bucketOutcomes, kv.Range{Reverse: true, Start: []byte(q.Before)}, func(_, value []byte) bool {
			var item IncidentOutcome
			if decodeErr = json.Unmarshal(value, &item); decodeErr != nil || item.Resolved.Before(q.Since) {
				return false
			}
			if (q.Group == "" || q.Group == item.Group) && (q.Severity == "" || q.Severity == item.Severity) && item.matchesEvidence(q.Evidence) {
				out = append(out, item)
			}
			return len(out) <= q.Limit
		})
		return decodeErr
	})
	return out, err
}

// PruneIncidentOutcomes removes summaries older than the configured resolution-time retention.
func (s *Store) PruneIncidentOutcomes(since time.Time) error {
	return s.db.Update(func(tx kv.Tx) error {
		var keys [][]byte
		prefix := []byte(outcomeTime(since))
		tx.Range(bucketOutcomes, kv.Range{}, func(k, _ []byte) bool {
			if string(k) >= string(prefix) {
				return false
			}
			keys = append(keys, bytesClone(k))
			return true
		})
		for _, k := range keys {
			var outcome IncidentOutcome
			if err := json.Unmarshal(tx.Get(bucketOutcomes, k), &outcome); err != nil {
				return err
			}
			if err := unlinkOutcomeDeliveries(tx, outcome); err != nil {
				return err
			}
			if err := tx.Delete(bucketOutcomes, k); err != nil {
				return err
			}
		}
		return nil
	})
}

func outcomeTime(t time.Time) string { return t.UTC().Format("20060102T150405.000000000") }

// OutcomeID names a resolved episode independently of the live group's ID.
func OutcomeID(resolved time.Time, group, episode string) string {
	return outcomeTime(resolved) + "-" + group + "-" + episode
}
