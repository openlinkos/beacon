package settings

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A setting's kind says what values fit it and, where the design orders them, which of two values
// is the stricter (the one that wins when tags conflict, or that a request cannot loosen).
// Durations are strings Go's time.ParseDuration reads, plus a d unit for days ("365d"), or the
// words "unlimited" (no bound) and "never" (no expiry), each where the kind allows them.
type kind struct {
	name string
	// check canonicalises and validates a value, returning it as it is stored (compact JSON).
	check func(v json.RawMessage) (json.RawMessage, error)
	// stricter says which of two valid values is the stricter, when the design orders this kind's
	// values; ok false means the kind has no such order and the more specific scope simply wins.
	stricter func(a, b json.RawMessage) (json.RawMessage, bool)
}

// tNone/tFalse/tTrue say which of a bool is the stricter, or that neither is.
type boolSide int

const (
	tNone boolSide = iota
	tFalse
	tTrue
)

func intKind(more bool) kind {
	return kind{
		name: "int",
		check: func(v json.RawMessage) (json.RawMessage, error) {
			n, err := asInt(v)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("a whole number of 0 or more")
			}
			return mustJSON(n), nil
		},
		stricter: func(a, b json.RawMessage) (json.RawMessage, bool) {
			x, y := jsonInt(a), jsonInt(b)
			if (x <= y) != more {
				return a, true
			}
			return b, true
		},
	}
}

func countKind() kind {
	return kind{
		name: "count",
		check: func(v json.RawMessage) (json.RawMessage, error) {
			if isWord(v, "unlimited") {
				return mustJSON("unlimited"), nil
			}
			n, err := asInt(v)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("a whole number of 0 or more, or unlimited")
			}
			return mustJSON(n), nil
		},
		// smaller is stricter; unlimited, the absence of a bound, is the loosest
		stricter: func(a, b json.RawMessage) (json.RawMessage, bool) {
			x, y := countOrInf(a), countOrInf(b)
			if x <= y {
				return a, true
			}
			return b, true
		},
	}
}

func durationKind(orUnlimited, orNever bool) kind {
	words := ""
	switch {
	case orUnlimited:
		words = ", or unlimited"
	case orNever:
		words = ", or never"
	}
	return kind{
		name: "duration",
		check: func(v json.RawMessage) (json.RawMessage, error) {
			if (orUnlimited && isWord(v, "unlimited")) || (orNever && isWord(v, "never")) {
				return mustJSON(strings.Trim(v.String(), `"`)), nil
			}
			d, err := ParseDuration(jsonString(v))
			if err != nil || d < 0 {
				return nil, fmt.Errorf("a duration such as 30m, 24h or 365d%s", words)
			}
			return mustJSON(jsonString(v)), nil
		},
		// smaller is stricter; unlimited and never are the loosest (no bound, no expiry)
		stricter: func(a, b json.RawMessage) (json.RawMessage, bool) {
			x, y := durationOrInf(a), durationOrInf(b)
			if x <= y {
				return a, true
			}
			return b, true
		},
	}
}

func boolKind(stricter boolSide) kind {
	var cmp func(a, b json.RawMessage) (json.RawMessage, bool)
	switch stricter {
	case tFalse:
		cmp = func(a, b json.RawMessage) (json.RawMessage, bool) { return mustJSON(jsonBool(a) && jsonBool(b)), true }
	case tTrue:
		cmp = func(a, b json.RawMessage) (json.RawMessage, bool) { return mustJSON(jsonBool(a) || jsonBool(b)), true }
	}
	return kind{
		name: "bool",
		check: func(v json.RawMessage) (json.RawMessage, error) {
			var b bool
			if err := json.Unmarshal(v, &b); err != nil {
				return nil, fmt.Errorf("true or false")
			}
			return mustJSON(b), nil
		},
		stricter: cmp,
	}
}

// stringsOrder says how two lists of strings compare: stringsMore holds that the list that says
// more is the stricter (encodings to scrub), stringsFewer that the list that says less is the
// stricter (levels a reviewer may approve); stringsNone that the lists are not ordered.
type stringsOrder int

const (
	stringsNone stringsOrder = iota
	stringsMore
	stringsFewer
)

