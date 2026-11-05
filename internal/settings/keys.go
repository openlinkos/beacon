package settings

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Definitions are every setting there is, with the value it starts with. They are the design's
// list of knobs with its out-of-the-box defaults, registered as the system level: a value someone
// sets at the system scope overrides the default, and resetting the row falls back to it.
//
// Where the design orders a setting's values, the order here is that order, strictest first: it is
// what wins when tags conflict and what a request cannot loosen. Durations are written the way the
// design writes them (30d for thirty days).
type Definition struct {
	Key      string
	Group    string // requests, review, use, retention, import, backup or storage; the pages group by it
	What     string // one line: what the setting turns
	Default  json.RawMessage
	Failsafe json.RawMessage // served when the signed head is unusable or a live row does not Accept
	Access   bool            // access-control: failsafe is the strictest allowed value, never looser than Default

	kind kind
}

// Groups lists the groups in the order the console shows them.
var Groups = []string{"requests", "review", "use", "hygiene", "retention", "import", "backup", "storage"}

// Definitions, by key.
var Definitions = map[string]*Definition{}

// Ordered lists the definitions by group, in the groups' order, then by key.
func Ordered() []*Definition {
	var out []*Definition
	for _, g := range Groups {
		var in []*Definition
		for _, d := range Definitions {
			if d.Group == g {
				in = append(in, d)
			}
		}
		slices.SortFunc(in, func(a, b *Definition) int { return strings.Compare(a.Key, b.Key) })
		out = append(out, in...)
	}
	return out
}

// Get is one definition, when the key is one there is.
func Get(key string) (*Definition, bool) {
	d, ok := Definitions[key]
	return d, ok
}

// canonical fits a value to the definition's kind and returns it as it is stored.
func (d *Definition) canonical(v json.RawMessage) (json.RawMessage, error) {
	if len(v) == 0 || string(v) == "null" && d.Key != "audit.export" {
		return nil, fmt.Errorf("%w: no value", ErrBadInput)
	}
	cv, err := d.kind.check(v)
	if err != nil {
		return nil, err
	}
	if d.Key == "retention.audit" {
		if dur, err := ParseDuration(jsonString(cv)); err == nil && dur < 24*time.Hour {
			return nil, fmt.Errorf("retention.audit is at least 24h")
		}
	}
	if d.Key == "review.chain" {
		if len(jsonStringList(cv)) == 0 {
			return nil, fmt.Errorf("the review chain names at least one layer")
		}
	}
	if d.Key == "scrub.encodings" {
		if !slices.Contains(jsonStringList(cv), "plain") {
			return nil, fmt.Errorf("scrub.encodings always includes plain")
		}
	}
	if strings.HasPrefix(d.Key, "backup.retention.") {
		n, err := asInt(cv)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("keep at least one backup")
		}
	}
	return cv, nil
}

// stricter says which of two values for this setting is the stricter, when the design orders it.
func (d *Definition) stricter(a, b json.RawMessage) (json.RawMessage, bool) {
	if d.kind.stricter == nil {
		return nil, false
	}
	return d.kind.stricter(a, b)
}

// enum builds the kind of a setting whose values are words, ordered strictest first.
func enum(order ...string) kind {
	return kind{
		name: "enum",
		check: func(v json.RawMessage) (json.RawMessage, error) {
			s := jsonString(v)
			if !slices.Contains(order, s) {
				return nil, fmt.Errorf("one of %s", strings.Join(order, ", "))
			}
			return mustJSON(s), nil
		},
		stricter: func(a, b json.RawMessage) (json.RawMessage, bool) {
			ia, ib := slices.Index(order, jsonString(a)), slices.Index(order, jsonString(b))
			if ia < 0 || ib < 0 {
				return nil, false
			}
			if ia <= ib {
				return a, true
			}
			return b, true
		},
	}
}

