// Package config loads the service configuration (YAML) and the secrets it names. Secrets are never in
// the YAML itself: every credential is a *_file path, read once at start.
//
// Configuration problems are for the operator, so they are English, like the logs.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Severities are the alert severities the bridge knows, from the most to the least urgent.
var Severities = []string{"critical", "warning", "info"}

// Config is the service configuration.
type Config struct {
	Listen         string   `yaml:"listen"`
	DataDir        string   `yaml:"data_dir"`
	CatalogDir     string   `yaml:"catalog_dir"`
	PublicURL      string   `yaml:"public_url"`
	TrustedProxies []string `yaml:"trusted_proxies"`
	Language       string   `yaml:"language"`
	Timezone       string   `yaml:"timezone"`

	// Rules is the rule engine (vmalert or Prometheus) that posts alerts to the gateway's
	// /api/v2/alerts; TokenFile holds the bearer token it sends.
	Rules struct {
		TokenFile string `yaml:"token_file"`
	} `yaml:"rules"`
	Routing         Routing `yaml:"routing"`
	VictoriaMetrics struct {
		URL string `yaml:"url"`
	} `yaml:"victoriametrics"`
	// Gateway is the metrics gateway for machines (remote write, probe targets, heartbeats, reads,
	// Flux's alerts); see the gateway package. An empty Listen turns it off.
	Gateway struct {
		Listen      string `yaml:"listen"`
		TargetsDir  string `yaml:"targets_dir"`
		ModulesFile string `yaml:"modules_file"` // how to probe; default modules.yml in targets_dir
	} `yaml:"gateway"`
	// Storage is where the service's state lives: one SQL database, SQLite by default. Driver and
	// path belong here because the database has to be open before anything else can be read; the
	// rest of the storage settings live in the database itself (see the settings framework).
	Storage struct {
		Driver string `yaml:"driver"` // sqlite (the default); postgres arrives with the next package
		Path   string `yaml:"path"`   // default <data_dir>/beacon.db
		// Audit is the audit log's own schedule: how often its chain's checkpoints are signed
		// (24h by default) and how long entries are kept (365 days by default).
		Audit struct {
			CheckpointInterval Duration `yaml:"checkpoint_interval"`
			Retention          Duration `yaml:"retention"`
		} `yaml:"audit"`
		// SQLite holds the pragmas a deployment may tune. The defaults are what a deployment
		// should keep: WAL (readers never wait for the writer) and synchronous FULL (every
		// commit is on the disk before it answers). A WAL database is one file plus its -wal and
		// -shm side files and cannot live on a network file system.
		SQLite struct {
			JournalMode string   `yaml:"journal_mode"` // WAL (default), DELETE, TRUNCATE, PERSIST or MEMORY
			Synchronous string   `yaml:"synchronous"`  // FULL (default), NORMAL or OFF
			BusyTimeout Duration `yaml:"busy_timeout"` // default 15s
		} `yaml:"sqlite"`
	} `yaml:"storage"`
	// Backup is Beacon's own backups, and it is bootstrap-level on purpose: the recipients and
	// the destinations must be knowable before the database is open, because a restore has to
	// reach its destination with no database to read settings from (the plan's §9.1). Backups
	// are on when there is a destination; the recipients may be left out, in which case Beacon
	// generates a key pair once and hands the private half over as recovery material.
	Backup Backup `yaml:"backup"`
	// SSH is the channel through which agents reach machines with their own ssh, or with the MCP and
	// REST tools; it is on when CAKeyFile is set. PublicAddress (host:port) is where agents reach it,
	// for the ssh configuration Beacon hands them. SourceAddresses (CIDRs), when set, go into every
	// certificate, so certificates work only from Beacon's own addresses. Sessions are kept for Keep.
	SSH struct {
		Listen          string   `yaml:"listen"`
		PublicAddress   string   `yaml:"public_address"`
		HostKeyFile     string   `yaml:"host_key_file"`
		CAKeyFile       string   `yaml:"ca_key_file"`
		SourceAddresses []string `yaml:"source_addresses"`
		Keep            Duration `yaml:"keep"`
	} `yaml:"ssh"`
	Links struct {
		// GrafanaService and Runbook are URL templates: {service} and {alertname} are replaced.
		GrafanaService string `yaml:"grafana_service"`
		Runbook        string `yaml:"runbook"`
	} `yaml:"links"`
	// Pages are the HTML pages people open from the chat (docs/guide/console.md): an alert's timeline and a
	// session's replay, reached by signed links built from PublicURL. LinkTTL is how long a link is valid.
	Pages struct {
		LinkTTL Duration `yaml:"link_ttl"`
	} `yaml:"pages"`
	Branding Branding `yaml:"branding"`
	// OAuth is how MCP clients sign in to Beacon (docs/guide/people.md).
	OAuth OAuth `yaml:"oauth"`

	Feishu       Feishu              `yaml:"feishu"`
	Pushover     Pushover            `yaml:"pushover"`
	Bark         Bark                `yaml:"bark"`
	Healthchecks Healthchecks        `yaml:"healthchecks"`
	Escalation   map[string]Policy   `yaml:"escalation"`
	AckTTL       Duration            `yaml:"ack_ttl"`
	Agents       []Agent             `yaml:"agents"`  // hooks that wake agents; see agents.go
	Changes      Changes             `yaml:"changes"` // change events from the gateway
	Secrets      Secrets             `yaml:"-"`
	Location     *time.Location      `yaml:"-"`
	Proxies      []netip.Prefix      `yaml:"-"`
	OnCallByID   map[string]OnCaller `yaml:"-"`
}

