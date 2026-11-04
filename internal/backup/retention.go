package backup

// The retention: grandfather–father–son, as restic's forget does it (the plan's §11), over a
// history that is full backups and incremental ones. A backup is kept when it fits any rule:
// the newest of each of the last N hours, the last N days, the last N weeks and the last N
// months; the counting is on distinct buckets, so an hour with three backups keeps one of them.
//
// The chain matters as much as the count: an incremental is restorable only beside the full it
// follows, so the rules run over the fulls and the incrementals separately, an incremental
// older than the oldest kept full goes even when a rule would keep it, and the newest full
// always stays.

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
	Kind string // full or incr; empty reads as full (a name that could not be parsed)
}

// Plan marks which backups survive. A backup whose time cannot be read is always kept:
// retention never deletes what it cannot place.
func Plan(backups []Backup, keep Keep, now time.Time) map[string]bool {
	out := map[string]bool{}
	if keep == (Keep{}) {
		keep = DefaultKeep()
	}
	var fulls, incrs []Backup
	for _, b := range backups {
		if b.Time.IsZero() {
			out[b.Name] = true
			continue
		}
		if b.Kind == "incr" {
			incrs = append(incrs, b)
		} else {
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
	if len(fulls) == 0 {
		oldestKept = time.Time{} // no fulls at all: the incrementals stand on their own
	}
	for n := range keepG(incrs) {
		for _, b := range incrs {
			if b.Name == n {
				// names carry the second, so an incremental of the same second as its full
				// counts as following it
				if oldestKept.IsZero() || !b.Time.Before(oldestKept) {
					out[n] = true
				}
				break
			}
		}
	}
	return out
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
				continue // a clock ahead of today's is not a generation to count
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
