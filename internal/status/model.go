package status

import (
	"slices"
	"time"
)

// The states a component, a day or a page is in, as the JSON names them.
const (
	Operational = "operational"
	Degraded    = "degraded"
	Down        = "down"
	Maintenance = "maintenance"
	NoData      = "no_data"
)

// rank orders the states from the best to the worst news, for the worst of several.
var rank = map[string]int{NoData: 0, Operational: 1, Maintenance: 2, Degraded: 3, Down: 4}

func worst(a, b string) string {
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Page is a status page as the public JSON, the HTML and the agents' API show it: the state of each
// component now and over the last 90 days, the incidents (active, then those of the last 30 days) and
// the maintenance windows (active and scheduled, then those of the last 30 days). Its shape is stable:
// fields are added, never changed or removed. Texts are in Language.
type Page struct {
	Name        string      `json:"name"`
	Title       string      `json:"title"`
	Description string      `json:"description,omitempty"`
	URL         string      `json:"url"`
	Language    string      `json:"language"`
	Languages   []string    `json:"languages"`
	Status      string      `json:"status"`
	Summary     string      `json:"summary"`
	Updated     time.Time   `json:"updated"`
	Days        int         `json:"days"`      // how many days the history holds
	Timezone    string      `json:"timezone"`  // the days begin at midnight here
	Locations   []string    `json:"locations"` // where the probes check from, by public name
	Components  []Component `json:"components"`
	Groups      []Group     `json:"groups"`
	Incidents   []Incident  `json:"incidents"`
	Maintenance []Window    `json:"maintenance"`
	Links       Links       `json:"links"`

	all     []Incident // every incident kept, as far back as the history: the incident pages
	version int64      // of the configuration it was computed from
}

// Component is one line of the page.
type Component struct {
	Name         string   `json:"name"`
	Title        string   `json:"title"`
	Description  string   `json:"description,omitempty"`
	Group        string   `json:"group,omitempty"` // the title of its group
	Status       string   `json:"status"`
	Availability *float64 `json:"availability,omitempty"` // percent over the history, without maintenance; none without data
	History      []Day    `json:"history"`                // oldest first, today last
	services     []string
}

// Day is one day of a component's history. Availability is the share of the minutes with data, outside
// maintenance, that were not down.
type Day struct {
	Date            string   `json:"date"` // YYYY-MM-DD
	Status          string   `json:"status"`
	Availability    *float64 `json:"availability,omitempty"`
	DownMinutes     int      `json:"down_minutes,omitempty"`
	DegradedMinutes int      `json:"degraded_minutes,omitempty"`
	Incidents       []string `json:"incidents,omitempty"` // ids
	Maintenance     bool     `json:"maintenance,omitempty"`
}

// Group puts components under a heading.
type Group struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Components  []string `json:"components"`
}

// Incident is an alert group on the page's components: what it did to them (Impact: down or degraded),
// when, and its updates, the newest first.
type Incident struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Status     string     `json:"status"` // active or resolved
	Impact     string     `json:"impact"` // down or degraded
	Components []string   `json:"components"`
	Started    time.Time  `json:"started"`
	Resolved   *time.Time `json:"resolved,omitempty"`
	URL        string     `json:"url"`
	Updates    []Update   `json:"updates"`
}

// Update is one step of an incident: detected, acknowledged, a note (By says whether an agent or a
// person left it), resolved.
type Update struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // detected, acknowledged, note, resolved
	Text string    `json:"text"`
	By   string    `json:"by,omitempty"` // agent or person, for a note
}

// Window is a maintenance window on the page's components.
type Window struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"` // scheduled, active or completed
	Components  []string  `json:"components"`
	Starts      time.Time `json:"starts"`
	Ends        time.Time `json:"ends"`
}

// Links are where the page points people.
type Links struct {
	Feed    string `json:"feed"`
	JSON    string `json:"json"`
	Badge   string `json:"badge"`
	Home    string `json:"home,omitempty"`
	Docs    string `json:"docs,omitempty"`
	Abuse   string `json:"abuse,omitempty"`
	Privacy string `json:"privacy,omitempty"`
}

// Services are the services the component's state is made from, as the configuration in force names them.
func (c Component) Services() []string { return slices.Clone(c.services) }

// Incident returns the page's incident with id, as far back as the history reaches.
func (p *Page) Incident(id string) (Incident, bool) {
	for _, i := range p.all {
		if i.ID == id {
			return i, true
		}
	}
	return Incident{}, false
}

// Component returns the page's component called name.
func (p *Page) Component(name string) (Component, bool) {
	for _, c := range p.Components {
		if c.Name == name {
			return c, true
		}
	}
	return Component{}, false
}
