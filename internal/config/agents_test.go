package config

import (
	"strings"
	"testing"
	"time"
)

func TestAgents(t *testing.T) {
	d := t.TempDir()
	y := full(t) + `
agents:
  - name: claude-oncall
    kind: webhook
    url: https://agents.example.com/hooks/beacon
    secret_file: ` + secret(t, d, "hook", "0123456789abcdef0123456789") + `
    severities: [critical, warning]
    service: "hub-.*|git-.*"
    resolved: true
  - name: ops-workflow
    kind: github
    repository: example/ops
    workflow: oncall.yml
    token_file: ` + secret(t, d, "gh", "github_pat_x") + `
changes:
  keep: 240h
`
	c, err := Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	w, g := c.Agents[0], c.Agents[1]
	if w.Secret != "0123456789abcdef0123456789" || w.ServiceRe == nil || !w.ServiceRe.MatchString("hub-linkos-io") || w.ServiceRe.MatchString("xhub-a") {
		t.Errorf("webhook: %+v", w)
	}
	if g.Secret != "github_pat_x" || g.Ref != "main" || g.API != "https://api.github.com" || len(g.Severities) != 1 || g.Severities[0] != "critical" {
		t.Errorf("github defaults: %+v", g)
	}
	if c.Changes.Keep.D() != 240*time.Hour {
		t.Errorf("changes.keep: %v", c.Changes.Keep)
	}

	plain, err := Parse([]byte(full(t)))
	if err != nil || plain.Changes.Keep.D() != 30*24*time.Hour {
		t.Errorf("changes are kept 30 days by default: %v %v", plain.Changes.Keep, err)
	}
}

func TestAgentsInvalid(t *testing.T) {
	d := t.TempDir()
	short := secret(t, d, "short", "too-short")
	y := strings.Replace(full(t), "public_url: https://status.example.com\n", "", 1) + `
agents:
  - {name: Bad Name, kind: webhook, url: "ftp://x", secret_file: ` + short + `}
  - {name: a, kind: webhook, url: "https://x.example", severities: [urgent], service: "("}
  - {name: a, kind: github, repository: nope, workflow: "a b", url: "https://x"}
  - {name: c, kind: pager}
changes: {keep: 1h}
`
	_, err := Parse([]byte(y))
	if err == nil {
		t.Fatal("invalid agents accepted")
	}
	for _, want := range []string{
		"agents need public_url", `name "Bad Name"`, `url "ftp://x" is not an http(s) URL`,
		`"urgent" is not a severity`, "agents.a.service", "secret_file is required", "agents.a is listed twice",
		`repository "nope"`, `workflow "a b"`, "token_file is required", "url and secret_file are for kind webhook",
		`kind must be webhook or github, not "pager"`, "changes.keep is at least 24h",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

func TestAgentSecretTooShort(t *testing.T) {
	y := full(t) + `
agents:
  - {name: a, kind: webhook, url: "https://x.example", secret_file: ` + secret(t, t.TempDir(), "s", "too-short") + `}
`
	if _, err := Parse([]byte(y)); err == nil || !strings.Contains(err.Error(), "agents.a.secret_file: the secret is too short") {
		t.Errorf("a short signing secret: %v", err)
	}
}
