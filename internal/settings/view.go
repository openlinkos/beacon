package settings

import (
	"encoding/json"
	"time"
)

// View is one setting as the API, MCP and the console show it: what it turns, its default, the
// value in force, where that came from, and the row behind it with who set it, when and why.
type View struct {
	Key     string          `json:"key"`
	Group   string          `json:"group"`
	What    string          `json:"what"`
	Default json.RawMessage `json:"default"`
	Value   json.RawMessage `json:"value"`
	From    string          `json:"from"`              // default, system, collection, tag, entitlement or request
	Target  string          `json:"target,omitempty"`  // the scope's target, when from is neither default nor system
	Version int64           `json:"version,omitempty"` // of the row in force, when there is one
	Actor   string          `json:"actor,omitempty"`
	Via     string          `json:"via,omitempty"`
	At      string          `json:"at,omitempty"`
	Reason  string          `json:"reason,omitempty"`
	// Levels are the value each present level gives, least specific first, when the setting was
	// resolved for more than the system level.
	Levels []Level `json:"levels,omitempty"`
}

// Level is one level's value in a View's Levels.
type Level struct {
	From   string          `json:"from"`
	Target string          `json:"target,omitempty"`
	Value  json.RawMessage `json:"value"`
}

// RowView is a row that is not at the system level: what some collection, tag, entitlement or
// request holds, for the listings that watch the overrides.
type RowView struct {
	Key     string          `json:"key"`
	Scope   string          `json:"scope"`
	Target  string          `json:"target"`
	Value   json.RawMessage `json:"value"`
	Version int64           `json:"version"`
	Actor   string          `json:"actor"`
	Via     string          `json:"via"`
	At      string          `json:"at"`
	Reason  string          `json:"reason,omitempty"`
}

// Listing is every setting with the rows that are not the system's, and the rows that failed their
// signature (served as the registered failsafe, and reported).
type Listing struct {
	Settings  []View    `json:"settings"`
	Overrides []RowView `json:"overrides,omitempty"`
	BadRows   []string  `json:"bad_rows,omitempty"`
}

// ViewOf shapes a resolved setting with its definition.
func ViewOf(d *Definition, r Resolved) View {
	v := View{Key: d.Key, Group: d.Group, What: d.What, Default: d.Default,
		Value: r.Value, From: r.From, Target: r.Target}
	if r.Row != nil {
		v.Version, v.Actor, v.Via, v.Reason = r.Row.Version, r.Row.Actor, r.Row.Via, r.Row.Reason
		v.At = r.Row.At.Format(time.RFC3339)
	}
	return v
}

// List is every setting at the system level, with the other scopes' rows and the rows that failed
// their signature.
func (s *Service) List() Listing {
	out := Listing{Settings: make([]View, 0, len(Definitions))}
	for _, d := range Ordered() {
		r, err := s.System(d.Key)
		if err != nil {
			continue
		}
		out.Settings = append(out.Settings, ViewOf(d, r))
	}
	for _, row := range s.Overrides() {
		v := RowView{Key: row.Key, Scope: row.Scope, Target: row.Target, Value: row.Value, Version: row.Version,
			Actor: row.Actor, Via: row.Via, At: row.At.Format(time.RFC3339), Reason: row.Reason}
		out.Overrides = append(out.Overrides, v)
	}
	out.BadRows = s.Bad()
	return out
}
