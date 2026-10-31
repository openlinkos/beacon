package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// secret writes value to a file in dir and returns its path.
func secret(t *testing.T, dir, name, value string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(p)
}

func full(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	return `
public_url: https://status.example.com
trusted_proxies: [172.16.0.0/12, 10.0.0.1]
rules:
  token_file: ` + secret(t, d, "am", "0123456789abcdef0123456789") + `
victoriametrics:
  url: http://vm:8428
routing:
  group_by: [alertname, service, failure_domain]
  routes:
    - {match: {alertname: MainlandPathDegraded|ServiceDegraded}, group_by: [alertname, severity]}
  inhibit:
    - {source: {alertname: HostDown}, target: {alertname: ServiceDown}, equal: [failure_domain]}
feishu:
  app_id_file: ` + secret(t, d, "id", "cli_x") + `
  app_secret_file: ` + secret(t, d, "secret", "s3cret") + `
  chat_id: oc_123
  on_call:
    - {open_id: ou_aaa, name: Alice}
  webhook_url_file: ` + secret(t, d, "hook", "https://open.feishu.cn/open-apis/bot/v2/hook/x") + `
pushover:
  user_key_file: ` + secret(t, d, "pu", "u") + `
  token_file: ` + secret(t, d, "pt", "t") + `
escalation:
  critical: {channel: app, buzz: 10m, pushover: 15m}
  warning: {channel: app}
  info: {channel: webhook}
`
}

