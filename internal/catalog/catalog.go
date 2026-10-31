// Package catalog loads what is monitored and where from: the probe locations, the heartbeats that
// jobs must send, and the resources (servers, domains, certificates, tokens) whose expiry dates are
// watched. The files live in git (the ops repository) and are mounted read-only.
//
// The catalogue is also exported as metrics, so that alert rules can join against it: a location
// that should report but does not, a heartbeat older than its limit, a resource close to expiry.
package catalog

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/metrics"
)

// Location is a probe location. ID is internal (metrics, alert cards, configuration) and is the name of the
// probe's machine: when the resources list a vps of that name, the location is expected to report only until
// it expires, so a server that is let go stops counting as a probe on its own, and renewing it (a new date)
// brings the probe back. Name is public, for the status page: it says where and on which network, never
// which vendor or machine.
//
// A location on a CDN node may also name the node's Address and its cluster's DNS name (Rotation): the
// inventory check then reports when the node drops out of the cluster's answers. A Standby node joins the
// rotation only when another fails, so for it being out is normal and being in is the news.
//
// Probe changes how the location's agent probes, for a machine whose path is slower or lossier than the
// others'; the gateway hands it to that agent.
type Location struct {
	ID       string        `yaml:"id"`
	Name     string        `yaml:"name"`
	Region   string        `yaml:"region"` // overseas or mainland
	Network  string        `yaml:"network"`
	Address  string        `yaml:"address"`
	Rotation string        `yaml:"rotation"`
	Standby  bool          `yaml:"standby"`
	Probe    ProbeSettings `yaml:"probe"`
}

// ProbeSettings change how one location's agent probes. Zero values keep the modules' own settings.
type ProbeSettings struct {
	// Timeout is the least time any probe gets at this location: a module with a shorter timeout gets
	// this one. At most a minute, the agent's interval.
	Timeout config.Duration `yaml:"timeout"`
	// Retries is how many more times (at most 2) a probe is tried at once when it fails without an
	// answer (DNS, connect, timeout), for a machine whose connections drop now and then. A failure with
	// an answer, such as a wrong status code, is never retried.
	Retries int `yaml:"retries"`
}

// Heartbeat is a job that must report success at least every MaxAge.
type Heartbeat struct {
	Name        string          `yaml:"name"`
	MaxAge      config.Duration `yaml:"max_age"`
	Severity    string          `yaml:"severity"`
	Description string          `yaml:"description"`
}

// Resource is something that expires: a server, a domain, a certificate, a token.
type Resource struct {
	Name    string `yaml:"name"`
	Kind    string `yaml:"kind"`
	Expires string `yaml:"expires"` // YYYY-MM-DD
	Note    string `yaml:"note"`
	expires time.Time
}

// ExpiresAt is the expiry date (midnight UTC).
func (r Resource) ExpiresAt() time.Time { return r.expires }

// Catalog is the loaded catalogue.
type Catalog struct {
	Locations  []Location  `yaml:"locations"`
	Heartbeats []Heartbeat `yaml:"heartbeats"`
	Resources  []Resource  `yaml:"resources"`
}

var (
	idRe        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	resourceRes = []string{"vps", "domain", "cert", "token", "service"}
	now         = time.Now // replaced in tests
)

// Load reads every *.yaml file in dir and merges them. A missing directory is an empty catalogue.
func Load(dir string) (*Catalog, error) {
	c := &Catalog{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // inside the catalogue directory
		if err != nil {
			return nil, err
		}
		var part Catalog
		dec := yaml.NewDecoder(strings.NewReader(string(b)))
		dec.KnownFields(true)
		if err := dec.Decode(&part); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		c.Locations = append(c.Locations, part.Locations...)
		c.Heartbeats = append(c.Heartbeats, part.Heartbeats...)
		c.Resources = append(c.Resources, part.Resources...)
	}
	return c, c.validate()
}