// Branding is what Beacon's public pages say of the deployment and of Beacon (docs/guide/status-pages.md). Title
// names the deployment on the page at / when there is no status page (Beacon when empty). PoweredBy, on
// unless set to false, puts "Powered by Beacon, open source" in the public pages' footer; a team may
// leave it out of its status pages. The console always says what it runs on.
type Branding struct {
	Title     string `yaml:"title"`
	PoweredBy *bool  `yaml:"powered_by"`
}

// OAuth is how MCP clients sign in. ClientIDDocuments, on unless set to false, takes a client whose
// client_id is the https URL of its metadata document (Claude Code's and Codex's are), which Beacon
// fetches from the internet; a deployment that cannot reach the internet sets it to false, and clients
// then register themselves instead.
type OAuth struct {
	ClientIDDocuments *bool `yaml:"client_id_documents"`
}

// Backup is the backup section: where backups go, who can read them, how often and how long they
// are kept. Enabled means at least one destination; everything else has a working default.
type Backup struct {
	// Recipients are public keys backups are encrypted to: age (age1…) or ssh-ed25519, inline.
	// The server holds only these; it cannot read what it wrote.
	Recipients     []string `yaml:"recipients"`
	RecipientsFile string   `yaml:"recipients_file"` // more of them, one per line
	// SecretsDir is packed whole into every backup (it holds the root key the database's
	// encrypted halves need, so a snapshot and its secrets belong together). Default
	// /etc/beacon/secrets; empty means nothing is packed for it.
	SecretsDir string `yaml:"secrets_dir"`
	// Heartbeat is the built-in beacon-backup heartbeat: a backup that has not succeeded within
	// MaxAge alerts through the ordinary HeartbeatStale path.
	Heartbeat struct {
		MaxAge   Duration `yaml:"max_age"`  // default 2h
		Severity string   `yaml:"severity"` // default warning
	} `yaml:"heartbeat"`
	// DrillExpiry is how long a recovery drill's proof lasts: the built-in recovery-drill
	// resource expires this long after the last drill and reminds before it does. Default 90d.
	DrillExpiry Duration `yaml:"drill_expiry"`
	Schedule    struct {
		Database  Duration `yaml:"database"`   // the database snapshot alone; default 1h
		Files     Duration `yaml:"files"`      // recordings, configuration, secrets; default 24h
		FilesFull Duration `yaml:"files_full"` // a complete recordings set every so often; default 168h
	} `yaml:"schedule"`
	Retention struct {
		Hourly  int `yaml:"hourly"`  // default 24
		Daily   int `yaml:"daily"`   // default 14
		Weekly  int `yaml:"weekly"`  // default 8
		Monthly int `yaml:"monthly"` // default 12
	} `yaml:"retention"`
	// Retries is how many times a failed upload is tried again, the wait growing fourfold each
	// time from RetryWait (default 3, from 1m).
	Retries        int      `yaml:"retries"`
	RetryWait      Duration `yaml:"retry_wait"`
	VerifyReadback *bool    `yaml:"verify_readback"` // read every upload back and compare; default true
	// OnFailure is what a backup that keeps failing does: alert (the default, through the
	// heartbeat going stale). alert_and_pause is rejected until it does something.
	OnFailure    string              `yaml:"on_failure"`
	Destinations []BackupDestination `yaml:"destinations"`
}

