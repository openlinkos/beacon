package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

func TestArchivedDeliveryBindingIsPrivateAndPrunedWithItsOutcome(t *testing.T) {
	s, _ := open(t)
	const group, delivery = "123456789abc", "1234abcd"
	resolved := time.Now().UTC().Add(-time.Hour)
	o := IncidentOutcome{ID: OutcomeID(resolved, group, "1234567890abcdef"), Group: group, Resolved: resolved,
		AgentDeliveries: []OutcomeAgentDelivery{{ID: delivery, Hook: "fixture", Status: "pending"}}}
	if err := s.EnsureIncidentOutcome(o); err != nil {
		t.Fatal(err)
	}
	key := outcomeDeliveryKey(group, delivery)
	inspectBinding := func() *outcomeDeliveryRef {
		t.Helper()
		var got *outcomeDeliveryRef
		if err := s.db.View(func(tx kv.Tx) error {
			raw := tx.Get(bucketOutcomeDeliveries, key)
			if raw == nil {
				return nil
			}
			return json.Unmarshal(raw, &got)
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := inspectBinding(); got != nil {
		t.Fatal("an old public projection created an unknown reporter binding")
	}
	o.BindDeliveryReporters([]AgentCall{{Delivery: delivery, Reporter: "token:original"}})
	if err := s.EnsureIncidentOutcome(o); err != nil {
		t.Fatal(err)
	}
	o.BindDeliveryReporters([]AgentCall{{Delivery: delivery, Reporter: "token:replacement"}})
	if err := s.EnsureIncidentOutcome(o); err != nil {
		t.Fatal(err)
	}
	if got := inspectBinding(); got == nil || got.Outcome != o.ID || got.Reporter != "token:original" {
		t.Fatalf("the original authority did not survive subsequent writes: %+v", got)
	}
	read, err := s.IncidentOutcome(o.ID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := json.Marshal(read); err != nil || strings.Contains(string(raw), "token:original") {
		t.Fatal("the normal outcome response exposed its private reporter binding")
	}
	if err := s.PruneIncidentOutcomes(resolved.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := inspectBinding(); got != nil {
		t.Fatal("retention removed the outcome but left its private reporter binding")
	}
	if err := s.UpdateOutcomeDelivery(group, delivery, time.Time{}, func(IncidentOutcome, *OutcomeAgentDelivery, string) (bool, *AuditEntry, error) {
		t.Fatal("a pruned outcome was still reportable")
		return false, nil, nil
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pruned delivery: %v", err)
	}
}