func asRaw(v any) json.RawMessage {
	switch x := v.(type) {
	case json.RawMessage:
		return x
	case string:
		return json.RawMessage(x)
	default:
		return mustJSON(v)
	}
}

// register adds an ops setting: failsafe is the built-in default.
func register(key, group, what string, k kind, def any) {
	define(key, group, what, k, def, def, false)
}

// registerAccess adds an access-control setting: failsafe is the strictest allowed value.
func registerAccess(key, group, what string, k kind, def, failsafe any) {
	define(key, group, what, k, def, failsafe, true)
}

func define(key, group, what string, k kind, def, failsafe any, access bool) {
	d := &Definition{Key: key, Group: group, What: what, kind: k, Access: access}
	d.Default = asRaw(def)
	cv, err := d.canonical(asRaw(failsafe))
	if err != nil {
		panic("settings: failsafe for " + key + " does not fit its kind: " + err.Error())
	}
	d.Failsafe = cv
	if _, dup := Definitions[key]; dup {
		panic("settings: " + key + " registered twice")
	}
	Definitions[key] = d
}

// anomalyKind is anomaly.rules: the design's five rules, each on or off, with the threshold where
// it has one. Every key the design does not name is refused, so a typo cannot silently turn a rule
// off.
var anomalyKind = kind{
	name: "anomaly",
	check: func(v json.RawMessage) (json.RawMessage, error) {
		var in map[string]json.RawMessage
		if err := json.Unmarshal(v, &in); err != nil {
			return nil, fmt.Errorf("the rules: first_collection_use, usage_spike, denial_spike, signature_failure, sealed_state")
		}
		for _, name := range []string{"first_collection_use", "usage_spike", "denial_spike", "signature_failure", "sealed_state"} {
			if _, ok := in[name]; !ok {
				return nil, fmt.Errorf("the rules: first_collection_use, usage_spike, denial_spike, signature_failure, sealed_state")
			}
		}
		bounds := map[string]bool{"first_collection_use": true, "usage_spike": true, "denial_spike": true,
			"signature_failure": true, "sealed_state": true}
		out := map[string]json.RawMessage{}
		for name, raw := range in {
			if !bounds[name] {
				return nil, fmt.Errorf("no rule called %q", name)
			}
			if name == "usage_spike" {
				var u struct {
					Enabled     *bool    `json:"enabled"`
					TimesMean7d *float64 `json:"times_mean_7d"`
				}
				if err := json.Unmarshal(raw, &u); err != nil || u.Enabled == nil || u.TimesMean7d == nil {
					return nil, fmt.Errorf("usage_spike is {\"enabled\": bool, \"times_mean_7d\": n}")
				}
				if *u.TimesMean7d <= 0 {
					return nil, fmt.Errorf("times_mean_7d is more than 0")
				}
				out[name] = mustJSON(map[string]any{"enabled": *u.Enabled, "times_mean_7d": *u.TimesMean7d})
				continue
			}
			b, err := boolKind(tNone).check(raw)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			out[name] = b
		}
		b, err := json.Marshal(out)
		return b, err
	},
	// more rules on is the stricter
	stricter: func(a, b json.RawMessage) (json.RawMessage, bool) {
		ma, mb := jsonMap(a), jsonMap(b)
		out := map[string]json.RawMessage{}
		for n, va := range ma {
			if n == "usage_spike" {
				out[n] = va
				continue
			}
			if vb, ok := mb[n]; ok {
				out[n] = mustJSON(jsonBool(va) || jsonBool(vb))
			} else {
				out[n] = va
			}
		}
		for n, vb := range mb {
			if _, ok := ma[n]; !ok {
				out[n] = vb
			}
		}
		m, err := json.Marshal(out)
		if err != nil {
			return nil, false
		}
		return m, true
	},
}