// BackupDestination is one place backups are written: a local directory, or an S3-compatible
// store. Exactly one of the two.
type BackupDestination struct {
	Dir string    `yaml:"dir"` // a local directory, made when missing
	S3  *BackupS3 `yaml:"s3"`
}

// BackupS3 is an S3-compatible destination (MinIO, R2, S3), addressed path-style. The
// credentials are read from their files at use, so a rotated key needs no restart.
type BackupS3 struct {
	Endpoint      string `yaml:"endpoint"` // such as https://s3.example.com:9000
	Region        string `yaml:"region"`   // for signing; us-east-1 when the store ignores it
	Bucket        string `yaml:"bucket"`
	Prefix        string `yaml:"prefix"` // inside the bucket, such as beacon/
	AccessKeyFile string `yaml:"access_key_file"`
	SecretKeyFile string `yaml:"secret_key_file"`
}

// Enabled reports whether backups are on: at least one destination.
func (b Backup) Enabled() bool { return len(b.Destinations) > 0 }

// VerifyReadbackOn reports the read-back check's setting (on unless said otherwise).
func (b Backup) VerifyReadbackOn() bool { return b.VerifyReadback == nil || *b.VerifyReadback }

// Documents reports whether Beacon takes clients by their metadata documents.
func (o OAuth) Documents() bool { return o.ClientIDDocuments == nil || *o.ClientIDDocuments }

// Powered reports whether the public pages say they are powered by Beacon.
func (b Branding) Powered() bool { return b.PoweredBy == nil || *b.PoweredBy }

// Name is the deployment's title, Beacon unless the configuration names it.
func (b Branding) Name() string {
	if t := strings.TrimSpace(b.Title); t != "" {
		return t
	}
	return "Beacon"
}

// Routing groups and inhibits alerts (what Alertmanager's route and inhibit_rules did). Label values in
// match, source and target are regular expressions over the whole value.
type Routing struct {
	GroupBy        []string    `yaml:"group_by"`
	GroupWait      Duration    `yaml:"group_wait"`
	GroupInterval  Duration    `yaml:"group_interval"`
	ResolveTimeout Duration    `yaml:"resolve_timeout"`
	Routes         []RouteRule `yaml:"routes"`
	Inhibit        []Inhibit   `yaml:"inhibit"`
}

// RouteRule gives the alerts it matches their own grouping; the first match wins.
type RouteRule struct {
	Match   map[string]string `yaml:"match"`
	GroupBy []string          `yaml:"group_by"`
}

// Inhibit mutes target alerts while a source alert fires with the same values of the equal labels.
type Inhibit struct {
	Source map[string]string `yaml:"source"`
	Target map[string]string `yaml:"target"`
	Equal  []string          `yaml:"equal"`
}

// Feishu is the self-built app (cards, callbacks, buzz) and the group's custom bot (plain messages).
type Feishu struct {
	AppIDFile             string     `yaml:"app_id_file"`
	AppSecretFile         string     `yaml:"app_secret_file"`
	Domain                string     `yaml:"domain"`   // feishu (default) or lark
	Callback              string     `yaml:"callback"` // websocket (default) or http
	VerificationTokenFile string     `yaml:"verification_token_file"`
	EncryptKeyFile        string     `yaml:"encrypt_key_file"`
	ChatID                string     `yaml:"chat_id"`
	OnCall                []OnCaller `yaml:"on_call"`
	MonthlyBudget         int        `yaml:"monthly_budget"`
	WebhookURLFile        string     `yaml:"webhook_url_file"`
	WebhookSecretFile     string     `yaml:"webhook_secret_file"`
	WebhookKeyword        string     `yaml:"webhook_keyword"`
}

