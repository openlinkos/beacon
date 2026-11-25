package store

import (
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/kv"
)

var bucketOutcomeDeliveries = []byte("incident_outcome_deliveries")

type outcomeDeliveryRef struct {
	Outcome  string `json:"outcome"`
	Reporter string `json:"reporter,omitempty"`
}

// BindDeliveryReporters retains the original authority alongside an outcome when it is stored.
// The binding stays in a private exact-delivery index; the public projection has no reporter key.
func (o *IncidentOutcome) BindDeliveryReporters(calls []AgentCall) {
	if len(calls) == 0 {
		return
	}
	o.deliveryReporters = make(map[string]string, len(calls))
	for _, call := range calls {
		if call.Delivery != "" {
			o.deliveryReporters[call.Delivery] = call.Reporter
		}
	}
}

func outcomeDeliveryKey(group, delivery string) []byte { return []byte(group + "/" + delivery) }

func linkOutcomeDeliveries(tx kv.Tx, o IncidentOutcome) error {
	for _, d := range o.AgentDeliveries {
		reporter, bound := o.deliveryReporters[d.ID]
		if !bound {
			continue
		}
		key := outcomeDeliveryKey(o.Group, d.ID)
		// First binding is authoritative across backfill and subsequent saves.
		if tx.Get(bucketOutcomeDeliveries, key) != nil {
			continue
		}
		raw, err := json.Marshal(outcomeDeliveryRef{Outcome: o.ID, Reporter: reporter})
		if err != nil {
			return err
		}
		if err := tx.Put(bucketOutcomeDeliveries, key, raw); err != nil {
			return err
		}
	}
	return nil
}

func unlinkOutcomeDeliveries(tx kv.Tx, o IncidentOutcome) error {
	for _, d := range o.AgentDeliveries {
		key := outcomeDeliveryKey(o.Group, d.ID)
		raw := tx.Get(bucketOutcomeDeliveries, key)
		if raw == nil {
			continue
		}
		var ref outcomeDeliveryRef
		if err := json.Unmarshal(raw, &ref); err != nil {
			return err
		}
		if ref.Outcome == o.ID {
			if err := tx.Delete(bucketOutcomeDeliveries, key); err != nil {
				return err
			}
		}
	}
	return nil
}

// UpdateOutcomeDelivery edits one exact retained delivery and any resulting audit in a single
// transaction. A callback returning unchanged writes neither the outcome nor an audit entry.
func (s *Store) UpdateOutcomeDelivery(group, delivery string, since time.Time,
	update func(outcome IncidentOutcome, d *OutcomeAgentDelivery, reporter string) (changed bool, audit *AuditEntry, err error),
) error {
	return s.db.UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		data := tx.Get(bucketOutcomeDeliveries, outcomeDeliveryKey(group, delivery))
		if data == nil {
			return ErrNotFound
		}
		var ref outcomeDeliveryRef
		if err := json.Unmarshal(data, &ref); err != nil {
			return err
		}
		data = tx.Get(bucketOutcomes, []byte(ref.Outcome))
		if data == nil {
			return ErrNotFound
		}
		var o IncidentOutcome
		if err := json.Unmarshal(data, &o); err != nil {
			return err
		}
		if o.ID != ref.Outcome || o.Group != group || o.Resolved.Before(since) {
			return ErrNotFound
		}
		i := slices.IndexFunc(o.AgentDeliveries, func(d OutcomeAgentDelivery) bool { return d.ID == delivery })
		if i < 0 {
			return ErrNotFound
		}
		changed, audit, err := update(o, &o.AgentDeliveries[i], ref.Reporter)
		if err != nil || !changed {
			return err
		}
		if err := putOutcome(tx, o); err != nil {
			return err
		}
		if audit == nil {
			return nil
		}
		return s.AppendAuditTx(raw, events.Event{
			Time: audit.Time, Actor: audit.Actor, Via: audit.Via, Action: audit.Action, Object: audit.Target, Detail: audit.Detail,
		})
	})
}
