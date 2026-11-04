package backup

import (
	"testing"
	"time"
)

// The retention: grandfather–father–son over a constructed history. What must hold: the newest
// of each hour/day/week/month bucket survives up to its count; a backup fitting any rule stays;
// what fits none goes; and an unreadable name never costs a deletion.

func TestRetentionKeepsEachGeneration(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	var times []time.Time
	// three backups in the last hour: only the newest of the bucket survives the hourly rule
	times = append(times,
		now.Add(-20*time.Minute), now.Add(-10*time.Minute), now.Add(-5*time.Minute))
	// one a day for the past week, each an hour before noon
	for d := 1; d <= 7; d++ {
		times = append(times, now.Add(time.Duration(-d)*24*time.Hour).Add(-time.Hour))
	}
	// one a week for the past six weeks
	for w := 1; w <= 6; w++ {
		times = append(times, now.Add(time.Duration(-w)*7*24*time.Hour))
	}
	// one a month for the past year
	for m := 1; m <= 12; m++ {
		times = append(times, now.AddDate(0, -m, 0))
	}

	byTime := map[time.Time]string{}
	var list []Backup
	for i, at := range times {
		host := "srv"
		if i%2 == 0 {
			host = "srv-2" // a dashed host name must parse the same
		}
		name := ArchiveName(host, at, "full")
		if _, dup := byTime[at]; dup {
			t.Fatal("the fixture times collide")
		}
		byTime[at] = name
		list = append(list, Backup{Name: name, Time: at})
	}

	keep := Plan(list, DefaultKeep(), now)
	if len(keep) == 0 || len(keep) >= len(list) {
		t.Fatalf("kept %d of %d", len(keep), len(list))
	}
	// the newest of the busy hour is kept; its siblings are not (no other rule saves them)
	if !keep[byTime[times[2]]] {
		t.Error("the newest of the last hour was pruned")
	}
	if keep[byTime[times[0]]] {
		t.Error("an older backup of the same hour survived (no rule keeps it)")
	}
	// each of the past week's days is kept: a distinct daily bucket, within 14
	for d := 1; d <= 7; d++ {
		at := now.Add(time.Duration(-d) * 24 * time.Hour).Add(-time.Hour)
		if !keep[byTime[at]] {
			t.Errorf("the daily backup at %s was pruned", at)
		}
	}
	// the monthly rule keeps twelve distinct months counting the current one (as restic's
	// forget does), so eleven monthly fixtures besides now's own survive, and a twelfth does not
	if !keep[byTime[now.AddDate(0, -11, 0)]] {
		t.Error("the eleventh monthly backup was pruned")
	}
	if keep[byTime[now.AddDate(0, -12, 0)]] {
		t.Error("a backup in the thirteenth distinct month was kept by a 12-monthly rule")
	}
}

func TestRetentionPrunesWhatFitsNoRule(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	// forty hourly backups over two days: hourly keeps the newest of each of the last 24 hours,
	// daily the newest of each day (already among them)
	var list []Backup
	for i := 1; i <= 40; i++ {
		at := now.Add(time.Duration(-i) * time.Hour)
		list = append(list, Backup{Name: ArchiveName("srv", at, "incr"), Time: at})
	}
	keep := Plan(list, DefaultKeep(), now)
	if got := len(keep); got > 26 {
		t.Errorf("kept %d of 40 hourly backups; the rules should hold it near 24", got)
	}
	if !keep[list[0].Name] {
		t.Error("the newest backup was pruned")
	}
	if keep[list[len(list)-1].Name] {
		t.Error("a backup two days old that is not the newest of its day was kept")
	}
}

func TestRetentionNeverDeletesWhatItCannotRead(t *testing.T) {
	now := time.Now().UTC()
	list := []Backup{{Name: "beacon-srv-20260101-000000-full" + Suffix}, {Name: "not-a-backup-at-all"}}
	keep := Plan(list, DefaultKeep(), now)
	for _, b := range list {
		if !keep[b.Name] {
			t.Errorf("an unreadable name was deleted: %s", b.Name)
		}
	}
}

func TestRetentionKeepsTheIncrementalChain(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	at := func(days, hours int, kind string) Backup {
		t := now.Add(time.Duration(-days)*24*time.Hour - time.Duration(hours)*time.Hour)
		return Backup{Name: ArchiveName("srv", t, kind), Time: t, Kind: kind}
	}
	// fulls eight days apart, incrementals in between; hourly keeps a day's worth, daily 14,
	// so every full fits the daily rule — this test's point is the chain, so shrink the rules
	list := []Backup{
		at(9, 0, "full"), at(8, 23, "incr"), at(8, 12, "incr"), // the old full and its incrs
		at(8, 0, "full"), at(7, 23, "incr"), at(7, 12, "incr"), // the newer full and its incrs
		at(1, 0, "full"), at(0, 23, "incr"), at(0, 11, "incr"), // yesterday's full and today's incrs
	}
	// a tight policy: one hourly, one daily, one weekly — the two older fulls do not fit, and
	// their incrementals must go with them, while the newest full stays whatever the rules say
	keep := Plan(list, Keep{Hourly: 1, Daily: 1, Weekly: 1}, now)
	newestFull := list[6].Name
	if !keep[newestFull] {
		t.Fatal("the newest full was pruned")
	}
	if !keep[list[8].Name] {
		t.Error("the newest incremental was pruned")
	}
	if keep[list[7].Name] {
		t.Error("an older incremental of the same day survived a one-hourly policy")
	}
	for _, old := range []int{0, 1, 2, 3, 4, 5} {
		if keep[list[old].Name] {
			t.Errorf("a backup whose full is gone was kept: %s", list[old].Name)
		}
	}
}

func TestParseName(t *testing.T) {
	at := time.Date(2026, 11, 5, 7, 8, 9, 0, time.UTC)
	name := ArchiveName("srv-edge-1", at, "incr")
	host, when, kind, ok := ParseName(name)
	if !ok || host != "srv-edge-1" || !when.Equal(at) || kind != "incr" {
		t.Fatalf("parse %q: %q %v %q %v", name, host, when, kind, ok)
	}
	for _, bad := range []string{"beacon-x.tar.zst.age", "x-full" + Suffix, "beacon-h-20260101-full" + Suffix} {
		if _, _, _, ok := ParseName(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}
