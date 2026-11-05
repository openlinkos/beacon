package settings

import (
	"encoding/json"
	"testing"
	"time"
)

// The plan's table of settings and their out-of-the-box defaults, transcribed key for key and
// value for value. The test holds its own copy, spelled out, so it asserts the registered table
// rather than reading it back — a key the plan has and the code lacks fails here, and so does a
// default that differs.
//
// Where the plan writes a default in words ("30 days", "10 MB", "required, at least 2"), the
// stored value is the same thing in the setting's own shape, and the comment says the words.
func TestDefaultsMatchThePlan(t *testing.T) {
	plan := map[string]string{
		// ---- Requests and grants (the plan's requests-and-grants table)
		"request.default_uses":        `"unlimited"`,                                           // no limit
		"request.max_uses":            `"unlimited"`,                                           // no limit
		"request.default_duration":    `{"auto":"never","review":"30d","person":"7d"}`,         // auto: no expiry; review: 30 days; person: 7 days
		"request.max_duration":        `"unlimited"`,                                           // no limit
		"request.reason_min_length":   `10`,                                                    // 10
		"use.reason":                  `{"mode":"required","min_length":2}`,                    // required, at least 2 characters
		"request.pending_ttl":         `"24h"`,                                                 // 24 hours
		"request.on_pending_expiry":   `"expire"`,                                              // expire
		"grant.renewal":               `{"auto":"auto","review":"request","person":"request"}`, // auto renews itself; the rest ask
		"grant.expiry_notice":         `"3d"`,                                                  // 3 days ahead (grants under a day are not told)
		"grant.on_policy_tightened":   `"revoke"`,                                              // revoke
		"grant.on_owner_access_lost":  `"revoke"`,                                              // revoke
		"grant.on_item_changed":       `"notify_holders"`,                                      // notify_holders
		"grant.alert_grace":           `"15m"`,                                                 // 15 minutes
		"agents.default_entitlements": `[]`,                                                    // empty

		// ---- Review (the plan's review table)
		"review.chain":              `["rules","agents","people"]`, // [rules, agents, people]
		"review.agent.may_approve":  `["review"]`,                  // [review]
		"review.agent.max_uses":     `"unlimited"`,                 // no limit
		"review.agent.max_duration": `"unlimited"`,                 // no limit
		"review.agent.timeout":      `"10m"`,                       // 10 minutes
		"review.agent.on_timeout":   `"next"`,                      // next
		"review.people.timeout":     `"24h"`,                       // 24 hours
		"review.people.on_timeout":  `"expire"`,                    // expire
		"review.approvals_required": `1`,                           // 1 (settable per level)
		"review.separation":         `"requester"`,                 // requester
		"review.notify_decisions":   `["feishu"]`,                  // Feishu
		"review.rules":              `[]`,                          // none (the plan's 6.2: the rules are configurable; none are on out of the box)

		// ---- Use, rate limits and anomalies (the plan's use table)
		"rate.per_agent_per_minute": `600`,        // 600
		"rate.on_exceed":            `"throttle"`, // throttle
		"anomaly.rules": `{"first_collection_use":true,"usage_spike":{"enabled":true,"times_mean_7d":5},` +
			`"denial_spike":true,"signature_failure":true,"sealed_state":true}`, // the plan's five rules, all on (usage at 5 times the 7-day mean)
		"anomaly.on_detect":     `"notify"`,                       // notify
		"otp.seed_to_agents":    `false`,                          // false
		"reveal.reauth_window":  `"5m"`,                           // 5 minutes
		"scrub.encodings":       `["plain","base64","url","hex"]`, // the value itself, base64, URL encoding, hexadecimal
		"http.allow_private":    `false`,                          // false
		"http.follow_redirects": `"same_host"`,                    // same_host
		"file.on_change":        `"deliver"`,                      // deliver
		"sealed.on_request":     `"refuse"`,                       // refuse

		// ---- Retention (the plan's retention table)
		"retention.audit":           `"365d"`,                            // 365 days
		"retention.uses":            `"365d"`,                            // 365 days
		"retention.requests":        `"365d"`,                            // 365 days
		"retention.item_versions":   `{"keep":"365d","min_versions":10}`, // 365 days, plus the latest 10 versions whatever their age
		"retention.trash":           `"30d"`,                             // 30 days
		"audit.checkpoint_interval": `"24h"`,                             // 24 hours
		"audit.checkpoint_publish":  `["backup","feishu"]`,               // the backup; Feishu too, when configured
		"audit.export":              `null`,                              // none

		// ---- Import (the plan's import table)
		"import.levels.by_source": `{"default":"review","1password:personal":"person","1password:private":"person","chrome":"person"}`, // 1Password Personal and Private -> person; Chrome -> person; every other vault -> review
		"import.levels.by_category": `{"credit_card":"never","bank_account":"never","identity":"never","passport":"never","driver_license":"never",` +
			`"social_security_number":"never","medical_record":"never","membership":"never","reward_program":"never"}`, // the identity and finance categories -> never
		"import.on_conflict":   `"ask"`,    // ask
		"attachments.max_size": `10485760`, // 10 MB

		// ---- Backup (§11): schedule, retention, checks, retries, failure, heartbeat, pg_dump
		"backup.schedule.database":   `"1h"`,    // the database, hourly
		"backup.schedule.recordings": `"24h"`,   // recordings and the configuration, daily
		"backup.retention.hourly":    `24`,      // 24 hourly copies kept
		"backup.retention.daily":     `14`,      // 14 daily copies kept
		"backup.retention.weekly":    `8`,       // 8 weekly copies kept
		"backup.retention.monthly":   `12`,      // 12 monthly copies kept
		"backup.verify.integrity":    `true`,    // integrity_check before encrypting (on)
		"backup.verify.readback":     `true`,    // read back after uploading (both on)
		"backup.retries":             `3`,       // 3 retries, the wait growing
		"backup.on_failure":          `"alert"`, // alert only, by default
		"backup.heartbeat":           `"2h"`,    // 2 hours between heartbeats at most
		"backup.pg_dump_daily":       `false`,   // a daily pg_dump inside the encrypted backup (off)

		// ---- Storage (§10.3): the lease values the design says are configurable. The driver, the
		// DSN and the SQLite and PostgreSQL tuning are read before the database opens, so they live
		// in the configuration file; the lease loop reads from here.
		"storage.lease.renew":   `"5s"`,  // the primary renews every 5 seconds
		"storage.lease.expires": `"15s"`, // the lease lasts 15 seconds
	}
	for key, want := range plan {
		d, ok := Definitions[key]
		if !ok {
			t.Errorf("%s: the plan has it, the code does not", key)
			continue
		}
		if got := string(d.Default); got != want {
			t.Errorf("%s: default is %s, the plan says %s", key, got, want)
		}
	}
	for key := range Definitions {
		if _, ok := plan[key]; !ok {
			t.Errorf("%s: the code has it, the plan does not", key)
		}
	}
}

