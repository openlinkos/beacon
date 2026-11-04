package bridge

import (
	"slices"

	"github.com/openlinkos/beacon/internal/store"
)

// The alert groups as the public status pages see them (package status): incidents, with their text
// cleaned as for agents. Only critical and warning groups that name services are incidents; which of
// them a page shows, and which of their notes, the page decides.

// Incidents lists the alert groups that may be incidents on a status page, the newest first.
func (b *Bridge) Incidents() []store.Incident {
	var out []store.Incident
	for _, g := range b.d.Store.Groups() {
		if g.Severity != "critical" && g.Severity != "warning" {
			continue
		}
		in := incidentOf(&g)
		if len(in.Services) == 0 {
			continue
		}
		out = append(out, in)
	}
	return out
}

func incidentOf(g *store.Group) store.Incident {
	in := store.Incident{ID: g.ID, AlertName: g.Common["alertname"], Severity: g.Severity, Status: g.Status, StartsAt: g.StartsAt,
		ResolvedAt: g.ResolvedAt, Services: []string{}}
	if in.AlertName == "" {
		in.AlertName = g.Labels["alertname"]
	}
	add := func(list *[]string, s string) {
		if s != "" && !slices.Contains(*list, s) {
			*list = append(*list, s)
		}
	}
	add(&in.Services, g.Labels["service"])
	add(&in.Services, g.Common["service"])
	firing := 0
	for _, a := range g.Alerts {
		add(&in.Services, a.Labels["service"])
		if g.Firing() && a.Status == "firing" {
			firing++
			if s := a.Labels["service"]; s != "" {
				add(&in.Firing, s)
			} else {
				add(&in.Firing, g.Labels["service"])
				add(&in.Firing, g.Common["service"])
			}
		}
	}
	if g.Firing() && firing == 0 { // a group whose alerts are not listed is judged by its own labels
		add(&in.Firing, g.Labels["service"])
		add(&in.Firing, g.Common["service"])
	}
	slices.Sort(in.Services)
	slices.Sort(in.Firing)
	if g.Ack != nil {
		in.AckedAt = g.Ack.At
	}
	for _, n := range g.Notes {
		in.Notes = append(in.Notes, store.IncidentNote{At: n.At, Text: clean(n.Text, maxLong, true), Agent: n.Via == "api" || n.Via == "mcp"})
	}
	return in
}
