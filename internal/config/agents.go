package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Agent is a hook that wakes an agent. When an alert group it matches is first notified, Beacon calls
// it with the group's id and where to fetch the evidence; with Resolved, it calls again when the group
// resolves. Beacon only wakes the agent: what to make of the alert is the agent's business.
//
// Kind webhook POSTs a JSON payload to URL, signed with HMAC-SHA256 under the secret in SecretFile
// (header X-Beacon-Signature: sha256=<hex>), and/or with the headers in HeadersFile, for a receiver
// that checks a secret of its own (Authorization: Bearer …, X-Webhook-Secret: …). Kind github
// dispatches Workflow in Repository at Ref (workflow_dispatch) with the token in TokenFile, passing
// the inputs alert (the group id) and event.
type Agent struct {
	Name       string   `yaml:"name"`
	Kind       string   `yaml:"kind"`       // webhook or github
	Severities []string `yaml:"severities"` // default [critical]
	Service    string   `yaml:"service"`    // a regular expression over the whole service; empty matches any
	Resolved   bool     `yaml:"resolved"`   // also call when the group resolves

	URL         string `yaml:"url"`          // webhook
	SecretFile  string `yaml:"secret_file"`  // webhook: the signing secret
	HeadersFile string `yaml:"headers_file"` // webhook: "Name: value" lines sent with every call (the receiver's own secret)

	Repository string `yaml:"repository"` // github: owner/name
	Workflow   string `yaml:"workflow"`   // github: the workflow's file name (oncall.yml) or id
	Ref        string `yaml:"ref"`        // github: default main
	TokenFile  string `yaml:"token_file"` // github: a token that may dispatch the workflow (actions: write)
	API        string `yaml:"api"`        // github: default https://api.github.com (GitHub Enterprise, tests)

	Secret    string            `yaml:"-"` // read from secret_file or token_file
	Headers   map[string]string `yaml:"-"` // read from headers_file
	ServiceRe *regexp.Regexp    `yaml:"-"`
}

// headerNameRe is an HTTP field name (RFC 9110 token).
var headerNameRe = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

// reservedHeaders are set by Beacon itself and cannot come from headers_file.
var reservedHeaders = []string{"content-type", "content-length", "host", "user-agent", "x-beacon-signature", "x-beacon-event", "x-beacon-delivery"}

// parseHeaders reads a headers_file: one "Name: value" per line, blank lines and lines starting with
// # ignored.
func parseHeaders(b []byte) (map[string]string, error) {
	h := map[string]string{}
	for n, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		switch {
		case !ok || !headerNameRe.MatchString(name):
			return nil, fmt.Errorf("line %d is not \"Name: value\"", n+1)
		case value == "":
			return nil, fmt.Errorf("line %d: %s has no value", n+1, name)
		case strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }):
			return nil, fmt.Errorf("line %d: %s has a control character in its value", n+1, name)
		case slices.Contains(reservedHeaders, strings.ToLower(name)):
			return nil, fmt.Errorf("line %d: %s is set by Beacon itself", n+1, name)
		}
		h[name] = value
	}
	if len(h) == 0 {
		return nil, errors.New("no headers")
	}
	return h, nil
}

// Changes are the change events (deployments, Flux reconciliations) the gateway takes, kept for Keep.
type Changes struct {
	Keep Duration `yaml:"keep"`
}

var (
	agentNameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	repositoryRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	workflowRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,200}$`)
)

func (c *Config) agentDefaults() {
	if c.Changes.Keep == 0 {
		c.Changes.Keep = Duration(30 * 24 * time.Hour)
	}
	for i := range c.Agents {
		a := &c.Agents[i]
		if len(a.Severities) == 0 {
			a.Severities = []string{"critical"}
		}
		if a.Kind == "github" {
			if a.Ref == "" {
				a.Ref = "main"
			}
			if a.API == "" {
				a.API = "https://api.github.com"
			}
		}
	}
}

func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

func (c *Config) validateAgents() []error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if c.Changes.Keep.D() < 24*time.Hour {
		bad("changes.keep is at least 24h")
	}
	if len(c.Agents) > 0 && c.PublicURL == "" {
		bad("agents need public_url: the hooks tell agents where to fetch the evidence")
	}
	seen := map[string]bool{}
	for i := range c.Agents {
		a := &c.Agents[i]
		what := fmt.Sprintf("agents[%d]", i)
		if !agentNameRe.MatchString(a.Name) {
			bad("%s: name %q: use lower case letters, digits, dots, dashes and underscores", what, a.Name)
		} else {
			what = "agents." + a.Name
		}
		if seen[a.Name] {
			bad("%s is listed twice", what)
		}
		seen[a.Name] = true
		for _, s := range a.Severities {
			if !slices.Contains(Severities, s) {
				bad("%s: %q is not a severity (%s)", what, s, strings.Join(Severities, ", "))
			}
		}
		if a.Service != "" {
			re, err := regexp.Compile("^(?:" + a.Service + ")$")
			if err != nil {
				bad("%s.service: %v", what, err)
			}
			a.ServiceRe = re
		}
		switch a.Kind {
		case "webhook":
			if !httpURL(a.URL) {
				bad("%s.url %q is not an http(s) URL", what, a.URL)
			}
			if a.SecretFile == "" && a.HeadersFile == "" {
				bad("%s: secret_file or headers_file is required, so the agent can tell Beacon's calls from a forgery: "+
					"secret_file signs each call (X-Beacon-Signature), headers_file carries the receiver's own secret", what)
			}
			if a.Repository != "" || a.Workflow != "" || a.TokenFile != "" {
				bad("%s: repository, workflow and token_file are for kind github", what)
			}
		case "github":
			if !repositoryRe.MatchString(a.Repository) {
				bad("%s.repository %q is not owner/name", what, a.Repository)
			}
			if !workflowRe.MatchString(a.Workflow) {
				bad("%s.workflow %q is not a workflow file name (such as oncall.yml) or id", what, a.Workflow)
			}
			if a.TokenFile == "" {
				bad("%s.token_file is required: a token that may dispatch the workflow", what)
			}
			if !httpURL(a.API) {
				bad("%s.api %q is not an http(s) URL", what, a.API)
			}
			if a.URL != "" || a.SecretFile != "" || a.HeadersFile != "" {
				bad("%s: url, secret_file and headers_file are for kind webhook", what)
			}
		default:
			bad("%s.kind must be webhook or github, not %q", what, a.Kind)
		}
	}
	return errs
}

func (c *Config) readAgentSecrets() []error {
	var errs []error
	for i := range c.Agents {
		a := &c.Agents[i]
		path, what := a.SecretFile, "agents."+a.Name+".secret_file"
		if a.Kind == "github" {
			path, what = a.TokenFile, "agents."+a.Name+".token_file"
		}
		if a.Kind == "webhook" && a.HeadersFile != "" {
			hwhat := "agents." + a.Name + ".headers_file"
			b, err := os.ReadFile(a.HeadersFile)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", hwhat, err))
			} else if a.Headers, err = parseHeaders(b); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", hwhat, err))
			}
		}
		if path == "" {
			continue
		}
		b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", what, err))
			continue
		}
		a.Secret = strings.TrimSpace(string(b))
		switch {
		case a.Secret == "":
			errs = append(errs, fmt.Errorf("%s: %s is empty", what, path))
		case a.Kind == "webhook" && len(a.Secret) < 24:
			errs = append(errs, errors.New(what+": the secret is too short; use at least 24 random characters"))
		}
	}
	return errs
}
