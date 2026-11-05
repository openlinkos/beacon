package backup

// The retention: grandfather–father–son, as restic's forget does it (the plan's §11), over a
// history that is full backups, file incrementals and database-only snapshots. A full or a
// database-only backup is kept when it fits any rule: the newest of each of the last N hours,
// the last N days, the last N weeks and the last N months; the counting is on distinct buckets,
// so an hour with three backups keeps one of them.
//
// File incrementals (kind incr) are restorable only beside the full they follow. For each kept
// full, the incrementals until the next full (kept or not) stay; incrementals after a discarded
// full drop; incrementals after the newest full stay. The newest full always stays.
//
// Archives dated in the future are all kept and clockSkew is set, but they do not freeze the
// rest: dated archives still follow the rules. When every dated archive is in the future, all
// are kept. A backup whose time cannot be read is always kept: retention never deletes what it
// cannot place.

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
// retention never deletes what it cannot place. clockSkew is true when any dated backup is
// after now: those future archives are kept, and dated ones still follow the rules.
func Plan(backups []Backup, keep Keep, now time.Time) (kept map[string]bool, clockSkew bool) {
	out := map[string]bool{}
	if keep == (Keep{}) {
		keep = DefaultKeep()
	}
	var future, dated []Backup
	for _, b := range backups {
		if b.Time.IsZero() {
			out[b.Name] = true
			continue
		}
		if b.Time.After(now) {
			future = append(future, b)
			continue
		}
		dated = append(dated, b)
	}
	for _, b := range future {
		out[b.Name] = true
	}
	clockSkew = len(future) > 0
	if len(dated) == 0 {
		return out, clockSkew
	}

	var fulls, incrs, dbs []Backup
	for _, b := range dated {
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
	keepIncrementals(out, fulls, incrs, keptF)
	for n := range keepG(dbs) {
		out[n] = true
	}
	return out, clockSkew
}

// keepIncrementals keeps, for each kept full, the incrementals until the next full of any kind,
// and every incremental after the newest full. Incrementals after a discarded full drop.
func keepIncrementals(out map[string]bool, fulls, incrs []Backup, keptF map[string]bool) {
	if len(fulls) == 0 {
		for _, b := range incrs {
			out[b.Name] = true
		}
		return
	}
	oldestFirst := make([]Backup, len(fulls))
	copy(oldestFirst, fulls)
	sort.Slice(oldestFirst, func(i, j int) bool { return oldestFirst[i].Time.Before(oldestFirst[j].Time) })
	for i, f := range oldestFirst {
		if !keptF[f.Name] {
			continue
		}
		var until time.Time
		if i+1 < len(oldestFirst) {
			until = oldestFirst[i+1].Time
		}
		for _, inc := range incrs {
			if inc.Time.Before(f.Time) {
				continue
			}
			if !until.IsZero() && !inc.Time.Before(until) {
				continue
			}
			out[inc.Name] = true
		}
	}
	newest := oldestFirst[len(oldestFirst)-1]
	for _, inc := range incrs {
		if inc.Time.After(newest.Time) {
			out[inc.Name] = true
		}
	}
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