// stringsKind builds a kind that holds a list of strings, from anywhere or from allowed.
func stringsKind(allowed []string, order stringsOrder) kind {
	return kind{
		name: "strings",
		check: func(v json.RawMessage) (json.RawMessage, error) {
			var in []string
			if err := json.Unmarshal(v, &in); err != nil {
				return nil, fmt.Errorf("a list of strings")
			}
			if len(in) > 64 {
				return nil, fmt.Errorf("at most 64")
			}
			seen := map[string]bool{}
			for _, s := range in {
				if s == "" || len(s) > 128 {
					return nil, fmt.Errorf("strings of 1 to 128 characters")
				}
				if len(allowed) > 0 && !slices.Contains(allowed, s) {
					return nil, fmt.Errorf("one of %s", strings.Join(allowed, ", "))
				}
				if seen[s] {
					return nil, fmt.Errorf("%q twice", s)
				}
				seen[s] = true
			}
			return mustJSON(in), nil
		},
		stricter: func(a, b json.RawMessage) (json.RawMessage, bool) {
			if order == stringsNone {
				return nil, false
			}
			xa, xb := jsonStringList(a), jsonStringList(b)
			aHoldsB, bHoldsA := true, true
			for _, s := range xb {
				if !slices.Contains(xa, s) {
					aHoldsB = false
				}
			}
			for _, s := range xa {
				if !slices.Contains(xb, s) {
					bHoldsA = false
				}
			}
			switch {
			case aHoldsB: // a holds all of b
				if order == stringsMore {
					return a, true
				}
				return b, true
			case bHoldsA:
				if order == stringsMore {
					return b, true
				}
				return a, true
			}
			// incomparable: the stricter reading is the union for stringsMore, the intersection for
			// stringsFewer — never less than either side allows itself
			var out []string
			for _, s := range xa {
				if order == stringsMore || slices.Contains(xb, s) {
					out = append(out, s)
				}
			}
			if order == stringsMore {
				for _, s := range xb {
					if !slices.Contains(out, s) {
						out = append(out, s)
					}
				}
			}
			return mustJSON(out), true
		},
	}
}

// levelsKind builds a kind that holds a value for each approval level (auto, review, person) —
// the per-level defaults the design writes as "auto: …; review: …; person: …". inner fits each
// level's value; stricter compares level by level.
func levelsKind(inner kind) kind {
	levels := []string{"auto", "review", "person"}
	return kind{
		name: "levels",
		check: func(v json.RawMessage) (json.RawMessage, error) {
			var in map[string]json.RawMessage
			if err := json.Unmarshal(v, &in); err != nil {
				return nil, fmt.Errorf("an object with auto, review and person")
			}
			if len(in) == 0 || len(in) > len(levels) {
				return nil, fmt.Errorf("the levels are %s", strings.Join(levels, ", "))
			}
			out := map[string]json.RawMessage{}
			for l, raw := range in {
				if !slices.Contains(levels, l) {
					return nil, fmt.Errorf("the levels are %s", strings.Join(levels, ", "))
				}
				cv, err := inner.check(raw)
				if err != nil {
					return nil, fmt.Errorf("level %s: %w", l, err)
				}
				out[l] = cv
			}
			b, err := json.Marshal(out)
			return b, err
		},
		stricter: func(a, b json.RawMessage) (json.RawMessage, bool) {
			ma, mb := jsonMap(a), jsonMap(b)
			out := map[string]json.RawMessage{}
			for l, va := range ma {
				vb, ok := mb[l]
				if !ok {
					out[l] = va
					continue
				}
				w, ok := inner.stricter(va, vb)
				if !ok {
					return nil, false
				}
				out[l] = w
			}
			for l, vb := range mb {
				if _, ok := ma[l]; !ok {
					out[l] = vb
				}
			}
			b, err := json.Marshal(out)
			if err != nil {
				return nil, false
			}
			return b, true
		},
	}
}

// approvalsKind is review.approvals_required: a number, or a number for each level; more approvals
// is the stricter.
var approvalsKind = kind{
	name: "approvals",
	check: func(v json.RawMessage) (json.RawMessage, error) {
		if n, err := asInt(v); err == nil {
			if n < 1 || n > 10 {
				return nil, fmt.Errorf("1 to 10 approvals")
			}
			return mustJSON(n), nil
		}
		return levelsKind(intKind(true)).check(v)
	},
	stricter: levelsKind(intKind(true)).stricter,
}

