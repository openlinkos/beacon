package settings

import (
	"encoding/json"
	"slices"
	"testing"
)

// jsonAt picks one key out of a stored object value.
func jsonAt(v json.RawMessage, key string) json.RawMessage {
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(v, &m)
	return m[key]
}

// The resolution the acceptance asks for, level by level: each more specific scope wins, a
// collection over the system, a tag over the collection, an entitlement over the tags, a request
// over the entitlement — and the levels list shows where the value was overridden.
func TestResolutionLevelByLevel(t *testing.T) {
	e := newEnv(t)
	set := func(scope, target, value string) {
		t.Helper()
		if _, err := e.s.Set(ctx, api, scope, target, "request.max_uses", raw(value), why, 0); err != nil {
			t.Fatal(err)
		}
	}
	v, ls, _ := e.s.Value("request.max_uses", Selector{Collection: "infra", Tags: []string{"env/prod"},
		Entitlement: "oncall-dns", Request: "r-1"})
	if v.From != "default" || string(v.Value) != `"unlimited"` || len(ls) != 1 {
		t.Fatalf("nothing set: %+v %+v", v, ls)
	}

	set(ScopeSystem, "", "900")
	v, ls, _ = e.s.Value("request.max_uses", Selector{})
	if v.From != ScopeSystem || string(v.Value) != "900" || len(ls) != 2 {
		t.Fatalf("system: %+v %+v", v, ls)
	}

	set(ScopeCollection, "infra", "400")
	v, ls, _ = e.s.Value("request.max_uses", Selector{Collection: "infra"})
	if v.From != ScopeCollection || v.Target != "infra" || string(v.Value) != "400" || len(ls) != 3 {
		t.Fatalf("collection: %+v %+v", v, ls)
	}
	// another collection still sees the system's value
	if v, ls, _ = e.s.Value("request.max_uses", Selector{Collection: "shared"}); v.From != ScopeSystem || len(ls) != 2 {
		t.Fatalf("another collection: %+v %+v", v, ls)
	}

	set(ScopeTag, "env/prod", "300")
	v, ls, _ = e.s.Value("request.max_uses", Selector{Collection: "infra", Tags: []string{"env/prod"}})
	if v.From != ScopeTag || v.Target != "env/prod" || string(v.Value) != "300" || len(ls) != 4 {
		t.Fatalf("tag: %+v %+v", v, ls)
	}
	// an item without the tag keeps the collection's value
	if v, _, _ = e.s.Value("request.max_uses", Selector{Collection: "infra"}); v.From != ScopeCollection {
		t.Fatalf("no tag: %+v", v)
	}

	set(ScopeEntitlement, "oncall-dns", "200")
	v, ls, _ = e.s.Value("request.max_uses", Selector{Collection: "infra", Tags: []string{"env/prod"},
		Entitlement: "oncall-dns"})
	if v.From != ScopeEntitlement || v.Target != "oncall-dns" || string(v.Value) != "200" || len(ls) != 5 {
		t.Fatalf("entitlement: %+v %+v", v, ls)
	}

	// the request wins where it tightens
	if _, err := e.s.Set(ctx, api, ScopeRequest, "r-1", "request.max_uses", raw("100"), why, 0); err != nil {
		t.Fatal(err)
	}
	v, ls, _ = e.s.Value("request.max_uses", Selector{Collection: "infra", Tags: []string{"env/prod"},
		Entitlement: "oncall-dns", Request: "r-1"})
	if v.From != ScopeRequest || string(v.Value) != "100" || len(ls) != 6 {
		t.Fatalf("request: %+v %+v", v, ls)
	}
	// another request is untouched by r-1's row
	if v, _, _ = e.s.Value("request.max_uses", Selector{Collection: "infra", Tags: []string{"env/prod"},
		Entitlement: "oncall-dns", Request: "r-2"}); v.From != ScopeEntitlement {
		t.Fatalf("another request: %+v", v)
	}
}

