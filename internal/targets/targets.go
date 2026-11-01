// Package targets is what the probes check: target groups in Prometheus file_sd form ({targets,
// labels}), in named files (sets, once they are in Beacon's database; see package configdb). A file's
// name says which probe locations check it:
//
//	all-*.yaml                every location
//	overseas-*.yaml           the overseas locations (mainland-*.yaml: the mainland ones)
//	<location id>-*.yaml      that location only
//
// The gateway hands each probe its list (Prometheus HTTP service discovery), so changing what is
// checked is a change in Beacon, not a redeployment of every probe.
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
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return &Set{files: map[string][]Group{}}, nil
	}
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // inside the targets directory
		if err != nil {
			return nil, err
		}
		files[e.Name()] = b
	}
	return Parse(files, locations)
}

// Parse is Load for the files' contents, by file name.
func Parse(files map[string][]byte, locations []string) (*Set, error) {
	groups := map[string][]Group{}
	var errs []error
	for _, name := range sortedKeys(files) {
		if !nameOK(name, locations) {
			groups[name] = nil // Build says why
			continue
		}
		var gs []Group
		dec := yaml.NewDecoder(strings.NewReader(string(files[name])))
		dec.KnownFields(true)
		if err := dec.Decode(&gs); err != nil && !errors.Is(err, io.EOF) {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		groups[name] = gs
	}
	s, err := Build(groups, locations)
	return s, errors.Join(append(errs, err)...)
}

// Build makes a set from groups by file name, checked as Load checks them: a file whose name starts
// with none of all-, a region or a location id is left out and is an error, so that a typo does not
// silently stop a check; groups without targets or without the labels module and service are errors.
func Build(files map[string][]Group, locations []string) (*Set, error) {
	s := &Set{files: map[string][]Group{}}
	var errs []error
	for _, name := range sortedKeys(files) {
		if !nameOK(name, locations) {
			errs = append(errs, fmt.Errorf("%s: the name must start with all-, %s- or a location id and a dash",
				name, strings.Join(Regions, "-, ")))
			continue
		}
		groups := files[name]
		for i, g := range groups {
			switch {
			case len(g.Targets) == 0:
				errs = append(errs, fmt.Errorf("%s: group %d has no targets", name, i+1))
			case g.Labels["module"] == "" || g.Labels["service"] == "":
				errs = append(errs, fmt.Errorf("%s: group %d (%s) needs the labels module and service", name, i+1, g.Targets[0]))
			}
		}
		s.files[name] = groups
	}
	return s, errors.Join(errs...)
}

func nameOK(name string, locations []string) bool { return scope(name, locations) != "" }

// Scope is who probes a file: all, a region or a location id, by the file's name; "" when its name
// starts with none of them.
func Scope(name string, locations []string) string { return scope(name, locations) }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Groups returns the groups of one file.
func (s *Set) Groups(file string) []Group { return s.files[file] }

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
