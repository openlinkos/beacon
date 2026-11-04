package status

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/openlinkos/beacon/internal/vm"
)

// What the probes say, minute by minute, about each service: the reference rules' agreement (package
// deploy/rules/probes.yml), evaluated by VictoriaMetrics on the same data. A location fails a service
// once every probe of it there failed for 3 minutes; the service is down when at least two locations
// fail it and they are at least half of those reporting, degraded when at least two fail but fewer than
// half; a minute without any location reporting has no data. So a page never says a service was down
// when the alerts did not, nor the other way round.

// Querier is what the pages need of VictoriaMetrics; *vm.Client has it.
type Querier interface {
	QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]vm.Series, error)
}

// The state of one minute of one service.
const (
	minNone     byte = iota // no location reported
	minOK                   // fewer than two locations failing
	minDegraded             // at least two failing, fewer than half
	minDown                 // at least two failing, and at least half
)

const (
	// Days is how many days a page's history holds, today included.
	Days = 90
	// minutesPerDay is the length of the minute arrays; a day of a daylight-saving change is cut to it.
	minutesPerDay = 24 * 60
	// final is how long after a day's end it is computed for the last time: probes that lost their
	// path queue results on disk and send them late.
	final = 6 * time.Hour
	// refreshToday and refreshRecent are how often today, and a day not yet final, are computed again.
	refreshToday  = time.Minute
	refreshRecent = 10 * time.Minute
	queryTimeout  = 20 * time.Second
)

// agreement is the query for the minutes of the services sel matches: 1 ok, 2 degraded, 3 down.
func agreement(sel string) string {
	f := fmt.Sprintf(`(max by (service, location) (max_over_time(probe_success{job="probe",%s}[3m])) == bool 0)`, sel)
	failing, reporting := "sum by (service) "+f, "count by (service) "+f
	return fmt.Sprintf("1 + (%[1]s >= bool 2) + ((%[1]s >= bool 2) * (%[1]s * 2 >= bool %[2]s))", failing, reporting)
}