// Several tags cover one item and each overrides the same setting: counts and durations take the
// smaller, consequences the more conservative of deny, expire, pass on, wait, approve; booleans take
// the disabling side where the design's default is the safe one, and what the design does not
// order falls to the first tag in name order.
func TestSeveralTagsTakeTheStricter(t *testing.T) {
	e := newEnv(t)
	tag := func(tag, key, value string) {
		t.Helper()
		if _, err := e.s.Set(ctx, api, ScopeTag, tag, key, raw(value), why, 0); err != nil {
			t.Fatal(err)
		}
	}

	// counts take the smaller
	tag("provider/example", "request.max_uses", "50")
	tag("service/dns", "request.max_uses", "30")
	v, _, _ := e.s.Value("request.max_uses", Selector{Tags: []string{"service/dns", "provider/example"}})
	if string(v.Value) != "30" || v.Target != "service/dns" {
		t.Fatalf("counts: %+v", v)
	}
	// unlimited is the loosest bound there is
	tag("env/prod", "request.max_uses", `"unlimited"`)
	v, _, _ = e.s.Value("request.max_uses", Selector{Tags: []string{"env/prod", "service/dns"}})
	if string(v.Value) != "30" {
		t.Fatalf("unlimited against a number: %+v", v)
	}

	// durations take the smaller
	tag("provider/example", "grant.alert_grace", `"30m"`)
	tag("service/dns", "grant.alert_grace", `"10m"`)
	v, _, _ = e.s.Value("grant.alert_grace", Selector{Tags: []string{"provider/example", "service/dns"}})
	if string(v.Value) != `"10m"` {
		t.Fatalf("durations: %+v", v)
	}

	// consequences take the more conservative: deny over expire over approve
	tag("provider/example", "request.on_pending_expiry", `"approve"`)
	tag("service/dns", "request.on_pending_expiry", `"expire"`)
	v, _, _ = e.s.Value("request.on_pending_expiry", Selector{Tags: []string{"provider/example", "service/dns"}})
	if string(v.Value) != `"expire"` {
		t.Fatalf("consequences: %+v", v)
	}
	tag("env/prod", "request.on_pending_expiry", `"deny"`)
	v, _, _ = e.s.Value("request.on_pending_expiry", Selector{Tags: []string{"provider/example", "service/dns", "env/prod"}})
	if string(v.Value) != `"deny"` {
		t.Fatalf("consequences with three: %+v", v)
	}
	// the agent-timeout order runs deny, next, wait, approve
	tag("provider/example", "review.agent.on_timeout", `"wait"`)
	tag("service/dns", "review.agent.on_timeout", `"next"`)
	v, _, _ = e.s.Value("review.agent.on_timeout", Selector{Tags: []string{"provider/example", "service/dns"}})
	if string(v.Value) != `"next"` {
		t.Fatalf("agent timeouts: %+v", v)
	}

	// a bool the design wants off takes false
	tag("provider/example", "otp.seed_to_agents", "true")
	tag("service/dns", "otp.seed_to_agents", "false")
	v, _, _ = e.s.Value("otp.seed_to_agents", Selector{Tags: []string{"provider/example", "service/dns"}})
	if string(v.Value) != "false" {
		t.Fatalf("bools: %+v", v)
	}

	// scrubbing keeps every encoding either side scrubs, and a reviewer who may approve less is
	// the stricter
	tag("provider/example", "scrub.encodings", `["plain","base64"]`)
	tag("service/dns", "scrub.encodings", `["plain","url"]`)
	v, _, _ = e.s.Value("scrub.encodings", Selector{Tags: []string{"provider/example", "service/dns"}})
	got := jsonStringList(v.Value)
	slices.Sort(got)
	if !slices.Equal(got, []string{"base64", "plain", "url"}) {
		t.Fatalf("encodings: %s", v.Value)
	}
	tag("provider/example", "review.agent.may_approve", `["auto","review"]`)
	tag("service/dns", "review.agent.may_approve", `["review"]`)
	v, _, _ = e.s.Value("review.agent.may_approve", Selector{Tags: []string{"provider/example", "service/dns"}})
	if string(v.Value) != `["review"]` {
		t.Fatalf("approvals a reviewer may give: %s", v.Value)
	}

	// a setting the design does not order takes the first tag in name order
	tag("provider/example", "review.notify_decisions", `["feishu"]`)
	tag("service/dns", "review.notify_decisions", `[]`)
	v, _, _ = e.s.Value("review.notify_decisions", Selector{Tags: []string{"service/dns", "provider/example"}})
	if v.Target != "provider/example" {
		t.Fatalf("unordered: %+v", v)
	}
}

