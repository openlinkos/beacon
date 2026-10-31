// Package targets loads what the probes check: target groups in Prometheus file_sd form ({targets,
// labels}), in the YAML files of a directory. A file's name says which probe locations check it:
//
//	all-*.yaml                every location
//	overseas-*.yaml           the overseas locations (mainland-*.yaml: the mainland ones)
//	<location id>-*.yaml      that location only
//
// The gateway hands each probe its list (Prometheus HTTP service discovery), so changing what is
// checked is a change here and a reload, not a redeployment of every probe.
package targets

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Group is a list of targets with the labels they share. Every group needs the labels module (how
// to probe) and service (what the result belongs to).
type Group struct {
	Targets []string          `yaml:"targets" json:"targets"`
	Labels  map[string]string `yaml:"labels" json:"labels"`
}

// Settings is what the gateway tells one probe agent besides its targets and modules
// (GET /api/v1/agent): the location it probes as and how, from the catalogue.
type Settings struct {
	Location string `json:"location"`
	// ProbeTimeoutSeconds is the least time any probe gets (0: the modules' own timeouts).
	ProbeTimeoutSeconds float64 `json:"probe_timeout_seconds,omitempty"`
	// ProbeRetries is how many more times a probe that failed without an answer is tried at once.
	ProbeRetries int `json:"probe_retries,omitempty"`
}

// Set is the loaded target files, by file name.
type Set struct {
	files map[string][]Group
}

// Regions are the region prefixes a file name may start with.
var Regions = []string{"overseas", "mainland"}

// Load reads every *.yaml file in dir. A missing directory is an empty set. locations are the known
// location ids: a file whose name starts with none of all-, a region or a location id is an error,
// so that a typo does not silently stop a check.
func Load(dir string, locations []string) (*Set, error) {
	s := &Set{files: map[string][]Group{}}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		if scope(e.Name(), locations) == "" {
			errs = append(errs, fmt.Errorf("%s: the name must start with all-, %s- or a location id and a dash",
				e.Name(), strings.Join(Regions, "-, ")))
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // inside the targets directory
		if err != nil {
			return nil, err
		}
		var groups []Group
		dec := yaml.NewDecoder(strings.NewReader(string(b)))
		dec.KnownFields(true)
		if err := dec.Decode(&groups); err != nil && !errors.Is(err, io.EOF) {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		for i, g := range groups {
			switch {
			case len(g.Targets) == 0:
				errs = append(errs, fmt.Errorf("%s: group %d has no targets", e.Name(), i+1))
			case g.Labels["module"] == "" || g.Labels["service"] == "":
				errs = append(errs, fmt.Errorf("%s: group %d (%s) needs the labels module and service", e.Name(), i+1, g.Targets[0]))
			}
		}
		s.files[e.Name()] = groups
	}
	return s, errors.Join(errs...)
}

// scope is the file name's prefix (all, a region or a location id), or "" when it has none. The
// longest matching location id wins, so cdn-pre-us1-x.yaml is not taken for a location cdn-pre.
func scope(name string, locations []string) string {
	for _, p := range append([]string{"all"}, Regions...) {
		if strings.HasPrefix(name, p+"-") {
			return p
		}
	}
	best := ""
	for _, id := range locations {
		if strings.HasPrefix(name, id+"-") && len(id) > len(best) {
			best = id
		}
	}
	return best
}

// For is the target groups the location checks, from its files in name order.
func (s *Set) For(location, region string, locations []string) []Group {
	names := make([]string, 0, len(s.files))
	for n := range s.files {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []Group{}
	for _, n := range names {
		switch scope(n, locations) {
		case "all", region, location:
			out = append(out, s.files[n]...)
		}
	}
	return out
}

// Files lists the file names.
func (s *Set) Files() []string {
	out := make([]string, 0, len(s.files))
	for n := range s.files {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// Count is the number of targets over all files.
func (s *Set) Count() int {
	n := 0
	for _, gs := range s.files {
		for _, g := range gs {
			n += len(g.Targets)
		}
	}
	return n
}