func (c *Catalog) validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	seen := map[string]bool{}
	for _, l := range c.Locations {
		if !idRe.MatchString(l.ID) {
			bad("location %q: the id must be lower case letters, digits and dashes", l.ID)
		}
		if seen["l:"+l.ID] {
			bad("location %q is listed twice", l.ID)
		}
		seen["l:"+l.ID] = true
		if l.Name == "" {
			bad("location %q needs a public name such as \"United States - Los Angeles\"", l.ID)
		}
		if l.Region != "overseas" && l.Region != "mainland" {
			bad("location %q: region must be overseas or mainland, not %q", l.ID, l.Region)
		}
		if l.Rotation != "" && l.Address == "" {
			bad("location %q: a rotation needs the node's address", l.ID)
		}
		if l.Standby && l.Rotation == "" {
			bad("location %q: standby means standby in a rotation; name the rotation", l.ID)
		}
		if d := l.Probe.Timeout.D(); d < 0 || d > time.Minute {
			bad("location %q: probe.timeout must be between 0 and 1m (the agent probes every minute)", l.ID)
		}
		if l.Probe.Retries < 0 || l.Probe.Retries > 2 {
			bad("location %q: probe.retries must be 0, 1 or 2", l.ID)
		}
	}
	for _, h := range c.Heartbeats {
		if !idRe.MatchString(h.Name) {
			bad("heartbeat %q: the name must be lower case letters, digits and dashes", h.Name)
		}
		if seen["h:"+h.Name] {
			bad("heartbeat %q is listed twice", h.Name)
		}
		seen["h:"+h.Name] = true
		if h.MaxAge.D() < time.Minute {
			bad("heartbeat %q: max_age must be at least 1m", h.Name)
		}
		if !slices.Contains(config.Severities, h.Severity) {
			bad("heartbeat %q: severity must be one of %s", h.Name, strings.Join(config.Severities, ", "))
		}
	}
	for i := range c.Resources {
		r := &c.Resources[i]
		if r.Name == "" {
			bad("a resource has no name")
		}
		if seen["r:"+r.Name] {
			bad("resource %q is listed twice", r.Name)
		}
		seen["r:"+r.Name] = true
		if !slices.Contains(resourceRes, r.Kind) {
			bad("resource %q: kind must be one of %s", r.Name, strings.Join(resourceRes, ", "))
		}
		t, err := time.Parse(time.DateOnly, r.Expires)
		if err != nil {
			bad("resource %q: expires %q is not a date such as 2026-11-27", r.Name, r.Expires)
		}
		r.expires = t
	}
	return errors.Join(errs...)
}

func (c *Catalog) resource(name string) (Resource, bool) {
	for _, r := range c.Resources {
		if r.Name == name {
			return r, true
		}
	}
	return Resource{}, false
}

// Expected reports whether the location should be reporting at t: always, unless the resources list a vps
// of the same name that has expired (the whole expiry day still counts).
func (c *Catalog) Expected(l Location, t time.Time) bool {
	r, ok := c.resource(l.ID)
	return !ok || r.Kind != "vps" || t.Before(r.expires.AddDate(0, 0, 1))
}

// Location returns the location with id.
func (c *Catalog) Location(id string) (Location, bool) {
	for _, l := range c.Locations {
		if l.ID == id {
			return l, true
		}
	}
	return Location{}, false
}

// Register exports the catalogue as metrics. cat is read on every scrape, so a reload takes effect.
func Register(m *metrics.Metrics, cat func() *Catalog) {
	m.Register("linkos_expected_location", "Probe locations that should report (1 each; not once their machine has expired)", "gauge", func() []metrics.Sample {
		var out []metrics.Sample
		c, t := cat(), now()
		for _, l := range c.Locations {
			if c.Expected(l, t) {
				out = append(out, metrics.Sample{Labels: metrics.Labels("location", l.ID, "region", l.Region, "network", l.Network), Value: 1})
			}
		}
		return out
	})
	m.Register("linkos_heartbeat_max_age_seconds", "How old a heartbeat may get before it alerts", "gauge", func() []metrics.Sample {
		var out []metrics.Sample
		for _, h := range cat().Heartbeats {
			out = append(out, metrics.Sample{Labels: metrics.Labels("heartbeat", h.Name, "severity", h.Severity), Value: h.MaxAge.D().Seconds()})
		}
		return out
	})
	m.Register("linkos_resource_expiry_timestamp_seconds", "When a server, domain, certificate or token expires", "gauge", func() []metrics.Sample {
		var out []metrics.Sample
		for _, r := range cat().Resources {
			out = append(out, metrics.Sample{Labels: metrics.Labels("resource", r.Name, "kind", r.Kind), Value: float64(r.expires.Unix())})
		}
		return out
	})
}