// A request may only tighten what the level above gives: a larger count, a longer duration or a
// looser consequence is clamped to the bound; a stricter one is kept; a setting the design does
// not order takes the request's value, the more specific one.
func TestRequestIsClampedToTheBound(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.Set(ctx, api, ScopeEntitlement, "oncall-dns", "request.max_uses", raw("30"), why, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Set(ctx, api, ScopeEntitlement, "oncall-dns", "request.max_duration", raw(`"7d"`), why, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Set(ctx, api, ScopeEntitlement, "oncall-dns", "request.on_pending_expiry", raw(`"deny"`), why, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Set(ctx, api, ScopeRequest, "r-9", "request.max_uses", raw("50"), why, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Set(ctx, api, ScopeRequest, "r-9", "request.max_duration", raw(`"30d"`), why, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Set(ctx, api, ScopeRequest, "r-9", "request.on_pending_expiry", raw(`"approve"`), why, 0); err == nil {
		t.Fatal("a request must not set review consequences")
	}
	sel := Selector{Entitlement: "oncall-dns", Request: "r-9"}
	v, _, _ := e.s.Value("request.max_uses", sel)
	if string(v.Value) != "30" {
		t.Fatalf("uses clamped: %+v", v)
	}
	v, _, _ = e.s.Value("request.max_duration", sel)
	if string(v.Value) != `"7d"` {
		t.Fatalf("duration clamped: %+v", v)
	}
	v, _, _ = e.s.Value("request.on_pending_expiry", sel)
	if string(v.Value) != `"deny"` {
		t.Fatalf("consequence from the entitlement: %+v", v)
	}

	// a stricter request holds; an unordered setting is not a request's to set
	if _, err := e.s.Set(ctx, api, ScopeRequest, "r-9", "request.max_uses", raw("10"), why, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Set(ctx, api, ScopeRequest, "r-9", "review.notify_decisions", raw(`[]`), why, 0); err == nil {
		t.Fatal("a request must not drop notify_decisions")
	}
	if v, _, _ = e.s.Value("request.max_uses", sel); string(v.Value) != "10" || v.From != ScopeRequest {
		t.Fatalf("stricter request: %+v", v)
	}
}

// Per-level maps compare level by level: the smaller duration at each level.
func TestLevelsMapsTakeTheStricterPerLevel(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.Set(ctx, api, ScopeTag, "env/prod", "request.default_duration",
		raw(`{"auto":"never","review":"30d","person":"7d"}`), why, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Set(ctx, api, ScopeTag, "service/dns", "request.default_duration",
		raw(`{"auto":"2d","review":"3d"}`), why, 0); err == nil {
		t.Fatal("a per-level map must name every level")
	}
	if _, err := e.s.Set(ctx, api, ScopeTag, "service/dns", "request.default_duration",
		raw(`{"auto":"2d","review":"3d","person":"1d"}`), why, 0); err != nil {
		t.Fatal(err)
	}
	v, _, _ := e.s.Value("request.default_duration", Selector{Tags: []string{"env/prod", "service/dns"}})
	for level, want := range map[string]string{"auto": `"2d"`, "review": `"3d"`, "person": `"1d"`} {
		if got := string(jsonAt(v.Value, level)); got != want {
			t.Fatalf("level %s: %s, want %s", level, got, want)
		}
	}
}
