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

	keep, skew := Plan(list, DefaultKeep(), now)
	if skew {
		t.Fatal("a clock on the newest backup's day read as behind")
	}
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
		list = append(list, Backup{Name: ArchiveName("srv", at, KindDB), Time: at, Kind: KindDB})
	}
	keep, _ := Plan(list, DefaultKeep(), now)
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
	keep, _ := Plan(list, DefaultKeep(), now)
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
	keep, _ := Plan(list, Keep{Hourly: 1, Daily: 1, Weekly: 1}, now)
	newestFull := list[6].Name
	if !keep[newestFull] {
		t.Fatal("the newest full was pruned")
	}
	if !keep[list[8].Name] {
		t.Error("the newest incremental was pruned")
	}
	if !keep[list[7].Name] {
		t.Error("an incremental after the newest full was pruned")
	}
	for _, old := range []int{0, 1, 2, 3, 4, 5} {
		if keep[list[old].Name] {
			t.Errorf("a backup whose full is gone was kept: %s", list[old].Name)
		}
	}
}

func TestRetentionDoesNotPruneWhenTheClockIsBehind(t *testing.T) {
	now := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	start := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	var list []Backup
	for i := 0; i < 1440; i++ {
		at := start.Add(-time.Duration(i) * time.Hour)
		kind := KindIncr
		if i%24 == 0 {
			kind = KindFull
		}
		list = append(list, Backup{Name: ArchiveName("srv", at, kind), Time: at, Kind: kind})
	}
	keep, skew := Plan(list, DefaultKeep(), now)
	if !skew {
		t.Fatal("a clock in 2000 did not see the backups as in the future")
	}
	if len(keep) != len(list) {
		t.Fatalf("kept %d of %d; a clock in the past must not wipe history", len(keep), len(list))
	}
}

func TestRetentionKeepsRecordingIncrementalsAmongDBSnapshots(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	fullAt := now.Add(-7 * 24 * time.Hour)
	list := []Backup{{Name: ArchiveName("srv", fullAt, KindFull), Time: fullAt, Kind: KindFull}}
	var incrs []string
	for d := 6; d >= 0; d-- {
		day := now.Add(-time.Duration(d) * 24 * time.Hour)
		recAt := time.Date(day.Year(), day.Month(), day.Day(), 1, 0, 0, 0, time.UTC)
		incr := Backup{Name: ArchiveName("srv", recAt, KindIncr), Time: recAt, Kind: KindIncr}
		list = append(list, incr)
		incrs = append(incrs, incr.Name)
		for h := 2; h < 24; h++ {
			at := time.Date(day.Year(), day.Month(), day.Day(), h, 0, 0, 0, time.UTC)
			if !at.Before(now) {
				continue
			}
			list = append(list, Backup{Name: ArchiveName("srv", at, KindDB), Time: at, Kind: KindDB})
		}
	}
	keep, _ := Plan(list, DefaultKeep(), now)
	for _, n := range incrs {
		if !keep[n] {
			t.Errorf("a recording incremental was pruned: %s", n)
		}
	}
}

func TestRetentionPrunesPastWhenOneBackupIsInTheFuture(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	var list []Backup
	for i := 1; i <= 40; i++ {
		at := now.Add(time.Duration(-i) * time.Hour)
		list = append(list, Backup{Name: ArchiveName("srv", at, KindFull), Time: at, Kind: KindFull})
	}
	future := now.Add(24 * time.Hour)
	fut := Backup{Name: ArchiveName("srv", future, KindFull), Time: future, Kind: KindFull}
	list = append(list, fut)
	keep, skew := Plan(list, DefaultKeep(), now)
	if !skew {
		t.Fatal("a future-dated backup did not set clockSkew")
	}
	if !keep[fut.Name] {
		t.Fatal("the future-dated backup was pruned")
	}
	oldest := list[len(list)-2]
	if keep[oldest.Name] {
		t.Errorf("an old backup that fits no rule was kept because another was in the future: %s", oldest.Name)
	}
}

func TestRetentionKeepsIncrementalsAfterEachKeptFull(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	at := func(when time.Time, kind string) Backup {
		return Backup{Name: ArchiveName("srv", when, kind), Time: when, Kind: kind}
	}
	olderKept := at(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), KindFull)
	incrAfterOlder := at(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC), KindIncr)
	discarded := at(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), KindFull)
	incrAfterDiscarded := at(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), KindIncr)
	newest := at(time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC), KindFull)
	incrAfterNewest := at(time.Date(2026, 11, 5, 11, 30, 0, 0, time.UTC), KindIncr)
	list := []Backup{olderKept, incrAfterOlder, discarded, incrAfterDiscarded, newest, incrAfterNewest}
	keep, skew := Plan(list, Keep{Hourly: 1, Daily: 1, Weekly: 1, Monthly: 3}, now)
	if skew {
		t.Fatal("a clock on the newest backup's day read as behind")
	}
	if !keep[olderKept.Name] || !keep[newest.Name] {
		t.Fatalf("kept fulls: older %v newest %v", keep[olderKept.Name], keep[newest.Name])
	}
	if keep[discarded.Name] {
		t.Fatal("the full between two kept fulls was kept")
	}
	if !keep[incrAfterOlder.Name] {
		t.Error("an incremental after a kept full, before the next full, was pruned")
	}
	if keep[incrAfterDiscarded.Name] {
		t.Error("an incremental after a discarded full was kept")
	}
	if !keep[incrAfterNewest.Name] {
		t.Error("an incremental after the newest full was pruned")
	}
}

func TestParseName(t *testing.T) {
	at := time.Date(2026, 11, 5, 7, 8, 9, 0, time.UTC)
	name := ArchiveName("srv-edge-1", at, KindIncr)
	host, when, kind, ok := ParseName(name)
	if !ok || host != "srv-edge-1" || !when.Equal(at) || kind != KindIncr {
		t.Fatalf("parse %q: %q %v %q %v", name, host, when, kind, ok)
	}
	for _, bad := range []string{"beacon-x.tar.zst.age", "x-full" + Suffix, "beacon-h-20260101-full" + Suffix} {
		if _, _, _, ok := ParseName(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}
