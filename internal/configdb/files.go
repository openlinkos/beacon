package configdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/probe"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

// Files is a configuration as files, by their path in an import or export directory, with forward
// slashes: catalog/*.yaml (the catalogue, which may be split over several files), targets/*.yaml (one
// file per target set, named as the set) and targets/modules.yml (the module file). It is the layout
// a deployment has always had in /etc/linkos-beacon.
type Files map[string]string

// ModulesFile is the module file's path in Files.
const ModulesFile = "targets/modules.yml"

// fileIn reports the directory (catalog or targets) and the name of a *.yaml file at path, or false.
func fileIn(path string) (dir, name string, ok bool) {
	dir, name, _ = strings.Cut(path, "/")
	if (dir != "catalog" && dir != "targets") || !strings.HasSuffix(name, ".yaml") || len(name) == len(".yaml") ||
		strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") || strings.ContainsFunc(name, unicode.IsControl) {
		return "", "", false
	}
	return dir, name, true
}

func badPath(p string) error {
	return fmt.Errorf("%w: %s: an import holds catalog/*.yaml, targets/*.yaml and %s", ErrBadInput, p, ModulesFile)
}

// entries checks the files with the checks they always had, and returns what the database holds.
func (f Files) entries() (map[string][]byte, error) {
	cat, tgt := map[string][]byte{}, map[string][]byte{}
	var mods []byte
	hasMods := false
	for _, p := range sortedKeys(f) {
		if p == ModulesFile {
			mods, hasMods = []byte(f[p]), true
			continue
		}
		dir, name, ok := fileIn(p)
		switch {
		case !ok:
			return nil, badPath(p)
		case dir == "catalog":
			cat[name] = []byte(f[p])
		default:
			tgt[name] = []byte(f[p])
		}
	}
	c, err := catalog.Parse(cat)
	if c == nil {
		return nil, fmt.Errorf("%w: catalogue: %w", ErrInvalid, err)
	}
	var errs []error
	if err != nil {
		errs = append(errs, fmt.Errorf("catalogue: %w", err))
	}
	set, err := targets.Parse(tgt, locationIDs(c))
	if err != nil {
		errs = append(errs, fmt.Errorf("targets: %w", err))
	}
	if hasMods {
		ms, err := probe.ParseModules(mods)
		if err == nil {
			err = probe.CheckSecretFiles(ms)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ModulesFile, err))
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, errors.Join(errs...))
	}
	out := map[string][]byte{}
	put := func(key string, v any) {
		b, _ := json.Marshal(v)
		out[key] = b
	}
	for _, l := range c.Locations {
		put(entryKey(KindLocation, l.ID), l)
	}
	for _, h := range c.Heartbeats {
		put(entryKey(KindHeartbeat, h.Name), h)
	}
	for _, r := range c.Resources {
		put(entryKey(KindResource, r.Name), r)
	}
	for _, file := range set.Files() {
		gs := set.Groups(file)
		if gs == nil {
			gs = []targets.Group{}
		}
		put(entryKey(KindTargets, strings.TrimSuffix(file, ".yaml")), gs)
	}
	if hasMods {
		put(modulesKey, string(mods))
	}
	return out, nil
}

// ParseFiles checks files as an import does and returns the configuration they make.
func ParseFiles(f Files) (*State, error) {
	e, err := f.entries()
	if err != nil {
		return nil, err
	}
	st := build(e)
	return st, st.problem
}

