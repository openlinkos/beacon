package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

// IncidentEpisodeFilter selects episodes by the start time reported for the alert group.
type IncidentEpisodeFilter struct {
	Since, Until, RetainedFrom      time.Time
	Before, Group, Severity, Origin string
	Limit                           int
}

// IncidentEpisodeRecord reads either the current group or the retained summary of one episode.
type IncidentEpisodeRecord struct {
	ID, Group, Episode string
	Current            *Group
	Archived           *IncidentOutcome
}

const EpisodeCursorPrefix = "started-"

func incidentEpisodeKey(start time.Time, group, episode string) string {
	return EpisodeCursorPrefix + OutcomeID(start, group, episode)
}

func episodeCode(ep string) string {
	if ep == "" {
		return "legacy"
	}
	return ep
}

// IncidentEpisodeRecords reads open and retained recovered episodes from one database snapshot.
// A page keeps only the best Limit+1 rows in memory. The stable start key survives rule recovery.
func (s *Store) IncidentEpisodeRecords(ctx context.Context, q IncidentEpisodeFilter) ([]IncidentEpisodeRecord, error) {
	out := make([]IncidentEpisodeRecord, 0, q.Limit+1)
	include := func(start time.Time, group, opening, severity, origin string) bool {
		if start.IsZero() || start.Before(q.Since) || !start.Before(q.Until) || (q.Group != "" && group != q.Group) {
			return false
		}
		if opening != "" {
			severity = opening
		}
		return (q.Severity == "" || q.Severity == severity) && (IncidentOutcome{Origin: origin}).matchesOrigin(q.Origin)
	}
	insert := func(row IncidentEpisodeRecord) {
		if q.Before != "" && row.ID >= q.Before {
			return
		}
		i, found := slices.BinarySearchFunc(out, row, func(a, b IncidentEpisodeRecord) int {
			return strings.Compare(b.ID, a.ID)
		})
		if found || i > q.Limit {
			return
		}
		if len(out) > q.Limit {
			out = out[:q.Limit]
		}
		out = slices.Insert(out, i, row)
	}

	err := s.db.View(func(tx kv.Tx) error {
		var readErr error
		// Recovery cannot precede a valid start time. Use the retained archive's existing time key to
		// skip earlier rows without loading them for the daily accounting view.
		lower := []byte(outcomeTime(q.Since))
		if q.RetainedFrom.After(q.Since) {
			lower = []byte(outcomeTime(q.RetainedFrom))
		}
		tx.Range(bucketOutcomes, kv.Range{Start: lower}, func(_ []byte, value []byte) bool {
			if readErr = ctx.Err(); readErr != nil {
				return false
			}
			var o IncidentOutcome
			if readErr = json.Unmarshal(value, &o); readErr != nil {
				return false
			}
			if !o.Resolved.Before(o.Started) && include(o.Started, o.Group, o.OpeningSeverity, o.Severity, o.Origin) {
				_, episode, _ := strings.Cut(strings.TrimPrefix(o.ID, outcomeTime(o.Resolved)+"-"), "-")
				row := IncidentEpisodeRecord{Group: o.Group, Episode: episodeCode(episode), Archived: &o}
				row.ID = incidentEpisodeKey(o.Started, row.Group, row.Episode)
				insert(row)
			}
			return true
		})
		if readErr != nil {
			return readErr
		}
		tx.Range(bucketGroups, kv.Range{}, func(_ []byte, value []byte) bool {
			if readErr = ctx.Err(); readErr != nil {
				return false
			}
			var g Group
			if readErr = json.Unmarshal(value, &g); readErr != nil {
				return false
			}
			// A live resolved group also provides a fallback for a legacy database before backfill.
			if (g.Firing() || (g.Status == "resolved" && !g.ResolvedAt.Before(q.RetainedFrom) && !g.ResolvedAt.Before(g.StartsAt))) &&
				include(g.StartsAt, g.ID, g.OpeningSeverity, g.Severity, g.Origin) {
				row := IncidentEpisodeRecord{Group: g.ID, Episode: episodeCode(g.Episode), Current: &g}
				row.ID = incidentEpisodeKey(g.StartsAt, row.Group, row.Episode)
				insert(row)
			}
			return true
		})
		return readErr
	})
	return out, err
}