// Every default fits its own kind (also checked at registration, which panics; this keeps the
// failure a test failure and not a stop).
func TestDefaultsFit(t *testing.T) {
	for _, d := range Ordered() {
		if _, err := d.canonical(d.Default); err != nil {
			t.Errorf("%s: its default does not fit: %v", d.Key, err)
		}
	}
}

// Every definition says what it is and which group it is in, for the console's page.
func TestDefinitionsAreDescribed(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range Ordered() {
		if d.What == "" || len(d.What) > 160 {
			t.Errorf("%s: what it is should be one line", d.Key)
		}
		if d.Group == "" || !seen[d.Group] && !contains(Groups, d.Group) {
			t.Errorf("%s: group %q is not one of the groups", d.Key, d.Group)
		}
		seen[d.Group] = true
	}
	for _, g := range Groups {
		if !seen[g] {
			t.Errorf("group %s holds nothing; drop it or fill it", g)
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Values that do not fit are refused, with the setting's own words.
func TestValuesAreChecked(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"request.max_uses", `"five"`},
		{"request.max_uses", `-3`},
		{"request.pending_ttl", `"tomorrow"`},
		{"request.default_duration", `{"guest":"30d"}`}, // a level that is not one
		{"grant.alert_grace", `"unlimited"`},            // a grace is a duration, not a bound
		{"request.max_duration", `"never"`},             // a bound is unlimited, not never
		{"request.on_pending_expiry", `"wait"`},         // not one of the three
		{"review.chain", `["rules", "birds"]`},
		{"scrub.encodings", `["plain", "rot13"]`},
		{"scrub.encodings", `["plain", "plain"]`},
		{"otp.seed_to_agents", `"no"`},
		{"anomaly.rules", `{"first_collection_use": true, "mistypo": true}`},
		{"anomaly.rules", `{"usage_spike": {"enabled": true}}`},
		{"use.reason", `{"mode": "sometimes"}`},
		{"review.approvals_required", `0`},
		{"review.approvals_required", `{"auto": 2, "guest": 1}`},
		{"review.rules", `[{"name": "r", "decide": "maybe"}]`},
		{"review.rules", `[{"name": "oncall-ok", "decide": "approve", "window": {"from": "9:00", "to": "17:30"}}]`},
		{"review.rules", `[{"name": "oncall-ok", "decide": "approve", "reason_pattern": "("}]`},
		{"review.rules", `[{"name": "oncall-ok", "decide": "deny", "agent_tags": ["OnCall"]}]`},
		{"retention.item_versions", `{"keep": "365d"}`},
		{"audit.export", `"email"`},
		{"import.levels.by_source", `{"chrome": "sometimes"}`},
		{"attachments.max_size", `"10 MB"`},
	} {
		d, ok := Definitions[tc.key]
		if !ok {
			t.Errorf("%s: not a setting", tc.key)
			continue
		}
		if _, err := d.canonical(json.RawMessage(tc.value)); err == nil {
			t.Errorf("%s: %s should not fit, and does", tc.key, tc.value)
		}
	}
}

// Values are stored as they are checked, so two writes of one value hold the same bytes and the
// signature covers a fixed form.
func TestValuesAreCanonical(t *testing.T) {
	for _, tc := range []struct{ key, in, want string }{
		{"request.max_uses", "50", "50"},
		{"request.max_uses", `"unlimited"`, `"unlimited"`},
		{"request.pending_ttl", `"48h"`, `"48h"`},
		{"otp.seed_to_agents", "true", "true"},
		{"scrub.encodings", `["hex","base64"]`, `["hex","base64"]`},
		{"use.reason", `{"mode":"optional","min_length":2}`, `{"min_length":2,"mode":"optional"}`},
		{"request.default_duration", `{"review":"30d","auto":"never","person":"7d"}`, `{"auto":"never","person":"7d","review":"30d"}`},
	} {
		d := Definitions[tc.key]
		got, err := d.canonical(json.RawMessage(tc.in))
		if err != nil {
			t.Errorf("%s: %v", tc.key, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("%s: stored as %s, want %s", tc.key, got, tc.want)
		}
	}
}

// Durations read the way the plan writes them: Go's syntax, and days.
func TestParseDuration(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"15m", 15 * 60},
		{"24h", 24 * 3600},
		{"365d", 365 * 24 * 3600},
		{"1d12h", 36 * 3600},
		{"30s", 30},
	} {
		d, err := ParseDuration(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if int64(d/time.Second) != tc.want {
			t.Errorf("%s: %d seconds, want %d", tc.in, int64(d/time.Second), tc.want)
		}
	}
	for _, bad := range []string{"", "-5m", "soon", "5x"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("%s: should not parse", bad)
		}
	}
}