// OnCaller is a person who may act on alert cards and who is buzzed when an alert escalates.
type OnCaller struct {
	OpenID string `yaml:"open_id"`
	Name   string `yaml:"name"`
}

// Pushover is the emergency push channel for iOS (critical alerts repeat until acknowledged).
type Pushover struct {
	UserKeyFile string   `yaml:"user_key_file"`
	TokenFile   string   `yaml:"token_file"`
	Retry       Duration `yaml:"retry"`
	Expire      Duration `yaml:"expire"`
	API         string   `yaml:"api"` // for tests; default https://api.pushover.net
}

// Bark is an optional iOS push with a ringing critical level.
type Bark struct {
	URLFile string `yaml:"url_file"` // https://api.day.app/<key>, or a self-hosted server
}

// Healthchecks is where the bridge reports that it is healthy (a dead man's switch outside our infrastructure).
type Healthchecks struct {
	BridgeURLFile string `yaml:"bridge_url_file"`
}

// Policy says how alerts of one severity are delivered and escalated. Zero durations turn a step off.
type Policy struct {
	Channel  string   `yaml:"channel"` // app (Feishu app card), webhook (group custom bot) or none (only recorded)
	Buzz     Duration `yaml:"buzz"`
	Pushover Duration `yaml:"pushover"`
	Bark     Duration `yaml:"bark"`
}

// Secrets are the credentials read from the *_file paths.
type Secrets struct {
	RulesToken              string
	FeishuAppID             string
	FeishuAppSecret         string
	FeishuVerificationToken string
	FeishuEncryptKey        string
	FeishuWebhookURL        string
	FeishuWebhookSecret     string
	PushoverUserKey         string
	PushoverToken           string
	BarkURL                 string
	HealthchecksBridgeURL   string
	SSHHostKey              string // OpenSSH private keys
	SSHCAKey                string
}

// Duration is a time.Duration written as "10m" in YAML.
type Duration time.Duration

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %q is not a duration such as 10m or 4h", n.Line, n.Value)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// String writes the duration the way the configuration does: 26h rather than 26h0m0s.
func (d Duration) String() string {
	s := time.Duration(d).String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

// MarshalJSON writes "26h", as in YAML.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON reads "26h", as in YAML.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("%s is not a duration such as \"10m\" or \"4h\" (a string)", b)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration such as 10m or 4h", s)
	}
	*d = Duration(v)
	return nil
}

