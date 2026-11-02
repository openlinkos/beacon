package status

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

// publicPrefix marks a note for the public: on a page with notes: public (the default), only the notes
// that begin with it are its incidents' updates.
const publicPrefix = "public:"

// componentsOf are the names of the page's components that any of svcs backs.
func componentsOf(p *Page, svcs []string) []string {
	var out []string
	for _, c := range p.Components {
		if slices.ContainsFunc(c.services, func(s string) bool { return slices.Contains(svcs, s) }) {
			out = append(out, c.Name)
		}
	}
	return out
}

func (p *Page) servicesOf(names []string) []string {
	var out []string
	for _, c := range p.Components {
		if slices.Contains(names, c.Name) {
			out = append(out, c.services...)
		}
	}
	return out
}

func (p *Page) titles(names []string, l i18n.Lang) string {
	var out string
	for _, c := range p.Components {
		if !slices.Contains(names, c.Name) {
			continue
		}
		if out == "" {
			out = c.Title
		} else {
			out = i18n.T(l, "%s, %s", out, c.Title)
		}
	}
	return out
}

// incidents are the alert groups on the page's components that the page counts (its alerts), as far
// back as the history reaches: the active ones first, then the newest.
func (s *Service) incidents(def catalog.StatusPage, p *Page, l i18n.Lang, now time.Time) []Incident {
	if s.d.Store == nil {
		return []Incident{}
	}
	oldest := dayStart(now, s.d.Location, Days-1)
	alerts := def.IncidentAlerts()
	out := []Incident{}
	for _, rec := range s.d.Store.Incidents() {
		if !slices.Contains(alerts, rec.AlertName) {
			continue
		}
		comps := componentsOf(p, rec.Services)
		if len(comps) == 0 {
			continue
		}
		active := rec.Status == "firing" && slices.ContainsFunc(rec.Firing, func(svc string) bool { return slices.Contains(p.servicesOf(comps), svc) })
		in := Incident{ID: rec.ID, Status: "resolved", Impact: Degraded, Components: comps, Started: rec.StartsAt.UTC(), URL: s.URL(def, "incidents/"+rec.ID)}
		if rec.AlertName == "ServiceDown" {
			in.Impact = Down
		}
		if active {
			in.Status = "active"
		} else if !rec.ResolvedAt.IsZero() {
			t := rec.ResolvedAt.UTC()
			in.Resolved = &t
		}
		if in.Resolved != nil && in.Resolved.Before(oldest) {
			continue
		}
		if in.Impact == Down {
			in.Title = i18n.T(l, "Outage: %s", p.titles(comps, l))
		} else {
			in.Title = i18n.T(l, "Partial outage: %s", p.titles(comps, l))
		}
		in.Updates = updates(def, rec, in, l)
		out = append(out, in)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Status == "active") != (out[j].Status == "active") {
			return out[i].Status == "active"
		}
		return out[i].Started.After(out[j].Started)
	})
	return out
}

// updates are an incident's steps, the newest first: when the probes found it, when someone took it on,
// the notes the page publishes, and when it resolved.
func updates(def catalog.StatusPage, rec store.Incident, in Incident, l i18n.Lang) []Update {
	var out []Update
	detected := i18n.T(l, "The probes see it failing from some of their locations.")
	if in.Impact == Down {
		detected = i18n.T(l, "The probes see it failing from at least half of their locations.")
	}
	out = append(out, Update{At: rec.StartsAt.UTC(), Kind: "detected", Text: detected})
	if !rec.AckedAt.IsZero() {
		out = append(out, Update{At: rec.AckedAt.UTC(), Kind: "acknowledged", Text: i18n.T(l, "We are looking into it.")})
	}
	for _, n := range rec.Notes {
		text, marked := strings.TrimSpace(n.Text), false
		if len(text) >= len(publicPrefix) && strings.EqualFold(text[:len(publicPrefix)], publicPrefix) {
			text, marked = strings.TrimSpace(text[len(publicPrefix):]), true
		}
		if text == "" || (!marked && !def.AllNotes()) {
			continue
		}
		by := "person"
		if n.Agent {
			by = "agent"
		}
		out = append(out, Update{At: n.At.UTC(), Kind: "note", Text: text, By: by})
	}
	if in.Resolved != nil {
		out = append(out, Update{At: *in.Resolved, Kind: "resolved", Text: i18n.T(l, "The probes see it working again.")})
	}
	// the oldest first, steps at the same time in the order they were taken; then turned round
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	slices.Reverse(out)
	return out
}

// window is a maintenance window on the page, with when it covers.
type window struct {
	Window
	start, end time.Time
}

