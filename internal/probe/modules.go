// Package probe checks targets from a probe location, the way the Prometheus Blackbox Exporter does
// and with the same metric names (probe_success, probe_duration_seconds, probe_http_status_code,
// probe_ssl_earliest_cert_expiry), so that alert rules work with either. It adds what a status service
// needs: every failure says why (probe_failure_info{reason}), and multi-step checks for a container
// registry (token, then manifest), a Git repository over HTTP and a domain's expiry (RDAP).
//
// The module file is the Blackbox Exporter's format for the http and tcp probers (the subset this
// package implements; anything else is an error rather than silently ignored), plus the registry, git
// and rdap probers.
package probe

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Probers are the kinds of check.
var Probers = []string{"http", "tcp", "registry", "git", "rdap"}

// Modules are the named ways to probe a target.
type Modules struct {
	Modules map[string]*Module `yaml:"modules"`
}

// Module is one way to probe a target.
type Module struct {
	Prober   string         `yaml:"prober"`
	Timeout  Duration       `yaml:"timeout"`
	HTTP     HTTPModule     `yaml:"http"`
	TCP      TCPModule      `yaml:"tcp"`
	Registry RegistryModule `yaml:"registry"`
	Git      GitModule      `yaml:"git"`
	RDAP     RDAPModule     `yaml:"rdap"`
}

// IPConfig chooses the address family: ip4 or ip6 (default ip6, as in the Blackbox Exporter), with or
// without falling back to the other one.
type IPConfig struct {
	PreferredIPProtocol string `yaml:"preferred_ip_protocol"`
	IPProtocolFallback  *bool  `yaml:"ip_protocol_fallback"`
}

// HTTPModule is an HTTP request and what its answer must look like. Without valid_status_codes any
// 2xx is success.
type HTTPModule struct {
	IPConfig                   `yaml:",inline"`
	Method                     string            `yaml:"method"`
	ValidStatusCodes           []int             `yaml:"valid_status_codes"`
	FollowRedirects            *bool             `yaml:"follow_redirects"`
	FailIfNotSSL               bool              `yaml:"fail_if_not_ssl"`
	FailIfBodyNotMatchesRegexp []string          `yaml:"fail_if_body_not_matches_regexp"`
	Headers                    map[string]string `yaml:"headers"`

	bodyRe []*regexp.Regexp
}

// TCPModule connects, optionally over TLS, and checks what the server says.
type TCPModule struct {
	IPConfig      `yaml:",inline"`
	TLS           bool            `yaml:"tls"`
	QueryResponse []QueryResponse `yaml:"query_response"`
}

// QueryResponse sends a line, or waits for a line matching Expect.
type QueryResponse struct {
	Expect string `yaml:"expect"`
	Send   string `yaml:"send"`

	re *regexp.Regexp
}

// RegistryModule pulls a manifest the way a container client does: /v2/, then a token from the
// realm the registry names (anonymously, or with the credentials), then HEAD the manifest. Nothing
// is downloaded. The target is the registry's base URL.
type RegistryModule struct {
	IPConfig     `yaml:",inline"`
	Repository   string `yaml:"repository"` // such as library/busybox
	Reference    string `yaml:"reference"`  // a tag or digest; default latest
	Username     string `yaml:"username"`
	PasswordFile string `yaml:"password_file"`
}

// GitModule asks a repository for its refs over smart HTTP (what git clone does first). The target is
// the repository URL.
type GitModule struct {
	IPConfig     `yaml:",inline"`
	Username     string `yaml:"username"`
	PasswordFile string `yaml:"password_file"`
}

// RDAPModule looks up a domain's expiry. The target is the domain.
type RDAPModule struct {
	IPConfig `yaml:",inline"`
	Server   string `yaml:"server"` // default https://rdap.org, which redirects to the registry's server
}

// Duration is a time.Duration written as "20s" in YAML.
type Duration time.Duration

// UnmarshalYAML reads "20s".
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %q is not a duration such as 20s", n.Line, n.Value)
	}
	*d = Duration(v)
	return nil
}

// LoadModules reads and checks a module file.
func LoadModules(path string) (*Modules, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
	if err != nil {
		return nil, err
	}
	return ParseModules(b)
}

// ParseModules is LoadModules without the file.
func ParseModules(b []byte) (*Modules, error) {
	ms := &Modules{}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(ms); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("modules: %w", err)
	}
	var errs []error
	for name, m := range ms.Modules {
		if m == nil {
			errs = append(errs, fmt.Errorf("module %s is empty", name))
			continue
		}
		if err := m.check(); err != nil {
			errs = append(errs, fmt.Errorf("module %s: %w", name, err))
		}
	}
	return ms, errors.Join(errs...)
}

func (m *Module) check() error {
	if !slices.Contains(Probers, m.Prober) {
		return fmt.Errorf("prober must be one of %s, not %q", strings.Join(Probers, ", "), m.Prober)
	}
	if m.Timeout == 0 {
		m.Timeout = Duration(10 * time.Second)
	}
	for _, ip := range []IPConfig{m.HTTP.IPConfig, m.TCP.IPConfig, m.Registry.IPConfig, m.Git.IPConfig, m.RDAP.IPConfig} {
		if p := ip.PreferredIPProtocol; p != "" && p != "ip4" && p != "ip6" {
			return fmt.Errorf("preferred_ip_protocol must be ip4 or ip6, not %q", p)
		}
	}
	for _, s := range m.HTTP.FailIfBodyNotMatchesRegexp {
		re, err := regexp.Compile(s)
		if err != nil {
			return fmt.Errorf("fail_if_body_not_matches_regexp %q: %w", s, err)
		}
		m.HTTP.bodyRe = append(m.HTTP.bodyRe, re)
	}
	for i := range m.TCP.QueryResponse {
		q := &m.TCP.QueryResponse[i]
		if q.Expect != "" {
			re, err := regexp.Compile(q.Expect)
			if err != nil {
				return fmt.Errorf("query_response expect %q: %w", q.Expect, err)
			}
			q.re = re
		}
	}
	if m.Prober == "registry" && m.Registry.Repository == "" {
		return errors.New("a registry probe needs registry.repository")
	}
	if m.HTTP.Method == "" {
		m.HTTP.Method = "GET"
	}
	if m.Registry.Reference == "" {
		m.Registry.Reference = "latest"
	}
	if m.RDAP.Server == "" {
		m.RDAP.Server = "https://rdap.org"
	}
	return nil
}