// Load reads, defaults and validates the configuration at path and reads its secrets.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse is Load without reading the file; the secrets are still read from their files.
func Parse(b []byte) (*Config, error) {
	c := &Config{}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("configuration: %w", err)
	}
	c.defaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	if err := c.readSecrets(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) defaults() {
	set := func(s *string, v string) {
		if *s == "" {
			*s = v
		}
	}
	set(&c.Listen, ":17001")
	set(&c.DataDir, "/var/lib/beacon")
	set(&c.CatalogDir, "/etc/beacon/catalog")
	set(&c.Gateway.TargetsDir, "/etc/beacon/targets")
	set(&c.Gateway.Listen, ":17006")
	set(&c.Gateway.ModulesFile, strings.TrimSuffix(c.Gateway.TargetsDir, "/")+"/modules.yml")
	set(&c.SSH.Listen, ":17002")
	if c.SSH.Keep == 0 {
		c.SSH.Keep = Duration(180 * 24 * time.Hour)
	}
	if c.Routing.GroupBy == nil {
		c.Routing.GroupBy = []string{"alertname", "service"}
	}
	if c.Routing.GroupWait == 0 {
		c.Routing.GroupWait = Duration(30 * time.Second)
	}
	if c.Routing.GroupInterval == 0 {
		c.Routing.GroupInterval = Duration(2 * time.Minute)
	}
	if c.Routing.ResolveTimeout == 0 {
		c.Routing.ResolveTimeout = Duration(5 * time.Minute)
	}
	set(&c.Storage.Driver, "sqlite")
	if c.Storage.Audit.CheckpointInterval == 0 {
		c.Storage.Audit.CheckpointInterval = Duration(24 * time.Hour)
	}
	if c.Storage.Audit.Retention == 0 {
		c.Storage.Audit.Retention = Duration(365 * 24 * time.Hour)
	}
	set(&c.Backup.SecretsDir, "/etc/beacon/secrets")
	if c.Backup.Heartbeat.MaxAge == 0 {
		c.Backup.Heartbeat.MaxAge = Duration(2 * time.Hour)
	}
	set(&c.Backup.Heartbeat.Severity, "warning")
	if c.Backup.DrillExpiry == 0 {
		c.Backup.DrillExpiry = Duration(90 * 24 * time.Hour)
	}
	if c.Backup.Schedule.Database == 0 {
		c.Backup.Schedule.Database = Duration(time.Hour)
	}
	if c.Backup.Schedule.Files == 0 {
		c.Backup.Schedule.Files = Duration(24 * time.Hour)
	}
	if c.Backup.Schedule.FilesFull == 0 {
		c.Backup.Schedule.FilesFull = Duration(7 * 24 * time.Hour)
	}
	if c.Backup.Retention.Hourly == 0 && c.Backup.Retention.Daily == 0 &&
		c.Backup.Retention.Weekly == 0 && c.Backup.Retention.Monthly == 0 {
		c.Backup.Retention.Hourly, c.Backup.Retention.Daily = 24, 14
		c.Backup.Retention.Weekly, c.Backup.Retention.Monthly = 8, 12
	}
	if c.Backup.Retries == 0 {
		c.Backup.Retries = 3
	}
	if c.Backup.RetryWait == 0 {
		c.Backup.RetryWait = Duration(time.Minute)
	}
	set(&c.Backup.OnFailure, "alert")
	set(&c.Language, "zh")
	set(&c.Timezone, "Asia/Shanghai")
	set(&c.Feishu.Domain, "feishu")
	set(&c.Feishu.Callback, "websocket")
	set(&c.Feishu.WebhookKeyword, "Beacon")
	set(&c.Pushover.API, "https://api.pushover.net")
	if c.Feishu.MonthlyBudget == 0 {
		c.Feishu.MonthlyBudget = 10000
	}
	if c.Pushover.Retry == 0 {
		c.Pushover.Retry = Duration(time.Minute)
	}
	if c.Pushover.Expire == 0 {
		c.Pushover.Expire = Duration(3 * time.Hour)
	}
	if c.AckTTL == 0 {
		c.AckTTL = Duration(4 * time.Hour)
	}
	if c.Pages.LinkTTL == 0 {
		c.Pages.LinkTTL = Duration(30 * 24 * time.Hour)
	}
	c.agentDefaults()
	if c.Escalation == nil {
		// The best channel configured; with none, alerts are only recorded (the API and MCP still show them).
		ch := "none"
		switch {
		case c.Feishu.AppIDFile != "":
			ch = "app"
		case c.Feishu.WebhookURLFile != "":
			ch = "webhook"
		}
		crit, info := Policy{Channel: ch}, Policy{Channel: ch}
		if ch == "app" {
			crit.Buzz = Duration(10 * time.Minute)
			if c.Feishu.WebhookURLFile != "" {
				info.Channel = "webhook" // low-severity alerts need no app quota
			}
		}
		if c.Pushover.TokenFile != "" {
			crit.Pushover = Duration(15 * time.Minute)
		}
		c.Escalation = map[string]Policy{"critical": crit, "warning": {Channel: ch}, "info": info}
	}
}

