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

	Feishu       Feishu              `yaml:"feishu"`
	Pushover     Pushover            `yaml:"pushover"`
	Bark         Bark                `yaml:"bark"`
	Healthchecks Healthchecks        `yaml:"healthchecks"`
	Escalation   map[string]Policy   `yaml:"escalation"`
	AckTTL       Duration            `yaml:"ack_ttl"`
	Secrets      Secrets             `yaml:"-"`
	Location     *time.Location      `yaml:"-"`
	Proxies      []netip.Prefix      `yaml:"-"`
	OnCallByID   map[string]OnCaller `yaml:"-"`
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
	set(&c.DataDir, "/var/lib/linkos-beacon")
	set(&c.CatalogDir, "/etc/linkos-beacon/catalog")
	set(&c.Gateway.TargetsDir, "/etc/linkos-beacon/targets")
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
	set(&c.Language, "zh")
	set(&c.Timezone, "Asia/Shanghai")
	set(&c.Feishu.Domain, "feishu")
	set(&c.Feishu.Callback, "websocket")
	set(&c.Feishu.WebhookKeyword, "LinkOS")
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
