package backup

// The retention: grandfather–father–son, as restic's forget does it (the plan's §11), over a
// history that is full backups, file incrementals and database-only snapshots. A full or a
// database-only backup is kept when it fits any rule: the newest of each of the last N hours,
// the last N days, the last N weeks and the last N months; the counting is on distinct buckets,
// so an hour with three backups keeps one of them.
//
// File incrementals (kind incr) are restorable only beside the full they follow, and thinning
// them by GFS would drop the day's recordings in favour of a later database-only snapshot of
// the same kind. Every incremental at or after the oldest kept full is therefore kept. The
// newest full always stays.
//
// A clock behind the newest backup would otherwise skip every dated archive (they look like
// they are in the future) and then delete them. When now is before the newest dated backup,
// Plan keeps everything and reports the skew so the caller does not prune.

import (
	"fmt"
	"sort"
	"time"
)

// Keep is the retention policy: how many of each generation survive.
type Keep struct {
	Hourly, Daily, Weekly, Monthly int
}

// DefaultKeep is the plan's default: 24 hours, 14 days, 8 weeks, 12 months — about a year of
// history at the outer edge.
func DefaultKeep() Keep { return Keep{Hourly: 24, Daily: 14, Weekly: 8, Monthly: 12} }

// Backup is one backup at a destination, as retention sees it.
type Backup struct {
	Name string
	Time time.Time
	Kind string // full, incr or db; empty reads as full (a name that could not be parsed)
	Host string
}

// Plan marks which backups survive. A backup whose time cannot be read is always kept:
// retention never deletes what it cannot place. clockSkew is true when now is before the
// newest dated backup: the caller must not prune, and every name is kept.
func Plan(backups []Backup, keep Keep, now time.Time) (kept map[string]bool, clockSkew bool) {
	out := map[string]bool{}
	if keep == (Keep{}) {
		keep = DefaultKeep()
	}
	newest := time.Time{}
	for _, b := range backups {
		if b.Time.IsZero() {
			continue
		}
		if newest.IsZero() || b.Time.After(newest) {
			newest = b.Time
		}
	}
	if !newest.IsZero() && now.Before(newest) {
		for _, b := range backups {
			out[b.Name] = true
		}
		return out, true
	}
	var fulls, incrs, dbs []Backup
	for _, b := range backups {
		if b.Time.IsZero() {
			out[b.Name] = true
			continue
		}
		switch b.Kind {
		case KindIncr:
			incrs = append(incrs, b)
		case KindDB:
			dbs = append(dbs, b)
		default:
			fulls = append(fulls, b)
		}
	}
	keepG := func(list []Backup) map[string]bool { return planGFS(list, keep, now) }
	keptF := keepG(fulls)
	// the newest full stays whatever the rules say: without one, nothing incremental is
	// restorable and the next full is a schedule away
	sort.Slice(fulls, func(i, j int) bool { return fulls[i].Time.After(fulls[j].Time) })
	if len(fulls) > 0 {
		keptF[fulls[0].Name] = true
	}
	for n := range keptF {
		out[n] = true
	}
	// the oldest full that stays bounds the chain: nothing incremental before it is restorable
	oldestKept := time.Time{}
	for _, b := range fulls {
		if keptF[b.Name] && (oldestKept.IsZero() || b.Time.Before(oldestKept)) {
			oldestKept = b.Time
		}
	}
	// every file incremental at or after that full stays: GFS over mixed kinds used to keep
	// the newest of the day, which is the hourly database-only snapshot, and drop the
	// recordings
	for _, b := range incrs {
		if oldestKept.IsZero() || !b.Time.Before(oldestKept) {
			out[b.Name] = true
		}
	}
	for n := range keepG(dbs) {
		out[n] = true
	}
	return out, false
}

// planGFS is the generational rule over one list, newest first.
func planGFS(list []Backup, keep Keep, now time.Time) map[string]bool {
	out := map[string]bool{}
	sorted := make([]Backup, len(list))
	copy(sorted, list)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Time.After(sorted[j].Time) })
	buckets := []struct {
		n   int
		key func(time.Time) string
	}{
		{keep.Hourly, func(t time.Time) string { return t.UTC().Format("2006-01-02T15") }},
		{keep.Daily, func(t time.Time) string { return t.UTC().Format("2006-01-02") }},
		{keep.Weekly, func(t time.Time) string { _, w := t.UTC().ISOWeek(); return fmt.Sprintf("%d-W%02d", t.UTC().Year(), w) }},
		{keep.Monthly, func(t time.Time) string { return t.UTC().Format("2006-01") }},
	}
	for _, b := range buckets {
		if b.n <= 0 {
			continue
		}
		seen := map[string]bool{}
		for _, bk := range sorted {
			if bk.Time.After(now) {
				// a clock behind this backup: keep it (Plan has already short-circuited
				// when the newest is in the future; this is the remainder)
				out[bk.Name] = true
				continue
			}
			k := b.key(bk.Time)
			if seen[k] {
				continue
			}
			seen[k] = true
			if len(seen) > b.n {
				break
			}
			out[bk.Name] = true
		}
	}
	return out
}