// Import makes the configuration the one in the files, as one change: what the files do not hold is
// removed. With dryRun it only works out the change.
func (s *Service) Import(ctx context.Context, who Actor, f Files, note string, dryRun bool) (store.ConfigChange, error) {
	next, err := f.entries()
	if err != nil {
		return store.ConfigChange{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commit(ctx, who, "import", note, 0, next, dryRun)
}

// Signal is who imports the files when the service gets SIGHUP.
var Signal = Actor{Name: "linkos-beacon", Via: "signal"}

// ImportOnSignal imports the files on SIGHUP, but only when nothing was changed through Beacon since
// the last import, so that files cannot silently undo what people and agents changed. Otherwise it
// says how to go on.
func (s *Service) ImportOnSignal(ctx context.Context, f Files) (store.ConfigChange, error) {
	next, err := f.entries()
	if err != nil {
		return store.ConfigChange{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if last := s.d.Store.ConfigChanges(0, 1, nil); len(last) == 1 && last[0].Action != "import" {
		l := last[0]
		return store.ConfigChange{}, fmt.Errorf("%w through Beacon since the last import (version %d: %s by %s via %s); "+
			"keep it with linkos-beacon config export DIR, or replace it with linkos-beacon config import DIR", ErrConflict, l.Version, l.Action, l.Actor, l.Via)
	}
	return s.commit(ctx, Signal, "import", "", 0, next, false)
}

// Export writes the configuration in force as files, in the layout Import reads, so that importing
// them changes nothing. Lists are in the order of their ids and names; comments in the files the
// catalogue and targets were imported from are not kept (the module file keeps its own).
func (s *Service) Export() (Files, int64, error) {
	v, err := s.Get("", "")
	if err != nil {
		return nil, 0, err
	}
	f := Files{}
	write := func(path string, x any) error {
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(x); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if err := enc.Close(); err != nil {
			return err
		}
		f[path] = buf.String()
		return nil
	}
	var errs []error
	if len(v.Locations) > 0 {
		errs = append(errs, write("catalog/locations.yaml", catalog.Catalog{Locations: v.Locations}))
	}
	if len(v.Heartbeats) > 0 {
		errs = append(errs, write("catalog/heartbeats.yaml", catalog.Catalog{Heartbeats: v.Heartbeats}))
	}
	if len(v.Resources) > 0 {
		errs = append(errs, write("catalog/resources.yaml", catalog.Catalog{Resources: v.Resources}))
	}
	for _, t := range v.Targets {
		errs = append(errs, write("targets/"+t.Name+".yaml", t.Groups))
	}
	if m := s.Modules(); m != nil {
		f[ModulesFile] = string(m)
	}
	return f, v.Version, errors.Join(errs...)
}

// ReadFiles reads a configuration where the service's configuration file names it: every *.yaml in
// catalogDir and targetsDir, and the module file. What does not exist is left out.
func ReadFiles(catalogDir, targetsDir, modulesFile string) (Files, error) {
	f := Files{}
	for _, d := range []struct{ dir, as string }{{catalogDir, "catalog/"}, {targetsDir, "targets/"}} {
		entries, err := os.ReadDir(d.dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(d.dir, e.Name()))
			if err != nil {
				return nil, err
			}
			f[d.as+e.Name()] = string(b)
		}
	}
	b, err := os.ReadFile(modulesFile) //nolint:gosec // the operator names the file
	switch {
	case err == nil:
		f[ModulesFile] = string(b)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	return f, nil
}

// ReadDir reads an import directory: dir/catalog/*.yaml, dir/targets/*.yaml, dir/targets/modules.yml.
func ReadDir(dir string) (Files, error) {
	if fi, err := os.Stat(dir); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	f, err := ReadFiles(filepath.Join(dir, "catalog"), filepath.Join(dir, "targets"), filepath.Join(dir, filepath.FromSlash(ModulesFile)))
	if err == nil && len(f) == 0 {
		err = fmt.Errorf("%s holds no configuration: an import directory has catalog/*.yaml, targets/*.yaml and %s", dir, ModulesFile)
	}
	return f, err
}

// WriteDir writes an export into dir, in the layout ReadDir reads, and removes the *.yaml files (and
// the module file) there that the export does not hold, so that importing dir again gives the same
// configuration. It returns the paths written and removed.
func WriteDir(dir string, f Files) (written, removed []string, err error) {
	for p := range f {
		if _, _, ok := fileIn(p); !ok && p != ModulesFile {
			return nil, nil, badPath(p)
		}
	}
	for _, sub := range []string{"catalog", "targets"} {
		d := filepath.Join(dir, sub)
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, nil, err
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			return nil, nil, err
		}
		for _, e := range entries {
			p := sub + "/" + e.Name()
			_, _, isYAML := fileIn(p)
			if !e.Type().IsRegular() || (!isYAML && p != ModulesFile) {
				continue
			}
			if _, keep := f[p]; !keep {
				if err := os.Remove(filepath.Join(d, e.Name())); err != nil {
					return nil, nil, err
				}
				removed = append(removed, p)
			}
		}
	}
	for _, p := range sortedKeys(f) {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), []byte(f[p]), 0o640); err != nil { //nolint:gosec // the operator names the directory
			return nil, nil, err
		}
		written = append(written, p)
	}
	slices.Sort(removed)
	return written, removed, nil
}