// windows are the maintenance windows on the page's components, as far back as the history reaches: the
// active ones, then those to come (the soonest first), then those that ended (the latest first). They
// are the windows planned through Beacon, and the silences whose comment begins with "maintenance:".
func (s *Service) windows(def catalog.StatusPage, p *Page, now time.Time) []window {
	if s.d.Store == nil {
		return nil
	}
	oldest := dayStart(now, s.d.Location, Days-1)
	var out []window
	add := func(id, title, desc string, comps []string, start, end time.Time) {
		if len(comps) == 0 || end.Before(oldest) || !end.After(start) {
			return
		}
		w := window{Window: Window{ID: id, Title: title, Description: desc, Components: comps, Starts: start.UTC(), Ends: end.UTC()}, start: start, end: end}
		switch {
		case now.Before(start):
			w.Status = "scheduled"
		case now.Before(end):
			w.Status = "active"
		default:
			w.Status = "completed"
		}
		out = append(out, w)
	}
	planned := map[string]bool{}
	for _, m := range s.d.Store.MaintenanceWindows() {
		for _, id := range m.Silences {
			planned[id] = true
		}
		if !m.Cancelled {
			add(m.ID, m.Title, m.Description, componentsOf(p, m.Services), m.StartsAt, m.End())
		}
	}
	for _, sil := range s.d.Store.SilenceRecords() {
		c := strings.TrimSpace(sil.Comment)
		if planned[sil.ID] || len(c) < len(bridge.MaintenancePrefix) || !strings.EqualFold(c[:len(bridge.MaintenancePrefix)], bridge.MaintenancePrefix) {
			continue
		}
		var comps []string
		for _, cm := range p.Components {
			if slices.ContainsFunc(cm.services, func(svc string) bool { return coversService(sil, svc) }) {
				comps = append(comps, cm.Name)
			}
		}
		add("s-"+sil.ID, strings.TrimSpace(c[len(bridge.MaintenancePrefix):]), "", comps, sil.StartsAt, sil.EndsAt)
	}
	order := map[string]int{"active": 0, "scheduled": 1, "completed": 2}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if order[a.Status] != order[b.Status] {
			return order[a.Status] < order[b.Status]
		}
		if a.Status == "scheduled" {
			return a.start.Before(b.start)
		}
		return a.start.After(b.start)
	})
	return out
}

// coversService reports whether a silence's matchers on the service label all match svc; a silence
// without one covers no service on a page.
func coversService(s store.SilenceRecord, svc string) bool {
	n := 0
	for _, m := range s.Matchers {
		if m.Name != "service" {
			continue
		}
		n++
		ok := m.Value == svc
		if m.IsRegex {
			re, err := regexp.Compile("^(?:" + m.Value + ")$")
			ok = err == nil && re.MatchString(svc)
		}
		if ok != m.IsEqual {
			return false
		}
	}
	return n > 0
}

// days is a component's history, the oldest day first, and its availability over all of it.
func (s *Service) days(c *Component, incidents []Incident, windows []window, now time.Time) ([]Day, *float64) {
	loc := s.d.Location
	out := make([]Day, 0, Days)
	var allData, allDown int
	for d := Days - 1; d >= 0; d-- {
		start, end := dayStart(now, loc, d), dayStart(now, loc, d-1)
		n := min(minutesPerDay, int(end.Sub(start)/time.Minute))
		if now.Before(end) {
			n = min(n, int(now.Sub(start)/time.Minute))
		}
		day := Day{Date: start.Format(time.DateOnly)}
		maint := make([]bool, minutesPerDay)
		inc := make([]byte, minutesPerDay)
		cover := func(from, to time.Time, f func(i int)) {
			lo := max(0, int(from.Sub(start)/time.Minute))
			hi := min(n, int((to.Sub(start)+time.Minute-1)/time.Minute))
			for i := lo; i < hi; i++ {
				f(i)
			}
		}
		for _, w := range windows {
			if slices.Contains(w.Components, c.Name) && w.start.Before(end) && w.end.After(start) {
				cover(w.start, w.end, func(i int) { maint[i] = true })
			}
		}
		for _, in := range incidents {
			to := now
			if in.Resolved != nil {
				to = *in.Resolved
			}
			if !slices.Contains(in.Components, c.Name) || !in.Started.Before(end) || !to.After(start) {
				continue
			}
			day.Incidents = append(day.Incidents, in.ID)
			m := minDegraded
			if in.Impact == Down {
				m = minDown
			}
			cover(in.Started, to, func(i int) { inc[i] = max(inc[i], m) })
		}
		mins := make([][]byte, 0, len(c.services))
		for _, svc := range c.services {
			if m := s.hist.minutes(start, svc); m != nil {
				mins = append(mins, m)
			}
		}
		var data, down, degraded, maintained int
		var incDown, incDegraded bool
		for i := range n {
			if maint[i] {
				maintained++
				continue
			}
			m := inc[i]
			incDown = incDown || m == minDown
			incDegraded = incDegraded || m == minDegraded
			for _, ms := range mins {
				m = max(m, ms[i])
			}
			switch m {
			case minNone:
				continue
			case minDown:
				down++
			case minDegraded:
				degraded++
			}
			data++
		}
		day.DownMinutes, day.DegradedMinutes, day.Maintenance = down, degraded, maintained > 0
		day.Availability = pct(data-down, data)
		switch {
		case down >= 2 || incDown:
			day.Status = Down
		case down+degraded >= 2 || incDegraded:
			day.Status = Degraded
		case maintained > 0:
			day.Status = Maintenance
		case data > 0:
			day.Status = Operational
		default:
			day.Status = NoData
		}
		allData += data
		allDown += down
		out = append(out, day)
	}
	return out, pct(allData-allDown, allData)
}