func (c *Config) validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		bad("listen %q is not an address such as :17001", c.Listen)
	}
	if c.PublicURL != "" {
		if u, err := url.Parse(c.PublicURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			bad("public_url %q is not an http(s) URL", c.PublicURL)
		}
	}
	for _, p := range c.TrustedProxies {
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			if a, err2 := netip.ParseAddr(p); err2 == nil {
				pfx = netip.PrefixFrom(a, a.BitLen())
			} else {
				bad("trusted_proxies: %q is not an address or a CIDR range", p)
				continue
			}
		}
		c.Proxies = append(c.Proxies, pfx.Masked())
	}
	if c.Language != "zh" && c.Language != "en" {
		bad("language must be zh or en, not %q", c.Language)
	}
	if t := c.Branding.Title; utf8.RuneCountInString(t) > 80 || strings.ContainsFunc(t, unicode.IsControl) {
		bad("branding.title must be one line of at most 80 characters")
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		bad("timezone %q is not a time zone such as Asia/Shanghai", c.Timezone)
	}
	c.Location = loc

	if c.SSH.CAKeyFile != "" {
		if _, _, err := net.SplitHostPort(c.SSH.Listen); err != nil {
			bad("ssh.listen %q is not an address such as :17002", c.SSH.Listen)
		}
		if c.SSH.HostKeyFile == "" {
			bad("ssh.host_key_file is needed with ssh.ca_key_file: it is the channel's own host key")
		}
		if c.SSH.PublicAddress != "" {
			if _, _, err := net.SplitHostPort(c.SSH.PublicAddress); err != nil {
				bad("ssh.public_address %q is not host:port", c.SSH.PublicAddress)
			}
		}
		for _, a := range c.SSH.SourceAddresses {
			if _, err := netip.ParsePrefix(a); err != nil {
				bad("ssh.source_addresses: %q is not a CIDR range such as 203.0.113.7/32", a)
			}
		}
		if c.SSH.Keep.D() < 24*time.Hour {
			bad("ssh.keep is at least 24h")
		}
	}

	if c.Storage.Driver != "sqlite" {
		bad("storage.driver %q: sqlite is the driver today; postgres arrives with the next package", c.Storage.Driver)
	}
	if t := c.Storage.Audit.CheckpointInterval.D(); t < time.Minute {
		bad("storage.audit.checkpoint_interval is at least 1m")
	}
	if t := c.Storage.Audit.Retention.D(); t < 24*time.Hour {
		bad("storage.audit.retention is at least 24h")
	}

	if c.Backup.Enabled() {
		for i, d := range c.Backup.Destinations {
			switch {
			case d.Dir != "" && d.S3 != nil:
				bad("backup.destinations[%d]: dir and s3 are alternatives, not both", i)
			case d.Dir == "" && d.S3 == nil:
				bad("backup.destinations[%d]: either dir or s3 is needed", i)
			case d.S3 != nil:
				s := d.S3
				if s.Endpoint == "" || s.Bucket == "" {
					bad("backup.destinations[%d].s3 needs endpoint and bucket", i)
				}
				if u, err := url.Parse(s.Endpoint); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
					bad("backup.destinations[%d].s3.endpoint %q is not an http(s) URL", i, s.Endpoint)
				}
				if s.AccessKeyFile == "" || s.SecretKeyFile == "" {
					bad("backup.destinations[%d].s3 needs access_key_file and secret_key_file", i)
				}
				for _, f := range []struct{ path, what string }{
					{s.AccessKeyFile, "access_key_file"}, {s.SecretKeyFile, "secret_key_file"},
				} {
					if f.path != "" {
						if _, err := os.Stat(f.path); err != nil {
							bad("backup.destinations[%d].s3.%s: %v", i, f.what, err)
						}
					}
				}
			}
		}
		if c.Backup.RecipientsFile != "" {
			if _, err := os.Stat(c.Backup.RecipientsFile); err != nil {
				bad("backup.recipients_file: %v", err)
			}
		}
		for i, r := range c.Backup.Recipients {
			if !strings.HasPrefix(r, "age1") && !strings.HasPrefix(r, "ssh-ed25519 ") {
				bad("backup.recipients[%d] is an age public key (age1…) or an ssh-ed25519 line", i)
			}
		}
		if t := c.Backup.Heartbeat.MaxAge.D(); t < time.Minute {
			bad("backup.heartbeat.max_age is at least 1m")
		}
		if !slices.Contains(Severities, c.Backup.Heartbeat.Severity) {
			bad("backup.heartbeat.severity must be one of %s", strings.Join(Severities, ", "))
		}
		if t := c.Backup.Schedule.Database.D(); t < 5*time.Minute {
			bad("backup.schedule.database is at least 5m")
		}
		if t := c.Backup.Schedule.Files.D(); t < time.Hour {
			bad("backup.schedule.files is at least 1h")
		}
		if c.Backup.Schedule.FilesFull.D() < c.Backup.Schedule.Files.D() {
			bad("backup.schedule.files_full is at least backup.schedule.files")
		}
		if c.Backup.OnFailure != "alert" {
			bad("backup.on_failure is alert, not %q (alert_and_pause is not implemented)", c.Backup.OnFailure)
		}
		if c.Storage.Driver == "postgres" || c.Storage.Driver == "postgresql" {
			bad("backup does not snapshot PostgreSQL yet; use the database's own backup or keep storage.driver as sqlite")
		}
		if c.Backup.Retries < 0 || c.Backup.Retries > 10 {
			bad("backup.retries is between 0 and 10")
		}
		if c.Backup.RetryWait.D() < time.Second {
			bad("backup.retry_wait is at least 1s")
		}
	}

	if c.Gateway.Listen != "" {
		if _, _, err := net.SplitHostPort(c.Gateway.Listen); err != nil {
			bad("gateway.listen %q is not an address such as :17006", c.Gateway.Listen)
		}
		if c.VictoriaMetrics.URL == "" {
			bad("victoriametrics.url is required: the gateway writes there and the bridge reads from it")
		}
	}
	if c.Rules.TokenFile == "" {
		bad("rules.token_file is required: the rule engine authenticates its alerts with it")
	}
	checkLabels := func(what string, m map[string]string) {
		for k, v := range m {
			if _, err := regexp.Compile("^(?:" + v + ")$"); err != nil {
				bad("%s: %s: %v", what, k, err)
			}
		}
	}
	for i, r := range c.Routing.Routes {
		if len(r.Match) == 0 {
			bad("routing.routes[%d] needs match", i)
		}
		checkLabels(fmt.Sprintf("routing.routes[%d].match", i), r.Match)
	}
	for i, in := range c.Routing.Inhibit {
		if len(in.Source) == 0 || len(in.Target) == 0 {
			bad("routing.inhibit[%d] needs source and target", i)
		}
		checkLabels(fmt.Sprintf("routing.inhibit[%d].source", i), in.Source)
		checkLabels(fmt.Sprintf("routing.inhibit[%d].target", i), in.Target)
	}

	f := c.Feishu
	if f.AppIDFile != "" || f.AppSecretFile != "" {
		if f.AppIDFile == "" || f.AppSecretFile == "" {
			bad("feishu.app_id_file and feishu.app_secret_file go together")
		}
		if f.ChatID == "" {
			bad("feishu.chat_id is required with the Feishu app: it is the group the alert cards go to")
		}
		if len(f.OnCall) == 0 {
			bad("feishu.on_call needs at least one person: only they may act on cards, and they are buzzed")
		}
	}
	if f.Domain != "feishu" && f.Domain != "lark" {
		bad("feishu.domain must be feishu or lark, not %q", f.Domain)
	}
	switch f.Callback {
	case "websocket":
	case "http":
		if f.VerificationTokenFile == "" {
			bad("feishu.callback http needs feishu.verification_token_file")
		}
	default:
		bad("feishu.callback must be websocket or http, not %q", f.Callback)
	}
	c.OnCallByID = map[string]OnCaller{}
	for _, p := range f.OnCall {
		if !strings.HasPrefix(p.OpenID, "ou_") {
			bad("feishu.on_call: %q is not an open_id (ou_…)", p.OpenID)
		}
		if _, dup := c.OnCallByID[p.OpenID]; dup {
			bad("feishu.on_call: %s is listed twice", p.OpenID)
		}
		if p.Name == "" {
			bad("feishu.on_call: %s needs a name, shown on the cards", p.OpenID)
		}
		c.OnCallByID[p.OpenID] = p
	}
	if f.MonthlyBudget < 100 {
		bad("feishu.monthly_budget %d is too small; it is the app's API calls per month", f.MonthlyBudget)
	}
	if (c.Pushover.UserKeyFile == "") != (c.Pushover.TokenFile == "") {
		bad("pushover.user_key_file and pushover.token_file go together")
	}
	if c.Pushover.Retry.D() < 30*time.Second {
		bad("pushover.retry must be at least 30s (a Pushover limit)")
	}
	if c.Pushover.Expire.D() > 3*time.Hour {
		bad("pushover.expire must be at most 3h (a Pushover limit)")
	}
	for sev, p := range c.Escalation {
		if !slices.Contains(Severities, sev) {
			bad("escalation: %q is not a severity (%s)", sev, strings.Join(Severities, ", "))
		}
		if p.Channel != "app" && p.Channel != "webhook" && p.Channel != "none" {
			bad("escalation.%s.channel must be app, webhook or none, not %q", sev, p.Channel)
		}
		if p.Channel == "app" && f.AppIDFile == "" {
			bad("escalation.%s uses the Feishu app, but feishu.app_id_file is not set", sev)
		}
		if p.Channel == "webhook" && f.WebhookURLFile == "" {
			bad("escalation.%s uses the group bot, but feishu.webhook_url_file is not set", sev)
		}
		if p.Buzz < 0 || p.Pushover < 0 || p.Bark < 0 {
			bad("escalation.%s: durations cannot be negative", sev)
		}
		if p.Pushover > 0 && c.Pushover.TokenFile == "" {
			bad("escalation.%s escalates to Pushover, but pushover is not configured", sev)
		}
		if p.Bark > 0 && c.Bark.URLFile == "" {
			bad("escalation.%s escalates to Bark, but bark.url_file is not set", sev)
		}
		if p.Buzz > 0 && p.Channel != "app" {
			bad("escalation.%s: buzz needs the Feishu app channel", sev)
		}
	}
	if c.AckTTL.D() < 10*time.Minute {
		bad("ack_ttl must be at least 10m")
	}
	if ttl := c.Pages.LinkTTL.D(); ttl < time.Hour || ttl > 366*24*time.Hour {
		bad("pages.link_ttl is between 1h and 366 days (8784h)")
	}
	errs = append(errs, c.validateAgents()...)
	return errors.Join(errs...)
}