// itemVersionsKind is retention.item_versions: how long to keep versions, and how many of the
// newest to keep whatever their age ("365 days, plus the latest 10 versions whatever their age").
var itemVersionsKind = kind{
	name: "item_versions",
	check: func(v json.RawMessage) (json.RawMessage, error) {
		var in struct {
			Keep        *string `json:"keep"`
			MinVersions *int    `json:"min_versions"`
		}
		if err := json.Unmarshal(v, &in); err != nil || in.Keep == nil || in.MinVersions == nil {
			return nil, fmt.Errorf(`{"keep": "365d", "min_versions": 10}`)
		}
		if _, err := ParseDuration(*in.Keep); err != nil {
			return nil, fmt.Errorf("keep: a duration such as 365d")
		}
		if *in.MinVersions < 0 || *in.MinVersions > 10000 {
			return nil, fmt.Errorf("min_versions is 0 to 10000")
		}
		return mustJSON(map[string]any{"keep": *in.Keep, "min_versions": *in.MinVersions}), nil
	},
	stricter: func(a, b json.RawMessage) (json.RawMessage, bool) {
		ma, mb := jsonMap(a), jsonMap(b)
		// keeping longer, and keeping more of the newest, are the stricter
		if durationOrInf(ma["keep"]) != durationOrInf(mb["keep"]) {
			if durationOrInf(ma["keep"]) > durationOrInf(mb["keep"]) {
				return a, true
			}
			return b, true
		}
		if jsonInt(ma["min_versions"]) >= jsonInt(mb["min_versions"]) {
			return a, true
		}
		return b, true
	},
}

// exportKind is audit.export: nothing (null), syslog or s3.
var exportKind = kind{
	name: "export",
	check: func(v json.RawMessage) (json.RawMessage, error) {
		if string(v) == "null" {
			return json.RawMessage("null"), nil
		}
		s := jsonString(v)
		if s != "syslog" && s != "s3" {
			return nil, fmt.Errorf("null, syslog or s3")
		}
		return mustJSON(s), nil
	},
}

// levelsMapKind builds a kind holding a map of names to approval levels (the import levels), with
// an optional default under "default".
func levelsMapKind() kind {
	return kind{
		name: "levels_map",
		check: func(v json.RawMessage) (json.RawMessage, error) {
			var in map[string]json.RawMessage
			if err := json.Unmarshal(v, &in); err != nil || len(in) == 0 {
				return nil, fmt.Errorf("an object of names to levels (auto, review, person, never)")
			}
			out := map[string]json.RawMessage{}
			for name, raw := range in {
				if name == "" || len(name) > 128 {
					return nil, fmt.Errorf("names of 1 to 128 characters")
				}
				l := jsonString(raw)
				if !slices.Contains([]string{"auto", "review", "person", "never"}, l) {
					return nil, fmt.Errorf("%s: the level is auto, review, person or never", name)
				}
				out[name] = mustJSON(l)
			}
			b, err := json.Marshal(out)
			return b, err
		},
		stricter: func(a, b json.RawMessage) (json.RawMessage, bool) {
			// the stricter level for every name both name, and a name only one side names
			order := []string{"never", "person", "review", "auto"}
			ma, mb := jsonMap(a), jsonMap(b)
			out := map[string]json.RawMessage{}
			for n, va := range ma {
				vb, ok := mb[n]
				if !ok {
					out[n] = va
					continue
				}
				ia, ib := slices.Index(order, jsonString(va)), slices.Index(order, jsonString(vb))
				if ia < 0 || ib < 0 {
					return nil, false
				}
				if ia <= ib {
					out[n] = va
				} else {
					out[n] = vb
				}
			}
			for n, vb := range mb {
				if _, ok := ma[n]; !ok {
					out[n] = vb
				}
			}
			m, err := json.Marshal(out)
			if err != nil {
				return nil, false
			}
			return m, true
		},
	}
}

// fmtDuration is not needed: the stricter of two values that both passed their kind's check is one
// of them, so the comparison returns a side, never a made-up value.

