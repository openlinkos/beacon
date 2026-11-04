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
		// ---- Requests and grants (the plan's 申请与授权 table)
		"request.default_uses":        `"unlimited"`,                                           // 不限
		"request.max_uses":            `"unlimited"`,                                           // 不限
		"request.default_duration":    `{"auto":"never","review":"30d","person":"7d"}`,         // auto: 不过期; review: 30 天; person: 7 天
		"request.max_duration":        `"unlimited"`,                                           // 不限
		"request.reason_min_length":   `10`,                                                    // 10
		"use.reason":                  `{"mode":"required","min_length":2}`,                    // 必填，最短 2
		"request.pending_ttl":         `"24h"`,                                                 // 24 小时
		"request.on_pending_expiry":   `"expire"`,                                              // expire
		"grant.renewal":               `{"auto":"auto","review":"request","person":"request"}`, // auto 级 auto；其余 request
		"grant.expiry_notice":         `"3d"`,                                                  // 提前 3 天（短于 1 天的不提醒）
		"grant.on_policy_tightened":   `"revoke"`,                                              // revoke
		"grant.on_owner_access_lost":  `"revoke"`,                                              // revoke
		"grant.on_item_changed":       `"notify_holders"`,                                      // notify_holders
		"grant.alert_grace":           `"15m"`,                                                 // 15 分钟
		"agents.default_entitlements": `[]`,                                                    // 空

		// ---- Review (审核)
		"review.chain":              `["rules","agents","people"]`, // [rules, agents, people]
		"review.agent.may_approve":  `["review"]`,                  // [review]
		"review.agent.max_uses":     `"unlimited"`,                 // 不限
		"review.agent.max_duration": `"unlimited"`,                 // 不限
		"review.agent.timeout":      `"10m"`,                       // 10 分钟
		"review.agent.on_timeout":   `"next"`,                      // next
		"review.people.timeout":     `"24h"`,                       // 24 小时
		"review.people.on_timeout":  `"expire"`,                    // expire
		"review.approvals_required": `1`,                           // 1（每个级别可分别设）
		"review.separation":         `"requester"`,                 // requester
		"review.notify_decisions":   `["feishu"]`,                  // 飞书

		// ---- Use, rate limits and anomalies (取用、限流、异常)
		"rate.per_agent_per_minute": `600`,        // 600
		"rate.on_exceed":            `"throttle"`, // throttle
		"anomaly.rules": `{"first_collection_use":true,"usage_spike":{"enabled":true,"times_mean_7d":5},` +
			`"denial_spike":true,"signature_failure":true,"sealed_state":true}`, // 6.7 的五条，全开（用量取 7 天均值的 5 倍）
		"anomaly.on_detect":     `"notify"`,                       // notify
		"otp.seed_to_agents":    `false`,                          // false
		"reveal.reauth_window":  `"5m"`,                           // 5 分钟
		"scrub.encodings":       `["plain","base64","url","hex"]`, // 原值、base64、URL 编码、十六进制
		"http.allow_private":    `false`,                          // false
		"http.follow_redirects": `"same_host"`,                    // same_host
		"file.on_change":        `"deliver"`,                      // deliver
		"sealed.on_request":     `"refuse"`,                       // refuse

		// ---- Retention (保留期)
		"retention.audit":           `"365d"`,                            // 365 天
		"retention.uses":            `"365d"`,                            // 365 天
		"retention.requests":        `"365d"`,                            // 365 天
		"retention.item_versions":   `{"keep":"365d","min_versions":10}`, // 365 天，另外始终保留最近 10 个版本
		"retention.trash":           `"30d"`,                             // 30 天
		"audit.checkpoint_interval": `"24h"`,                             // 24 小时
		"audit.checkpoint_publish":  `["backup","feishu"]`,               // 备份；配了飞书就同时发飞书
		"audit.export":              `null`,                              // 无

		// ---- Import (导入)
		"import.levels.by_source": `{"default":"review","1password:personal":"person","1password:private":"person","chrome":"person"}`, // 1Password Personal、Private → person；Chrome → person；其余 vault → review
		"import.levels.by_category": `{"credit_card":"never","bank_account":"never","identity":"never","passport":"never","driver_license":"never",` +
			`"social_security_number":"never","medical_record":"never","membership":"never","reward_program":"never"}`, // 证件和金融类 → never
		"import.on_conflict":   `"ask"`,    // ask
		"attachments.max_size": `10485760`, // 10 MB

		// ---- Backup (§11): schedule, retention, checks, retries, failure, heartbeat, pg_dump
		"backup.schedule.database":   `"1h"`,    // 数据库每小时
		"backup.schedule.recordings": `"24h"`,   // 录像和配置每天
		"backup.retention.hourly":    `24`,      // 每小时留 24 份
		"backup.retention.daily":     `14`,      // 每天 14 份
		"backup.retention.weekly":    `8`,       // 每周 8 份
		"backup.retention.monthly":   `12`,      // 每月 12 份
		"backup.verify.integrity":    `true`,    // 加密前 integrity_check（开）
		"backup.verify.readback":     `true`,    // 上传后回读核对（都开）
		"backup.retries":             `3`,       // 重试 3 次，间隔递增
		"backup.on_failure":          `"alert"`, // 默认只告警
		"backup.heartbeat":           `"2h"`,    // 心跳最长间隔 2 小时
		"backup.pg_dump_daily":       `false`,   // 每天一份 pg_dump 进加密备份（关）

		// ---- Storage (§10.3): the lease values the design says are configurable. The driver, the
		// DSN and the SQLite and PostgreSQL tuning are read before the database opens, so they live
		// in the configuration file; the lease loop reads from here.
		"storage.lease.renew":   `"5s"`,  // 主每 5 秒续一次
		"storage.lease.expires": `"15s"`, // 15 秒过期
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
