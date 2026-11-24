package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

func TestSlackQueueSurvivesAnOlderGroupWriter(t *testing.T) {
	s, _ := open(t)
	g := Group{ID: "service", Status: "firing", Episode: "current", NotifySlack: true, SlackNotices: []WebhookNotice{
		{Delivery: "queued-message", Episode: "current", Body: json.RawMessage(`{"text":"Firing"}`)},
	}}
	if err := s.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Update(func(tx kv.Tx) error {
		// An older writer shares the group's JSON and has no code for the separate Slack bucket.
		var legacy Group
		if err := json.Unmarshal(tx.Get(bucketGroups, []byte(g.ID)), &legacy); err != nil {
			return err
		}
		legacy.Status = "resolved"
		legacyData, err := json.Marshal(legacy)
		if err != nil {
			return err
		}
		return tx.Put(bucketGroups, []byte(g.ID), legacyData)
	}); err != nil {
		t.Fatal(err)
	}
	stored, ok := s.Group(g.ID)
	if !ok || stored.Firing() || !stored.NotifySlack || len(stored.SlackNotices) != 1 || stored.SlackNotices[0].Delivery != "queued-message" || len(stored.WebhookNotices) != 0 {
		t.Fatalf("legacy write lost or misrouted the Slack message: %+v", stored)
	}
	if groups := s.Groups(); len(groups) != 1 || len(groups[0].SlackNotices) != 1 {
		t.Fatal("pending Slack message is missing from the group list")
	}
	if err := s.DropResolvedGroup(stored, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.db.View(func(tx kv.Tx) error {
		if tx.Get(bucketSlackNotices, []byte(g.ID)) != nil {
			t.Error("archiving the alert did not delete its Slack state")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSlackPauseDoesNotBecomeShorter(t *testing.T) {
	s, _ := open(t)
	until := time.Now().Add(90 * time.Second)
	if err := s.ExtendSlackPause(until); err != nil {
		t.Fatal(err)
	}
	if err := s.ExtendSlackPause(until.Add(-30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := s.SlackPausedUntil(); !got.Equal(until) {
		t.Fatalf("pause ended at %v, wanted %v", got, until)
	}
}
