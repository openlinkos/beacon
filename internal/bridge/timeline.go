package bridge

import (
	"context"
	"sort"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// Timeline is an alert group's story in time order: when it started and how people were told, what
// people and agents did, which agents were woken, and the changes and sessions on its machines around it,
// from before it started. Heartbeats are there when its labels name a heartbeat, as in the evidence.
type Timeline struct {
	Alert      AlertBrief      `json:"alert"`
	Window     Window          `json:"window"`
	Untrusted  Untrusted       `json:"untrusted"`
	Heartbeats []HeartbeatView `json:"heartbeats,omitempty"`
	Events     []Event         `json:"events"`
}

// Event is one entry of a timeline. Kind is started, notified, buzzed, pushover, notification, acknowledged, note,
// silenced, silence_ended, agent, agent_run, change, session, command (one run at a shell's prompt), heartbeat (its
// last success), probe_now or resolved; the other fields say what each needs. URL is a change's link, or the signed
// link to a session's page, where people replay it. Phase, on changes, sessions, commands and heartbeats,
// says whether the event came before the group started, while it fired or after it resolved.
type Event struct {
	At      time.Time                `json:"at"`
	Kind    string                   `json:"kind"`
	By      string                   `json:"by,omitempty"`   // a person, a token, a hook, a change's source
	Via     string                   `json:"via,omitempty"`  // how: feishu, pushover, api, mcp, ssh; the channel people were told on
	Text    string                   `json:"text,omitempty"` // a note, a comment, a change's summary, a command, why a call failed, a heartbeat's description
	Type    string                   `json:"type,omitempty"` // a change's kind, a session's kind, a hook's kind
	Machine string                   `json:"machine,omitempty"`
	Service string                   `json:"service,omitempty"`
	ID      string                   `json:"id,omitempty"` // the session (of a command too), change, silence or hook delivery; the heartbeat's name
	Until   *time.Time               `json:"until,omitempty"`
	Event   string                   `json:"event,omitempty"`  // an agent call: for firing or resolved
	Result  string                   `json:"result,omitempty"` // provider result or reported execution phase
	Exit    *int                     `json:"exit,omitempty"`   // a session, command or programme exit status
	URL     string                   `json:"url,omitempty"`
	Related string                   `json:"related,omitempty"` // why a change or session is listed: service, machine or agent
	Phase   string                   `json:"phase,omitempty"`   // before, during or after
	Probes  []store.ProbeObservation `json:"probes,omitempty"`  // on-demand observations, including missing results
}

// Timeline returns the alert group id's timeline, over the window of its evidence.
func (b *Bridge) Timeline(ctx context.Context, id, before string) (Timeline, error) {
	d, err := parseBefore(before)
	if err != nil {
		return Timeline{}, err
	}
	g, ok := b.d.Store.Group(id)
	if !ok {
		return Timeline{}, ErrNotFound
	}
	w := b.window(&g, d)
	ev := []Event{{At: g.StartsAt, Kind: "started", Service: cleanLine(b.service(&g))}}
	add := func(e Event) {
		if !e.At.IsZero() {
			ev = append(ev, e)
		}
	}
	for _, e := range notificationEvents(&g) {
		add(e)
	}
	for _, a := range g.PriorAcks {
		add(Event{At: a.At, Kind: "acknowledged", By: a.By, Via: a.Via, Type: a.Kind, Text: clean(a.Note, maxLong, true)})
	}
	if g.Ack != nil {
		add(Event{At: g.Ack.At, Kind: "acknowledged", By: g.Ack.By, Via: g.Ack.Via, Type: g.Ack.Kind, Text: clean(g.Ack.Note, maxLong, true)})
	}
	for _, n := range g.Notes {
		add(Event{At: n.At, Kind: "note", By: n.By, Via: n.Via, Text: clean(n.Text, maxLong, true)})
	}
	for _, s := range b.silences(&g, w) {
		until := s.EndsAt
		add(Event{At: s.StartsAt, Kind: "silenced", By: s.By, Text: s.Comment, ID: s.ID, Until: &until})
		if !s.EndsAt.After(b.now()) {
			add(Event{At: s.EndsAt, Kind: "silence_ended", ID: s.ID})
		}
	}
	for _, c := range g.Agents {
		at := c.At
		if !c.Done.IsZero() {
			at = c.Done
		}
		add(Event{At: at, Kind: "agent", By: c.Hook, Type: c.Kind, Event: c.Event, ID: c.Delivery, Result: c.Status, Text: cleanLine(c.Error)})
		for _, r := range []*store.AgentRun{c.RunStart, c.Run} {
			if r != nil {
				add(Event{At: r.At, Kind: "agent_run", By: c.Hook, Via: r.Via, Type: c.Kind,
					Event: c.Event, ID: c.Delivery, Result: r.Status, Exit: r.ExitCode})
			}
		}
	}
	for _, run := range g.ProbeRuns {
		add(Event{At: run.At, Kind: "probe_now", By: run.By, Via: run.Via, ID: run.ID, Probes: run.Results})
	}
	machines := b.machines(&g)
	for _, c := range b.changesAround(&g, w, machines) {
		add(Event{At: c.Time, Kind: "change", By: c.Source, Type: c.Kind, Text: c.Summary, Service: c.Service, Machine: c.Machine, ID: c.ID,
			URL: c.URL, Related: c.Related, Phase: c.Phase})
	}
	ss := b.sessions(&g, w, machines)
	b.withCommands(ss)
	for _, s := range ss {
		add(Event{At: s.Started, Kind: "session", By: s.Agent, Via: s.Via, Type: s.Kind, Machine: s.Machine, Text: s.Command, ID: s.ID,
			Exit: s.Exit, URL: s.Page, Related: s.Related, Phase: s.Phase})
		for _, c := range s.ShellCommands {
			add(Event{At: c.Started, Kind: "command", By: s.Agent, Via: s.Via, Type: s.Kind, Machine: s.Machine, Text: c.Command, ID: s.ID,
				Exit: c.Exit, URL: s.Page, Related: s.Related, Phase: phase(&g, c.Started)})
		}
	}
	hbs := b.heartbeats(ctx, &g, w)
	for _, h := range hbs {
		if h.LastSuccess != nil { // even when it is before the window: it is when the story began
			add(Event{At: *h.LastSuccess, Kind: "heartbeat", ID: h.Name, Machine: h.Machine, Text: h.Description, Phase: phase(&g, *h.LastSuccess)})
		}
	}
	if !g.Firing() {
		add(Event{At: g.ResolvedAt, Kind: "resolved"})
	}
	sort.SliceStable(ev, func(i, j int) bool { return ev[i].At.Before(ev[j].At) })
	return Timeline{Alert: b.brief(&g), Window: w, Heartbeats: hbs, Events: ev,
		Untrusted: untrusted("alert.title", "alert.description", "alert.service", "alert.services", "alert.labels",
			"heartbeats[].description", "heartbeats[].error", "events[].text", "events[].service",
			"events[].probes[].instance", "events[].probes[].module", "events[].probes[].reason")}, nil
}