// ruleNameRe is what a review rule may be called, the entitlements' own shape.
var ruleNameRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,39}$`)

// ruleTagRe is a grouping-tag pattern a review rule may name: a tag's own shape, or the same with
// a * standing for a layer's rest (the entitlements' tag patterns again).
var (
	ruleTagRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9 _\-]*(/[a-z0-9][a-z0-9 _\-]*)*$`)
	ruleTagGlobRe = regexp.MustCompile(`^[a-z0-9][a-z0-9 _\-]*(/[a-z0-9][a-z0-9 _\-]*)*/\*$`)
)

// ruleDays are the days a review rule's window may name.
var ruleDays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// ruleWindow is one review rule's time window, checked here in the shape the secrets package
// reads it back: from and to as HH:MM (to may come before from, crossing midnight), and the days
// it holds (empty: every day).
type ruleWindow struct {
	Days []string `json:"days,omitempty"`
	From string   `json:"from"`
	To   string   `json:"to"`
}

// ruleIn is one review rule as it arrives, in the shape review.rules stores it: the decision it
// hands down (approve or deny), the levels it applies to (empty: the levels that wait — review and
// person), and the conditions, every named one of which must hold.
type ruleIn struct {
	Name          string      `json:"name"`
	Decide        string      `json:"decide"`
	Disabled      bool        `json:"disabled,omitempty"`
	Levels        []string    `json:"levels,omitempty"`
	AgentTags     []string    `json:"agent_tags,omitempty"`
	AlertService  bool        `json:"alert_service,omitempty"`
	ReasonPattern string      `json:"reason_pattern,omitempty"`
	Window        *ruleWindow `json:"window,omitempty"`
	Fields        []string    `json:"fields,omitempty"`
}

// rulesKind is review.rules: the built-in review chain's deterministic conditions, a list of
// rules. The design does not order the values (a deny wins over an approve whatever their
// places), so a more specific level's row simply replaces the one above it.
var rulesKind = kind{
	name: "rules",
	check: func(v json.RawMessage) (json.RawMessage, error) {
		var in []ruleIn
		if err := json.Unmarshal(v, &in); err != nil {
			return nil, fmt.Errorf("a list of rules")
		}
		seen := map[string]bool{}
		for i, r := range in {
			what := fmt.Sprintf("rule %d", i)
			if r.Name != "" {
				what = r.Name
			}
			if !ruleNameRe.MatchString(r.Name) {
				return nil, fmt.Errorf("%s: a rule's name is 2 to 40 lower-case letters, digits, dots, dashes and underscores", what)
			}
			if seen[r.Name] {
				return nil, fmt.Errorf("%s: two rules of one name", r.Name)
			}
			seen[r.Name] = true
			if r.Decide != "approve" && r.Decide != "deny" {
				return nil, fmt.Errorf("%s: a rule decides approve or deny", r.Name)
			}
			for _, l := range r.Levels {
				if l != "review" && l != "person" {
					return nil, fmt.Errorf("%s: a rule's levels are review and person (the levels that wait)", r.Name)
				}
			}
			for _, t := range r.AgentTags {
				if !ruleTagRe.MatchString(t) && !ruleTagGlobRe.MatchString(t) {
					return nil, fmt.Errorf("%s: the agent tag %q is not lower-case and layered with / (a * may stand for a layer's rest)", r.Name, t)
				}
			}
			if r.ReasonPattern != "" {
				if _, err := regexp.Compile(r.ReasonPattern); err != nil {
					return nil, fmt.Errorf("%s: the reason pattern does not compile: %w", r.Name, err)
				}
			}
			if r.Window != nil {
				hhmm := func(s string) bool {
					h, m, ok := strings.Cut(s, ":")
					if !ok || len(h) != 2 || len(m) != 2 {
						return false
					}
					hh, err1 := strconv.Atoi(h)
					mm, err2 := strconv.Atoi(m)
					return err1 == nil && err2 == nil && hh >= 0 && hh <= 23 && mm >= 0 && mm <= 59
				}
				if !hhmm(r.Window.From) || !hhmm(r.Window.To) || r.Window.From == r.Window.To {
					return nil, fmt.Errorf("%s: a window's from and to are HH:MM and differ", r.Name)
				}
				for _, d := range r.Window.Days {
					if !slices.Contains(ruleDays, d) {
						return nil, fmt.Errorf("%s: a window's days are %s", r.Name, strings.Join(ruleDays, ", "))
					}
				}
			}
			for _, f := range r.Fields {
				if strings.TrimSpace(f) == "" {
					return nil, fmt.Errorf("%s: a rule's fields have names", r.Name)
				}
			}
		}
		if in == nil {
			in = []ruleIn{}
		}
		return mustJSON(in), nil
	},
}

