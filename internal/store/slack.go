package store

import (
	"encoding/json"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

// Separate Slack's queue from group JSON so that older centres can rewrite a group without
// dropping new deliveries or mistaking them for the signed notification webhook's payload.
var bucketSlackNotices = []byte("slack-notices")

const slackPauseKey = "slack-delivery-paused-until"

type slackDelivery struct {
	Episode string          `json:"episode,omitempty"`
	Queue   []WebhookNotice `json:"queue,omitempty"`
}

func (g *Group) readSlackDelivery(raw []byte) {
	var state slackDelivery
	if len(raw) > 0 && json.Unmarshal(raw, &state) == nil {
		g.NotifySlack = state.Episode != "" && state.Episode == g.Episode
		g.SlackNotices, g.slackStored = state.Queue, true
	}
}

func putSlackDelivery(tx kv.Tx, g Group) error {
	key := []byte(g.ID)
	if !g.NotifySlack && len(g.SlackNotices) == 0 {
		if g.slackStored {
			return tx.Delete(bucketSlackNotices, key)
		}
		return nil
	}
	state := slackDelivery{Queue: g.SlackNotices}
	if g.NotifySlack {
		state.Episode = g.Episode
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return tx.Put(bucketSlackNotices, key, raw)
}

// SlackPausedUntil survives a bridge restart and remains shared between HA leaders.
func (s *Store) SlackPausedUntil() time.Time { return s.Time(slackPauseKey) }

// ExtendSlackPause records a provider pause without shortening one recorded by another leader.
func (s *Store) ExtendSlackPause(until time.Time) error {
	key := []byte("t:" + slackPauseKey)
	return s.db.Update(func(tx kv.Tx) error {
		previous, _ := time.Parse(time.RFC3339Nano, string(tx.Get(bucketMeta, key)))
		if !until.After(previous) {
			return nil
		}
		return tx.Put(bucketMeta, key, []byte(until.UTC().Format(time.RFC3339Nano)))
	})
}