func (c *Config) readSecrets() error {
	var errs []error
	read := func(dst *string, path, what string) {
		if path == "" {
			return
		}
		b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", what, err))
			return
		}
		*dst = strings.TrimSpace(string(b))
		if *dst == "" {
			errs = append(errs, fmt.Errorf("%s: %s is empty", what, path))
		}
	}
	s := &c.Secrets
	read(&s.RulesToken, c.Rules.TokenFile, "rules.token_file")
	read(&s.FeishuAppID, c.Feishu.AppIDFile, "feishu.app_id_file")
	read(&s.FeishuAppSecret, c.Feishu.AppSecretFile, "feishu.app_secret_file")
	read(&s.FeishuVerificationToken, c.Feishu.VerificationTokenFile, "feishu.verification_token_file")
	read(&s.FeishuEncryptKey, c.Feishu.EncryptKeyFile, "feishu.encrypt_key_file")
	read(&s.FeishuWebhookURL, c.Feishu.WebhookURLFile, "feishu.webhook_url_file")
	read(&s.FeishuWebhookSecret, c.Feishu.WebhookSecretFile, "feishu.webhook_secret_file")
	read(&s.PushoverUserKey, c.Pushover.UserKeyFile, "pushover.user_key_file")
	read(&s.PushoverToken, c.Pushover.TokenFile, "pushover.token_file")
	read(&s.BarkURL, c.Bark.URLFile, "bark.url_file")
	read(&s.HealthchecksBridgeURL, c.Healthchecks.BridgeURLFile, "healthchecks.bridge_url_file")
	if c.SSH.CAKeyFile != "" {
		read(&s.SSHHostKey, c.SSH.HostKeyFile, "ssh.host_key_file")
		read(&s.SSHCAKey, c.SSH.CAKeyFile, "ssh.ca_key_file")
	}
	errs = append(errs, c.readAgentSecrets()...)
	if len(s.RulesToken) > 0 && len(s.RulesToken) < 24 {
		errs = append(errs, errors.New("rules.token_file: the token is too short; use at least 24 random characters"))
	}
	return errors.Join(errs...)
}

// Policy returns the delivery policy for a severity; unknown severities are treated as warning.
func (c *Config) Policy(severity string) Policy {
	if p, ok := c.Escalation[severity]; ok {
		return p
	}
	return c.Escalation["warning"]
}