// selector matches the series of the services.
func selector(svcs []string) string {
	if len(svcs) == 1 {
		return `service="` + svcs[0] + `"`
	}
	quoted := make([]string, len(svcs))
	for i, s := range svcs {
		quoted[i] = strings.ReplaceAll(regexp.QuoteMeta(s), `\`, `\\`)
	}
	return `service=~"` + strings.Join(quoted, "|") + `"`
}

// day is one day's minutes, by service.
type day struct {
	start    time.Time
	computed time.Time
	minutes  map[string][]byte
}

// history holds the days computed so far, by date.
type history struct {
	mu   sync.Mutex
	days map[string]*day
}

func newHistory() *history { return &history{days: map[string]*day{}} }

// dayStart is the midnight that begins the day d days before today, in loc.
func dayStart(now time.Time, loc *time.Location, d int) time.Time {
	n := now.In(loc)
	return time.Date(n.Year(), n.Month(), n.Day()-d, 0, 0, 0, 0, loc)
}

// due says which services of a day need computing now.
func (h *history) due(date string, svcs []string, now, end time.Time) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	d := h.days[date]
	if d == nil {
		return svcs
	}
	var stale bool
	switch {
	case now.Before(end): // today
		stale = now.Sub(d.computed) >= refreshToday
	case d.computed.Before(end.Add(final)): // not final yet
		stale = now.Sub(d.computed) >= refreshRecent || now.After(end.Add(final))
	}
	if stale {
		return svcs
	}
	var out []string
	for _, s := range svcs {
		if _, ok := d.minutes[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}

// refresh computes what is due of the last Days days for svcs, the newest first, until ctx is done or
// past days have been asked limit times (0: no limit; today is always asked). Two refreshes at once may
// ask for the same day; the later answer wins, and both are the same.
func (h *history) refresh(ctx context.Context, q Querier, svcs []string, now time.Time, loc *time.Location, limit int) error {
	if q == nil || len(svcs) == 0 {
		return nil
	}
	asked := 0
	var errs []string
	for d := 0; d < Days; d++ {
		if ctx.Err() != nil {
			break
		}
		start := dayStart(now, loc, d)
		end := dayStart(now, loc, d-1)
		date := start.Format(time.DateOnly)
		want := h.due(date, svcs, now, end)
		if len(want) == 0 {
			continue
		}
		if d > 0 {
			if limit > 0 && asked >= limit {
				break
			}
			asked++
		}
		until := end
		if now.Before(end) {
			until = now.Truncate(time.Minute)
		}
		mins, err := query(ctx, q, want, start, until)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		h.mu.Lock()
		cur := h.days[date]
		if cur == nil || !cur.start.Equal(start) {
			cur = &day{start: start, minutes: map[string][]byte{}}
			h.days[date] = cur
		}
		for _, s := range want {
			cur.minutes[s] = mins[s]
		}
		cur.computed = now
		h.mu.Unlock()
	}
	h.forget(now, loc)
	if len(errs) > 0 {
		return fmt.Errorf("probe history: %s", errs[0])
	}
	return nil
}

// forget drops the days older than the history.
func (h *history) forget(now time.Time, loc *time.Location) {
	oldest := dayStart(now, loc, Days-1).Format(time.DateOnly)
	h.mu.Lock()
	defer h.mu.Unlock()
	for date := range h.days {
		if date < oldest {
			delete(h.days, date)
		}
	}
}

// perQuery is how many services one query names, so that its URL stays short.
const perQuery = 40

// safe reports whether a service label's value can go into a query as it is: no quotes, backslashes or
// control characters, which a label from outside could use to change the query. Pages name their
// services so (package catalog checks); services found through a page label are checked here.
func safe(s string) bool {
	return s != "" && len(s) <= 200 && !strings.ContainsAny(s, "\"\\`") && !strings.ContainsFunc(s, unicode.IsControl)
}

// query asks VictoriaMetrics for the minutes of svcs from start to until, one point a minute: the point
// at t is the minute that ends at t. Every service gets an array, of no data where nothing came back; a
// service that cannot be put into a query safely is never asked for.
func query(ctx context.Context, q Querier, svcs []string, start, until time.Time) (map[string][]byte, error) {
	out := map[string][]byte{}
	var ask []string
	for _, s := range svcs {
		out[s] = make([]byte, minutesPerDay)
		if safe(s) {
			ask = append(ask, s)
		}
	}
	if !until.After(start) {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	for chunk := range slices.Chunk(ask, perQuery) {
		series, err := q.QueryRange(ctx, agreement(selector(chunk)), start.Add(time.Minute), until, time.Minute)
		if err != nil {
			return nil, err
		}
		for _, s := range series {
			mins, ok := out[s.Labels["service"]]
			if !ok {
				continue
			}
			for _, p := range s.Points {
				i := int(p.T.Sub(start)/time.Minute) - 1
				if i < 0 || i >= minutesPerDay || math.IsNaN(p.V) {
					continue
				}
				mins[i] = byte(min(max(math.Round(p.V), float64(minOK)), float64(minDown)))
			}
		}
	}
	return out, nil
}

// minutes returns the minutes of one service on the day that begins at start, or nil.
func (h *history) minutes(start time.Time, svc string) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	d := h.days[start.Format(time.DateOnly)]
	if d == nil || !d.start.Equal(start) {
		return nil
	}
	return d.minutes[svc]
}

// latest is the state of the services in the last minutes computed: the worst of them over the last
// three minutes that have data, and whether any has data at all.
func (h *history) latest(now time.Time, loc *time.Location, svcs []string) (byte, bool) {
	worstMin, seen := minNone, false
	for _, s := range svcs {
		start := dayStart(now, loc, 0)
		last := int(now.Sub(start)/time.Minute) - 1
		mins := h.minutes(start, s)
		if last < 3 { // just after midnight: the end of yesterday counts too
			if y := h.minutes(dayStart(now, loc, 1), s); y != nil {
				mins = append(slices.Clone(y), mins...)
				last += len(y)
			}
		}
		for i := min(last, len(mins)-1); i >= 0 && i > last-3; i-- {
			if m := mins[i]; m != minNone {
				seen = true
				worstMin = max(worstMin, m)
				break
			}
		}
	}
	return worstMin, seen
}

func pct(up, total int) *float64 {
	if total == 0 {
		return nil
	}
	v := math.Floor(float64(up)/float64(total)*10000) / 100 // never rounded up to 100
	return &v
}

func uniq(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		for _, s := range l {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	slices.Sort(out)
	return out
}