func init() {
	// ---- Requests and grants (the design's request and grant tables)
	registerAccess("request.default_uses", "requests", "the uses a grant starts with, when a request does not say", countKind(), `"unlimited"`, `1`)
	registerAccess("request.max_uses", "requests", "the most uses a request may ask for", countKind(), `"unlimited"`, `1`)
	registerAccess("request.default_duration", "requests", "how long a grant lasts, by the level it needed, when a request does not say",
		levelsKind(durationKind(false, true)), `{"auto":"never","review":"30d","person":"7d"}`, `{"auto":"7d","review":"7d","person":"7d"}`)
	registerAccess("request.max_duration", "requests", "the longest a request may ask for", durationKind(true, false), `"unlimited"`, `"7d"`)
	registerAccess("request.reason_min_length", "requests", "the shortest a request's reason may be (0: not required)", intKind(true), `10`, `10`)
	registerAccess("use.reason", "requests", "whether each use must say why, and the shortest its reason may be", reasonKind, `{"mode":"required","min_length":2}`, `{"mode":"required","min_length":2}`)
	register("request.pending_ttl", "requests", "how long a request waits for a decision", durationKind(false, false), `"24h"`)
	registerAccess("request.on_pending_expiry", "requests", "what happens to a request nobody decided", enum("deny", "expire", "approve"), `"expire"`, `"deny"`)
	registerAccess("grant.renewal", "requests", "how grants renew, by the level they needed",
		levelsKind(enum("none", "request", "auto")), `{"auto":"auto","review":"request","person":"request"}`, `{"auto":"none","review":"none","person":"none"}`)
	register("grant.expiry_notice", "requests", "how long before expiry to tell the agent and its owner (grants under a day are not told)", durationKind(false, false), `"3d"`)
	registerAccess("grant.on_policy_tightened", "requests", "what happens to grants when policy tightens around them", enum("revoke", "keep_until_expiry", "notify"), `"revoke"`, `"revoke"`)
	registerAccess("grant.on_owner_access_lost", "requests", "what happens to grants when their owner loses access", enum("revoke", "keep_until_expiry", "notify"), `"revoke"`, `"revoke"`)
	registerAccess("grant.on_item_changed", "requests", "what happens when an item's value changes under a grant", enum("notify_holders", "none"), `"notify_holders"`, `"notify_holders"`)
	register("grant.alert_grace", "requests", "how long a grant bound to an alert still works after the alert resolves", durationKind(false, false), `"15m"`)
	register("grant.unused_after", "requests", "how long a live grant may sit unused before the hygiene sweep acts", durationKind(false, false), `"30d"`)
	register("grant.on_unused", "requests", "what happens to a grant that sat unused: marked in the console, the owner told, or released", enum("auto_release", "notify", "mark"), `"mark"`)
	registerAccess("agents.default_entitlements", "requests", "the entitlement templates a new agent starts with", stringsKind(nil, stringsNone), `[]`, `[]`)

	// ---- Review (the review chain)
	registerAccess("review.chain", "review", "the review chain's layers and order", stringsKind([]string{"rules", "agents", "people"}, stringsNone), `["rules","agents","people"]`, `["rules","agents","people"]`)
	registerAccess("review.agent.may_approve", "review", "the levels a review agent may approve (person is people's by definition)", stringsKind([]string{"auto", "review"}, stringsFewer), `["review"]`, `[]`)
	registerAccess("review.agent.max_uses", "review", "the most uses a review agent may grant", countKind(), `"unlimited"`, `1`)
	registerAccess("review.agent.max_duration", "review", "the longest a review agent may grant", durationKind(true, false), `"unlimited"`, `"7d"`)
	register("review.agent.timeout", "review", "how long a review agent has to decide", durationKind(false, false), `"10m"`)
	registerAccess("review.agent.on_timeout", "review", "what happens when a review agent does not decide in time", enum("deny", "next", "wait", "approve"), `"next"`, `"deny"`)
	register("review.people.timeout", "review", "how long people have to decide", durationKind(false, false), `"24h"`)
	registerAccess("review.people.on_timeout", "review", "what happens when people do not decide in time", enum("deny", "expire", "approve"), `"expire"`, `"deny"`)
	registerAccess("review.approvals_required", "review", "approvals a request needs (a number, or one for each level)", approvalsKind, `1`, `10`)
	registerAccess("review.separation", "review", "who may not review: the requester, or anyone of the requester's owner", enum("owner", "requester"), `"requester"`, `"owner"`)
	register("review.notify_decisions", "review", "where a review agent's decisions are pushed", stringsKind(nil, stringsNone), `["feishu"]`)

	// ---- Use, rate limits and anomalies
	registerAccess("rate.per_agent_per_minute", "use", "the uses an agent may make each minute", intKind(false), `600`, `1`)
	registerAccess("rate.on_exceed", "use", "what happens when an agent passes its rate", enum("throttle", "alert"), `"throttle"`, `"throttle"`)
	registerAccess("anomaly.rules", "use", "the anomaly rules, each on or off (usage_spike carries its threshold)", anomalyKind,
		`{"first_collection_use":true,"usage_spike":{"enabled":true,"times_mean_7d":5},"denial_spike":true,"signature_failure":true,"sealed_state":true}`,
		`{"first_collection_use":true,"usage_spike":{"enabled":true,"times_mean_7d":5},"denial_spike":true,"signature_failure":true,"sealed_state":true}`)
	registerAccess("anomaly.on_detect", "use", "what happens when an anomaly rule fires", enum("suspend_agent", "notify"), `"notify"`, `"suspend_agent"`)
	registerAccess("otp.seed_to_agents", "use", "whether agents may see the seed of a one-time code, not only the code", boolKind(tFalse), `false`, `false`)
	register("reveal.reauth_window", "use", "how long after re-entering a password a person may view values again", durationKind(false, false), `"5m"`)
	registerAccess("scrub.encodings", "use", "the encodings scrubbed from output, recordings and transcripts", stringsKind([]string{"plain", "base64", "url", "hex"}, stringsMore), `["plain","base64","url","hex"]`, `["plain","base64","url","hex"]`)
	registerAccess("http.allow_private", "use", "whether http_request may be sent to private addresses", boolKind(tFalse), `false`, `false`)
	registerAccess("http.follow_redirects", "use", "the redirects http_request may follow", enum("none", "same_host", "any_bound"), `"same_host"`, `"none"`)
	registerAccess("file.on_change", "use", "what happens to a delivered file when its item's value changes", enum("deliver", "next_window", "notify"), `"deliver"`, `"deliver"`)
	registerAccess("sealed.on_request", "use", "what happens to requests while the vault is sealed", enum("refuse", "queue"), `"refuse"`, `"refuse"`)

	// ---- Hygiene (the rotation notices, the password health report, the idle grants)
	register("hygiene.hibp_check", "hygiene", "whether the health report also asks the leak corpus (HIBP's k-anonymity interface); it sends the first five characters of each password's hash outward", boolKind(tFalse), `false`)
	register("hygiene.weak_min_length", "hygiene", "the shortest a password may be before the report calls it weak", intKind(true), `8`)
	register("hygiene.password_age", "hygiene", "how old a password may be before the report calls it old", durationKind(false, false), `"90d"`)

	// ---- Retention
	register("retention.audit", "retention", "how long audit events are kept", durationKind(false, false), `"365d"`)
	register("retention.uses", "retention", "how long use records are kept", durationKind(false, false), `"365d"`)
	register("retention.requests", "retention", "how long requests are kept", durationKind(false, false), `"365d"`)
	register("retention.item_versions", "retention", "how long item versions are kept, and how many of the newest whatever their age", itemVersionsKind, `{"keep":"365d","min_versions":10}`)
	register("retention.trash", "retention", "how long removed items stay in the trash", durationKind(false, false), `"30d"`)
	register("audit.checkpoint_interval", "retention", "how often the audit chain is checkpointed and signed", durationKind(false, false), `"24h"`)
	register("audit.checkpoint_publish", "retention", "where checkpoints are published", stringsKind([]string{"backup", "feishu"}, stringsNone), `["backup","feishu"]`)
	register("audit.export", "retention", "where audit events are streamed to as well (null: nowhere)", exportKind, `null`)

	// ---- Import
	registerAccess("import.levels.by_source", "import", "the approval level items start with, by where they came from (default for any other source)",
		levelsMapKind(), `{"default":"review","1password:personal":"person","1password:private":"person","chrome":"person"}`,
		`{"default":"never","1password:personal":"never","1password:private":"never","chrome":"never"}`)
	registerAccess("import.levels.by_category", "import", "the approval level items start with, by category (the identity and finance categories start never)",
		levelsMapKind(), `{"credit_card":"never","bank_account":"never","identity":"never","passport":"never","driver_license":"never","social_security_number":"never","medical_record":"never","membership":"never","reward_program":"never"}`,
		`{"credit_card":"never","bank_account":"never","identity":"never","passport":"never","driver_license":"never","social_security_number":"never","medical_record":"never","membership":"never","reward_program":"never"}`)
	registerAccess("import.on_conflict", "import", "what happens when an import meets an item that is already there", enum("ask", "new_version", "skip", "keep_both"), `"ask"`, `"ask"`)
	register("attachments.max_size", "import", "the largest an attachment may be, in bytes (10 MB)", intKind(false), `10485760`)

	// ---- Backup (the design's backup table). Recipients and destination credentials are read at
	// start, from the secrets directory, so a backup can be restored onto a database that is empty;
	// everything that can change at run time is here.
	register("backup.schedule.database", "backup", "how often the database is backed up", durationKind(false, false), `"1h"`)
	register("backup.schedule.recordings", "backup", "how often recordings and the configuration directory are backed up", durationKind(false, false), `"24h"`)
	register("backup.retention.hourly", "backup", "hourly backups kept", intKind(false), `24`)
	register("backup.retention.daily", "backup", "daily backups kept", intKind(false), `14`)
	register("backup.retention.weekly", "backup", "weekly backups kept", intKind(false), `8`)
	register("backup.retention.monthly", "backup", "monthly backups kept", intKind(false), `12`)
	register("backup.verify.integrity", "backup", "check the database's integrity before encrypting a backup", boolKind(tTrue), `true`)
	register("backup.verify.readback", "backup", "read a backup back after uploading and check it", boolKind(tTrue), `true`)
	register("backup.retries", "backup", "how many times a failed backup is retried", intKind(false), `3`)
	register("backup.on_failure", "backup", "what happens when a backup keeps failing", enum("alert_pause_use", "alert"), `"alert"`)
	register("backup.heartbeat", "backup", "the longest a backup may go without the heartbeat noticing", durationKind(false, false), `"2h"`)
	register("backup.pg_dump_daily", "backup", "with PostgreSQL, put a daily pg_dump inside the encrypted backup too", boolKind(tFalse), `false`)

	// ---- Storage. The driver, DSN and tuning are read before the database opens (the
	// configuration file); these two are read by the lease loop, which runs after it.
	register("storage.lease.renew", "storage", "how often the primary instance renews its lease", durationKind(false, false), `"5s"`)
	register("storage.lease.expires", "storage", "how long a lease lasts without renewal, and so how long a failover takes", durationKind(false, false), `"15s"`)

	for _, d := range Definitions {
		if _, err := d.canonical(d.Default); err != nil {
			panic("settings: default for " + d.Key + " does not fit its kind: " + err.Error())
		}
		if _, err := d.canonical(d.Failsafe); err != nil {
			panic("settings: failsafe for " + d.Key + " does not fit its kind: " + err.Error())
		}
	}
}