// reasonKind is use.reason: required or optional, and the shortest it may be.
var reasonKind = kind{
	name: "reason",
	check: func(v json.RawMessage) (json.RawMessage, error) {
		var in struct {
			Mode      string `json:"mode"`
			MinLength *int   `json:"min_length"`
		}
		if err := json.Unmarshal(v, &in); err != nil || in.Mode == "" {
			return nil, fmt.Errorf(`an object {"mode": "required" or "optional", "min_length": n}`)
		}
		if in.Mode != "required" && in.Mode != "optional" {
			return nil, fmt.Errorf(`mode is "required" or "optional"`)
		}
		if in.MinLength == nil || *in.MinLength < 0 || *in.MinLength > 4096 {
			return nil, fmt.Errorf("min_length is 0 or more")
		}
		return mustJSON(map[string]any{"mode": in.Mode, "min_length": *in.MinLength}), nil
	},
	stricter: func(a, b json.RawMessage) (json.RawMessage, bool) {
		ma, mb := jsonMap(a), jsonMap(b)
		mode := "optional"
		if w, ok := ma["mode"]; ok && isWord(w, "required") {
			mode = "required"
		}
		if w, ok := mb["mode"]; ok && isWord(w, "required") {
			mode = "required"
		}
		n := maxJSON(jsonInt(ma["min_length"]), jsonInt(mb["min_length"]))
		return mustJSON(map[string]any{"mode": mode, "min_length": n}), true
	},
}

// ParseDuration reads a duration the way the design writes its defaults: Go's syntax plus a d unit
// for days, so "365d" is 365 days and "1d12h" a day and a half. Negative durations are an error
// here; settings do not take them.
func ParseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var total time.Duration
	rest := s
	for {
		i := strings.IndexByte(rest, 'd')
		if i < 0 {
			break
		}
		// the number ending at this d: digits and a dot, with an optional sign Go's own syntax took
		j := i
		for j > 0 && (rest[j-1] >= '0' && rest[j-1] <= '9' || rest[j-1] == '.') {
			j--
		}
		if j == i {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		days, err := strconv.ParseFloat(rest[j:i], 64)
		if err != nil {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		total += time.Duration(days * 24 * float64(time.Hour))
		rest = rest[:j] + rest[i+1:]
	}
	if rest != "" {
		d, err := time.ParseDuration(rest)
		if err != nil {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		total += d
	}
	if total < 0 {
		return 0, fmt.Errorf("negative duration %q", s)
	}
	return total, nil
}

// ---- small JSON helpers, so a value is handled only as it is stored ----

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func asInt(v json.RawMessage) (int64, error) {
	var n int64
	if err := json.Unmarshal(v, &n); err != nil {
		var f float64
		if err2 := json.Unmarshal(v, &f); err2 != nil || f != math.Trunc(f) {
			return 0, fmt.Errorf("not a whole number")
		}
		n = int64(f)
	}
	return n, nil
}

func jsonInt(v json.RawMessage) int64 {
	n, _ := asInt(v)
	return n
}

func maxJSON(x, y int64) int64 {
	if x >= y {
		return x
	}
	return y
}

func jsonString(v json.RawMessage) string {
	var s string
	_ = json.Unmarshal(v, &s)
	return s
}

func isWord(v json.RawMessage, word string) bool {
	var s string
	return json.Unmarshal(v, &s) == nil && s == word
}

func jsonBool(v json.RawMessage) bool {
	var b bool
	_ = json.Unmarshal(v, &b)
	return b
}

func jsonStringList(v json.RawMessage) []string {
	var out []string
	_ = json.Unmarshal(v, &out)
	return out
}

func jsonMap(v json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	_ = json.Unmarshal(v, &out)
	return out
}

// countOrInf reads a count that may be the word unlimited as +Inf: no bound is the loosest count.
func countOrInf(v json.RawMessage) float64 {
	if isWord(v, "unlimited") {
		return math.Inf(1)
	}
	return float64(jsonInt(v))
}

// durationOrInf reads a duration that may be the words unlimited or never, as +Inf: both mean no
// bound, and no bound is the loosest value there is.
func durationOrInf(v json.RawMessage) float64 {
	s := jsonString(v)
	if s == "unlimited" || s == "never" {
		return math.Inf(1)
	}
	d, err := ParseDuration(s)
	if err != nil {
		return math.Inf(1)
	}
	return float64(d)
}