func TestParseFull(t *testing.T) {
	c, err := Parse([]byte(full(t)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":17001" || c.Language != "zh" || c.Location.String() != "Asia/Shanghai" {
		t.Errorf("defaults: %+v", c)
	}
	if c.Secrets.FeishuAppSecret != "s3cret" || c.Secrets.PushoverToken != "t" {
		t.Errorf("secrets not read: %+v", c.Secrets)
	}
	if p := c.Policy("critical"); p.Buzz.D() != 10*time.Minute || p.Pushover.D() != 15*time.Minute {
		t.Errorf("critical policy: %+v", p)
	}
	if p := c.Policy("unknown"); p.Channel != "app" {
		t.Errorf("unknown severities fall back to warning: %+v", p)
	}
	if len(c.Proxies) != 2 || c.Proxies[1].String() != "10.0.0.1/32" {
		t.Errorf("proxies: %v", c.Proxies)
	}
	if c.OnCallByID["ou_aaa"].Name != "Alice" {
		t.Errorf("on-call index: %v", c.OnCallByID)
	}
	if len(c.Routing.Routes) != 1 || c.Routing.GroupWait.D() != 30*time.Second || c.Gateway.Listen != ":17006" || c.Secrets.RulesToken == "" {
		t.Errorf("routing: %+v gateway %+v", c.Routing, c.Gateway)
	}
}

func TestRoutingErrors(t *testing.T) {
	d := t.TempDir()
	rules := "rules: {token_file: " + secret(t, d, "tok", "0123456789abcdef0123456789") + "}\n"
	for name, y := range map[string]string{
		"bad regexp":     "routing: {routes: [{match: {alertname: '('}}]}\n",
		"route no match": "routing: {routes: [{group_by: [a]}]}\n",
		"inhibit target": "routing: {inhibit: [{source: {alertname: A}}]}\n",
	} {
		if _, err := Parse([]byte(rules + y)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseErrors(t *testing.T) {
	d := t.TempDir()
	tok := secret(t, d, "am", "0123456789abcdef0123456789")
	cases := map[string]string{
		"unknown field":  "rules: {token_file: " + tok + "}\nbogus: 1\n",
		"no rules token": "listen: :1\n",
		"short token":    "rules: {token_file: " + secret(t, d, "short", "abc") + "}\n",
		"missing secret": "rules: {token_file: " + filepath.ToSlash(filepath.Join(d, "nope")) + "}\n",
		"app without chat": "rules: {token_file: " + tok + "}\n" +
			"feishu: {app_id_file: " + tok + ", app_secret_file: " + tok + ", on_call: [{open_id: ou_a, name: A}]}\n",
		"bad open id": "rules: {token_file: " + tok + "}\n" +
			"feishu: {app_id_file: " + tok + ", app_secret_file: " + tok + ", chat_id: oc_1, on_call: [{open_id: x, name: A}]}\n",
		"pushover not configured": "rules: {token_file: " + tok + "}\n" +
			"feishu: {webhook_url_file: " + tok + "}\n" +
			"escalation: {critical: {channel: webhook, pushover: 15m}}\n",
		"buzz without app": "rules: {token_file: " + tok + "}\n" +
			"feishu: {webhook_url_file: " + tok + "}\n" +
			"escalation: {critical: {channel: webhook, buzz: 10m}}\n",
		"bad duration": "rules: {token_file: " + tok + "}\nack_ttl: soon\n",
		"bad severity": "rules: {token_file: " + tok + "}\n" +
			"feishu: {webhook_url_file: " + tok + "}\nescalation: {page: {channel: webhook}}\n",
		"http callback without token": "rules: {token_file: " + tok + "}\nfeishu: {callback: http}\n",
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected an error", name)
		} else if strings.Contains(err.Error(), "0123456789abcdef") {
			t.Errorf("%s: the error leaks a secret: %v", name, err)
		}
	}
}

func TestRecordOnlyAndDefaults(t *testing.T) {
	d := t.TempDir()
	am := "rules:\n  token_file: " + secret(t, d, "am", "0123456789abcdef0123456789") + "\nvictoriametrics: {url: http://vm:8428}\n"
	// no channel at all: alerts are only recorded
	c, err := Parse([]byte(am))
	if err != nil {
		t.Fatal(err)
	}
	for _, sev := range Severities {
		if p := c.Escalation[sev]; p.Channel != "none" || p.Buzz != 0 || p.Pushover != 0 {
			t.Errorf("%s: %+v", sev, p)
		}
	}
	// the app without the group bot: info goes to the app too (it needs no bot)
	app := am + "feishu:\n  app_id_file: " + secret(t, d, "id", "cli_x") + "\n  app_secret_file: " + secret(t, d, "s", "y") +
		"\n  chat_id: oc_1\n  on_call: [{open_id: ou_a, name: A}]\n"
	if c, err = Parse([]byte(app)); err != nil {
		t.Fatal(err)
	}
	if c.Escalation["info"].Channel != "app" || c.Escalation["critical"].Buzz.D() != 10*time.Minute || c.Escalation["critical"].Pushover != 0 {
		t.Errorf("app only: %+v", c.Escalation)
	}
	// the group bot alone
	bot := am + "feishu:\n  webhook_url_file: " + secret(t, d, "hook", "https://x") + "\n"
	if c, err = Parse([]byte(bot)); err != nil {
		t.Fatal(err)
	}
	if c.Escalation["critical"].Channel != "webhook" {
		t.Errorf("bot only: %+v", c.Escalation)
	}
}

func TestGateway(t *testing.T) {
	d := t.TempDir()
	am := "rules:\n  token_file: " + secret(t, d, "am", "0123456789abcdef0123456789") + "\n"
	if _, err := Parse([]byte(am + "gateway:\n  listen: \":17006\"\n")); err == nil || !strings.Contains(err.Error(), "victoriametrics.url") {
		t.Errorf("a gateway without VictoriaMetrics: %v", err)
	}
	c, err := Parse([]byte(am + "victoriametrics:\n  url: http://vm:17005\ngateway:\n  listen: \":17006\"\n"))
	if err != nil || c.Gateway.TargetsDir != "/etc/linkos-status/targets" {
		t.Fatalf("gateway: %+v %v", c.Gateway, err)
	}
}
