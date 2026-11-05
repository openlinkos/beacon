package settings

import (
	"encoding/json"
	"slices"
)

// Selector is what a value is resolved for: the collection the item is in, the tags on it, the
// entitlement the agent asks through, and the request itself. Empty parts are simply not there; a
// request with no overrides of its own resolves to what the levels above give.
type Selector struct {
	Collection  string
	Tags        []string
	Entitlement string
	Request     string
}

// Resolved is one setting as it stands for a selector: the value in force, where it came from
// (default, system, or a scope with its target), and the row behind it, if any, with its version
// and who set it.
type Resolved struct {
	Key    string
	Value  json.RawMessage
	From   string // default, or the scope the winning value sits at
	Target string // the scope's target, when it is not the system scope
	Row    *Row
}

// Value walks the selector's levels from the least specific: the default, the system row, the
// collection's row, the tags' rows (the stricter of them), the entitlement's row, and at last the
// request's row — which may only tighten what the level above gives, never loosen it. Approval
// levels do not travel this line; they are the strictest of item, collection and tags, which is
// the design's other rule and the vault's business, not this package's.

// Value is the setting in force for the selector: the value of its most specific level, clamped
// where a request tried to loosen. Its second answer holds each level that was present, least
// specific first, for the console to show where a value was overridden.
func (s *Service) Value(key string, sel Selector) (Resolved, []Resolved, error) {
	def, ok := Definitions[key]
	if !ok {
		return Resolved{}, nil, ErrUnknownKey(key)
	}
	var out []Resolved
	add := func(r Resolved) { out = append(out, r) }

	s.rowsMtx.Lock()
	defer s.rowsMtx.Unlock()
	if len(s.key) == 0 || s.tableFailsafe {
		r := Resolved{Key: key, Value: def.Failsafe, From: "failsafe"}
		return r, []Resolved{r}, nil
	}

	// the default every level starts from
	add(Resolved{Key: key, Value: def.Default, From: "default"})

	if row, ok := s.rows[rowKey(ScopeSystem, "", key)]; ok {
		add(Resolved{Key: key, Value: row.Value, From: ScopeSystem, Row: &row})
	} else if s.broken[rowKey(ScopeSystem, "", key)] {
		add(Resolved{Key: key, Value: def.Failsafe, From: "failsafe"})
	}
	if sel.Collection != "" {
		if row, ok := s.rows[rowKey(ScopeCollection, sel.Collection, key)]; ok {
			r := row
			add(Resolved{Key: key, Value: r.Value, From: ScopeCollection, Target: sel.Collection, Row: &r})
		} else if s.broken[rowKey(ScopeCollection, sel.Collection, key)] {
			add(Resolved{Key: key, Value: def.Failsafe, From: "failsafe", Target: sel.Collection})
		}
	}
	// several tags may cover one item: their rows are all at one level, and the stricter wins —
	// counts and durations take the smaller, consequences the more conservative, and the rest, which
	// the design does not order, fall back to the first tag in name order, so the answer is fixed
	var tagRows []Row
	for _, t := range slices.Sorted(slices.Values(sel.Tags)) {
		if row, ok := s.rows[rowKey(ScopeTag, t, key)]; ok {
			tagRows = append(tagRows, row)
			continue
		}
		if s.broken[rowKey(ScopeTag, t, key)] {
			tagRows = append(tagRows, Row{Key: key, Scope: ScopeTag, Target: t, Value: def.Failsafe})
		}
	}
	if len(tagRows) > 0 {
		won := tagRows[0]
		for _, r := range tagRows[1:] {
			w, ok := def.stricter(won.Value, r.Value)
			if !ok {
				continue // unordered: the first tag in name order holds
			}
			if string(w) == string(r.Value) {
				won = r // the stricter value came from this tag
			} else {
				won.Value = w
			}
		}
		add(Resolved{Key: key, Value: won.Value, From: ScopeTag, Target: won.Target, Row: &won})
	}
	if sel.Entitlement != "" {
		if row, ok := s.rows[rowKey(ScopeEntitlement, sel.Entitlement, key)]; ok {
			r := row
			add(Resolved{Key: key, Value: r.Value, From: ScopeEntitlement, Target: sel.Entitlement, Row: &r})
		} else if s.broken[rowKey(ScopeEntitlement, sel.Entitlement, key)] {
			add(Resolved{Key: key, Value: def.Failsafe, From: "failsafe", Target: sel.Entitlement})
		}
	}
	if sel.Request != "" && requestMaySet[key] {
		var r Row
		var have bool
		if row, ok := s.rows[rowKey(ScopeRequest, sel.Request, key)]; ok {
			r, have = row, true
		} else if s.broken[rowKey(ScopeRequest, sel.Request, key)] {
			r, have = Row{Key: key, Scope: ScopeRequest, Target: sel.Request, Value: def.Failsafe}, true
		}
		if have {
			// a request may only tighten: where the design orders the values, the request's
			// choice holds only when it is the stricter of it and the level above; where it does
			// not, the request's value simply wins, being the more specific
			above := out[len(out)-1]
			if w, ok := def.stricter(above.Value, r.Value); ok {
				r.Value = w
			}
			add(Resolved{Key: key, Value: r.Value, From: ScopeRequest, Target: sel.Request, Row: &r})
		}
	}
	return out[len(out)-1], out, nil
}

// ErrUnknownKey says the key is not a setting there is.
func ErrUnknownKey(key string) error { return &unknownKeyError{key} }

type unknownKeyError struct{ key string }

func (e *unknownKeyError) Error() string {
	return "unknown setting " + e.key + "; settings_get lists what there is"
}

// System is the setting at the system level only: the value in force for everyone, which is what
// the settings pages and the plain get show. Level is where it stands: default when nothing
// overrides it, system when a row does.
func (s *Service) System(key string) (Resolved, error) {
	r, _, err := s.Value(key, Selector{})
	return r, err
}

// Values are the resolved system values of every setting, in the order the console shows, for
// pages and listings.
func (s *Service) Values() []Resolved {
	out := make([]Resolved, 0, len(Definitions))
	for _, d := range Ordered() {
		r, err := s.System(d.Key)
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// Overrides lists the rows that are not at the system level, sorted by scope then target then key,
// for the console to show who tightened what where. They are the rows of the scopes the vault
// (W5) and the grants (W7) will sit at; here they can be made and seen, and resolved.
func (s *Service) Overrides() []Row {
	s.rowsMtx.Lock()
	defer s.rowsMtx.Unlock()
	var out []Row
	for _, r := range s.rows {
		if r.Scope != ScopeSystem {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Row) int {
		if a.Scope != b.Scope {
			return levels[a.Scope] - levels[b.Scope]
		}
		if a.Target != b.Target {
			return compare(a.Target, b.Target)
		}
		return compare(a.Key, b.Key)
	})
	return out
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Duration reads a setting that holds a duration, for the code that runs by it. A value that does
// not fit is the default, so a setting nobody could set wrongly stops nothing. unlimited and never
// are not durations: ok is false, so a ticker or a deadline is not created with zero.
func (s *Service) Duration(key string, sel Selector) (d int64, ok bool) {
	r, _, err := s.Value(key, sel)
	if err != nil {
		return 0, false
	}
	parse := func(raw json.RawMessage) (int64, bool) {
		word := jsonString(raw)
		if word == "unlimited" || word == "never" {
			return 0, false
		}
		v, err := ParseDuration(word)
		if err != nil {
			return 0, false
		}
		return int64(v), true
	}
	if n, ok := parse(r.Value); ok {
		return n, true
	}
	def := Definitions[key]
	return parse(def.Default)
}
