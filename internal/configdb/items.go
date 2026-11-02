package configdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/probe"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

// The store holds one entry per location, heartbeat, resource and target set, under kind/key, as JSON
// in the shape of its file; and the module file as it is, as a JSON string, under modulesKey. Modules
// are items in their own right for the API and the history, but they live in that file, so that
// probes get it byte for byte as it was imported.
const modulesKey = "modules"

func entryKey(kind, key string) string { return kind + "/" + key }

// build makes the state from entries. It is always usable; problem says why it does not pass the
// checks, if it does not.
func build(entries map[string][]byte) *State {
	st := &State{entries: entries, Catalog: &catalog.Catalog{}}
	var errs []error
	sets := map[string][]targets.Group{}
	for _, k := range sortedKeys(entries) {
		v := entries[k]
		if k == modulesKey {
			var text string
			if err := json.Unmarshal(v, &text); err != nil {
				errs = append(errs, fmt.Errorf("the module file: %w", err))
				continue
			}
			st.Modules = []byte(text)
			continue
		}
		kind, key, _ := strings.Cut(k, "/")
		var err error
		switch kind {
		case KindLocation:
			var l catalog.Location
			if err = json.Unmarshal(v, &l); err == nil {
				st.Catalog.Locations = append(st.Catalog.Locations, l)
			}
		case KindHeartbeat:
			var h catalog.Heartbeat
			if err = json.Unmarshal(v, &h); err == nil {
				st.Catalog.Heartbeats = append(st.Catalog.Heartbeats, h)
			}
		case KindResource:
			var r catalog.Resource
			if err = json.Unmarshal(v, &r); err == nil {
				st.Catalog.Resources = append(st.Catalog.Resources, r)
			}
		case KindTargets:
			var gs []targets.Group
			if err = json.Unmarshal(v, &gs); err == nil {
				sets[key+".yaml"] = gs
			}
		case KindStatus:
			var p catalog.StatusPage
			if err = json.Unmarshal(v, &p); err == nil {
				st.Catalog.StatusPages = append(st.Catalog.StatusPages, p)
			}
		default:
			err = errors.New("not a kind of item Beacon knows")
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", kind, key, err))
		}
	}
	if err := st.Catalog.Validate(); err != nil {
		errs = append(errs, err)
	}
	set, err := targets.Build(sets, locationIDs(st.Catalog))
	st.Targets = set
	if err != nil {
		errs = append(errs, err)
	}
	if st.Modules != nil {
		ms, err := probe.ParseModules(st.Modules)
		if err == nil {
			err = probe.CheckSecretFiles(ms) // a module reads files only from the probe's secrets directory
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	st.problem = errors.Join(errs...)
	return st
}

func locationIDs(c *catalog.Catalog) []string {
	ids := make([]string, 0, len(c.Locations))
	for _, l := range c.Locations {
		ids = append(ids, l.ID)
	}
	return ids
}

func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }

type itemKey struct{ kind, key string }

// items is every item with its value as JSON: the entries, and each module of the module file. A
// module file that cannot be read has no modules here; the checks report it.
func items(entries map[string][]byte) map[itemKey][]byte {
	out := map[itemKey][]byte{}
	for k, v := range entries {
		if k == modulesKey {
			for _, m := range modulesOf(entries) {
				out[itemKey{KindModule, m.name}] = m.value
			}
			continue
		}
		kind, key, _ := strings.Cut(k, "/")
		out[itemKey{kind, key}] = v
	}
	return out
}

// modulesOf lists the modules of the module file in entries, in the file's order.
func modulesOf(entries map[string][]byte) []namedModule {
	mods, _ := moduleList(moduleText(entries))
	return mods
}

func moduleText(entries map[string][]byte) []byte {
	var text string
	if v, ok := entries[modulesKey]; ok && json.Unmarshal(v, &text) == nil {
		return []byte(text)
	}
	return nil
}

// diff lists the items whose values differ between two sets of entries, by kind and key.
func diff(old, next map[string][]byte) []store.ConfigItem {
	a, b := items(old), items(next)
	keys := slices.Collect(maps.Keys(a))
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(x, y itemKey) int {
		if c := slices.Index(kinds, x.kind) - slices.Index(kinds, y.kind); c != 0 {
			return c
		}
		return strings.Compare(x.key, y.key)
	})
	out := []store.ConfigItem{}
	for _, k := range keys {
		va, ina := a[k]
		vb, inb := b[k]
		switch {
		case !ina:
			out = append(out, store.ConfigItem{Kind: k.kind, Key: k.key, Op: "added", After: vb})
		case !inb:
			out = append(out, store.ConfigItem{Kind: k.kind, Key: k.key, Op: "removed", Before: va})
		case !bytes.Equal(va, vb):
			out = append(out, store.ConfigItem{Kind: k.kind, Key: k.key, Op: "changed", Before: va, After: vb})
		}
	}
	return out
}

// normalKey is an item's key as stored: a target set may be named by its file name, all-web.yaml.
func normalKey(kind, key string) string {
	if kind == KindTargets {
		return strings.TrimSuffix(key, ".yaml")
	}
	return key
}

// checkKey refuses keys that cannot name an item, or a file for a target set, before the checks of
// the item itself (an id's form, a set name's prefix) say more.
func checkKey(kind, key string) error {
	switch {
	case key == "":
		return fmt.Errorf("%w: name the %s", ErrBadInput, kind)
	case len(key) > 200 || !utf8.ValidString(key) || strings.ContainsFunc(key, unicode.IsControl):
		return fmt.Errorf("%w: %q cannot name a %s", ErrBadInput, key, kind)
	case kind == KindTargets && (strings.ContainsAny(key, `/\`) || strings.HasPrefix(key, ".")):
		return fmt.Errorf("%w: a target set's name is a file name without .yaml, such as all-web", ErrBadInput)
	}
	return nil
}

// set puts one item into entries, given as JSON in the shape of its file; nil removes it. The item is
// read strictly (unknown fields are refused, as in the files) and stored in a canonical form.
func set(entries map[string][]byte, kind, key string, raw json.RawMessage) error {
	if err := checkKey(kind, key); err != nil {
		return err
	}
	if kind == KindModule {
		text, err := setModule(moduleText(entries), key, raw)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(string(text))
		entries[modulesKey] = b
		return nil
	}
	k := entryKey(kind, key)
	if raw == nil {
		delete(entries, k)
		return nil
	}
	var v any
	same := func(field, got string) error {
		if got != key {
			return fmt.Errorf("%w: the %s's %s is %q, not %q as in the path", ErrBadInput, kind, field, got, key)
		}
		return nil
	}
	switch kind {
	case KindLocation:
		var l catalog.Location
		if err := strict(raw, kind, &l); err != nil {
			return err
		}
		if l.ID == "" {
			l.ID = key
		}
		if err := same("id", l.ID); err != nil {
			return err
		}
		v = l
	case KindHeartbeat:
		var h catalog.Heartbeat
		if err := strict(raw, kind, &h); err != nil {
			return err
		}
		if h.Name == "" {
			h.Name = key
		}
		if err := same("name", h.Name); err != nil {
			return err
		}
		v = h
	case KindResource:
		var r catalog.Resource
		if err := strict(raw, kind, &r); err != nil {
			return err
		}
		if r.Name == "" {
			r.Name = key
		}
		if err := same("name", r.Name); err != nil {
			return err
		}
		v = r
	case KindTargets:
		gs := []targets.Group{}
		if err := strict(raw, "target set (a list of groups such as [{\"targets\": [...], \"labels\": {...}}])", &gs); err != nil {
			return err
		}
		if gs == nil {
			gs = []targets.Group{}
		}
		v = gs
	case KindStatus:
		p, err := statusPage(raw, key)
		if err != nil {
			return err
		}
		v = p
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrBadInput, kind)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	entries[k] = b
	return nil
}

// strict reads JSON into v, refusing unknown fields and anything after the value.
func strict(raw json.RawMessage, what string, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: not a %s: %v", ErrBadInput, what, err) //nolint:errorlint // the decoder's error is detail
	}
	if dec.More() {
		return fmt.Errorf("%w: not a %s: more after the value", ErrBadInput, what)
	}
	return nil
}

// TargetSet is a named list of target groups, what a file in the targets directory held. Its name's
// prefix says which locations probe it (ProbedBy): all, overseas, mainland or a location id.
type TargetSet struct {
	Name     string          `json:"name"`
	ProbedBy string          `json:"probed_by"`
	Groups   []targets.Group `json:"groups"`
}

// View is the configuration, or a part of it, in the shapes of its files, with the version in force.
type View struct {
	Version     int64                `json:"version"`
	Locations   []catalog.Location   `json:"locations,omitzero"`
	Heartbeats  []catalog.Heartbeat  `json:"heartbeats,omitzero"`
	Resources   []catalog.Resource   `json:"resources,omitzero"`
	Targets     []TargetSet          `json:"targets,omitzero"`
	Modules     map[string]any       `json:"modules,omitzero"`
	StatusPages []catalog.StatusPage `json:"status_pages,omitzero"`
}

// Get returns the configuration (kind ""), the list of one kind, or one item (kind and key) in its list.
func (s *Service) Get(kind, key string) (View, error) {
	if kind != "" {
		k, ok := Kind(kind)
		if !ok {
			return View{}, fmt.Errorf("%w: "+kindsHint, ErrBadInput)
		}
		kind, key = k, normalKey(k, key)
	} else if key != "" {
		return View{}, fmt.Errorf("%w: name the kind of the item too", ErrBadInput)
	}
	st := s.state.Load()
	want := func(k string) bool { return kind == "" || kind == k }
	pick := func(k string) bool { return key == "" || key == k }
	v := View{Version: st.Version}
	found := false
	decode := func(entry string, into any) bool {
		if json.Unmarshal(st.entries[entry], into) != nil {
			return false
		}
		found = true
		return true
	}
	if want(KindLocation) {
		v.Locations = []catalog.Location{}
	}
	if want(KindHeartbeat) {
		v.Heartbeats = []catalog.Heartbeat{}
	}
	if want(KindResource) {
		v.Resources = []catalog.Resource{}
	}
	if want(KindTargets) {
		v.Targets = []TargetSet{}
	}
	if want(KindStatus) {
		v.StatusPages = []catalog.StatusPage{}
	}
	ids := locationIDs(st.Catalog)
	for _, e := range sortedKeys(st.entries) {
		k, name, _ := strings.Cut(e, "/")
		if !want(k) || !pick(name) {
			continue
		}
		switch k {
		case KindLocation:
			var l catalog.Location
			if decode(e, &l) {
				v.Locations = append(v.Locations, l)
			}
		case KindHeartbeat:
			var h catalog.Heartbeat
			if decode(e, &h) {
				v.Heartbeats = append(v.Heartbeats, h)
			}
		case KindResource:
			var r catalog.Resource
			if decode(e, &r) {
				v.Resources = append(v.Resources, r)
			}
		case KindTargets:
			var gs []targets.Group
			if decode(e, &gs) {
				v.Targets = append(v.Targets, TargetSet{Name: name, ProbedBy: targets.Scope(name+".yaml", ids), Groups: gs})
			}
		case KindStatus:
			var p catalog.StatusPage
			if decode(e, &p) {
				v.StatusPages = append(v.StatusPages, p)
			}
		}
	}
	if want(KindModule) {
		v.Modules = map[string]any{}
		for _, m := range modulesOf(st.entries) {
			var x any
			if pick(m.name) && json.Unmarshal(m.value, &x) == nil {
				v.Modules[m.name] = x
				found = true
			}
		}
	}
	if key != "" && !found {
		return View{}, fmt.Errorf("%w: no %s %q", ErrNotFound, kind, key)
	}
	return v, nil
}

// Item returns one item's value in the shape of its file, as Put takes it.
func (s *Service) Item(kind, key string) (json.RawMessage, error) {
	k, ok := Kind(kind)
	if !ok {
		return nil, fmt.Errorf("%w: "+kindsHint, ErrBadInput)
	}
	key = normalKey(k, key)
	if v, ok := items(s.state.Load().entries)[itemKey{k, key}]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("%w: no %s %q", ErrNotFound, k, key)
}

// Put adds or replaces one item, given as JSON in the shape of its file. The whole configuration is
// checked as the files were, and takes effect at once.
func (s *Service) Put(ctx context.Context, who Actor, kind, key string, value json.RawMessage, note string) (store.ConfigChange, error) {
	return s.PutIf(ctx, who, kind, key, value, note, 0)
}

// PutIf is Put on condition that version, the one the caller last saw, is still in force (0: no
// condition): when the configuration moved on meanwhile, the change is refused with ErrConflict naming
// the version in force, and nothing changes, so that a caller never overwrites what it has not seen.
func (s *Service) PutIf(ctx context.Context, who Actor, kind, key string, value json.RawMessage, note string, version int64) (store.ConfigChange, error) {
	k, ok := Kind(kind)
	if !ok {
		return store.ConfigChange{}, fmt.Errorf("%w: "+kindsHint, ErrBadInput)
	}
	if len(bytes.TrimSpace(value)) == 0 || string(bytes.TrimSpace(value)) == "null" {
		return store.ConfigChange{}, fmt.Errorf("%w: give the %s's value; delete removes it", ErrBadInput, k)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.state.Load()
	if err := expect(cur, version); err != nil {
		return store.ConfigChange{}, err
	}
	next := maps.Clone(cur.entries)
	if next == nil {
		next = map[string][]byte{}
	}
	if err := set(next, k, normalKey(k, key), value); err != nil {
		return store.ConfigChange{}, err
	}
	if k == KindHeartbeat {
		if err := s.checkMachine(cur, next, normalKey(k, key)); err != nil {
			return store.ConfigChange{}, err
		}
	}
	return s.commit(ctx, who, "put", note, 0, next, false)
}

// Delete removes one item.
func (s *Service) Delete(ctx context.Context, who Actor, kind, key, note string) (store.ConfigChange, error) {
	return s.DeleteIf(ctx, who, kind, key, note, 0)
}

// DeleteIf is Delete on the condition PutIf takes.
func (s *Service) DeleteIf(ctx context.Context, who Actor, kind, key, note string, version int64) (store.ConfigChange, error) {
	k, ok := Kind(kind)
	if !ok {
		return store.ConfigChange{}, fmt.Errorf("%w: "+kindsHint, ErrBadInput)
	}
	key = normalKey(k, key)
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.state.Load()
	if err := expect(cur, version); err != nil {
		return store.ConfigChange{}, err
	}
	if _, ok := items(cur.entries)[itemKey{k, key}]; !ok {
		return store.ConfigChange{}, fmt.Errorf("%w: no %s %q", ErrNotFound, k, key)
	}
	next := maps.Clone(cur.entries)
	if err := set(next, k, key, nil); err != nil {
		return store.ConfigChange{}, err
	}
	return s.commit(ctx, who, "delete", note, 0, next, false)
}

// expect checks the version a caller's change is based on against the one in force (0: it says none).
func expect(cur *State, version int64) error {
	switch {
	case version < 0:
		return fmt.Errorf("%w: if_version is a version, a whole number from 1", ErrBadInput)
	case version != 0 && cur.Version != version:
		return fmt.Errorf("%w: version %d is in force, not %d as you last saw; read it again and retry", ErrConflict, cur.Version, version)
	}
	return nil
}

// Revert undoes the change that made version v, as a new change: every item it touched gets back its
// value from before, provided no later change touched it.
func (s *Service) Revert(ctx context.Context, who Actor, v int64, note string) (store.ConfigChange, error) {
	ch, ok := s.d.Store.ConfigChangeAt(v)
	if !ok {
		return store.ConfigChange{}, fmt.Errorf("%w: no change made version %d", ErrNotFound, v)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.state.Load()
	now := items(cur.entries)
	var conflicts []string
	for _, it := range ch.Items {
		if !bytes.Equal(now[itemKey{it.Kind, it.Key}], it.After) {
			later := s.d.Store.ConfigChanges(0, 1, func(c store.ConfigChange) bool { return c.Version > v && touches(c, it.Kind, it.Key) })
			at := ""
			if len(later) == 1 {
				at = fmt.Sprintf(" in version %d", later[0].Version)
			}
			conflicts = append(conflicts, fmt.Sprintf("%s %s changed again%s", it.Kind, it.Key, at))
		}
	}
	if len(conflicts) > 0 {
		return store.ConfigChange{}, fmt.Errorf("%w after version %d: %s; revert the later change first, or put the value you want",
			ErrConflict, v, strings.Join(conflicts, ", "))
	}
	next := maps.Clone(cur.entries)
	if next == nil {
		next = map[string][]byte{}
	}
	for _, it := range ch.Items {
		if err := set(next, it.Kind, it.Key, it.Before); err != nil {
			return store.ConfigChange{}, fmt.Errorf("%s %s: %w", it.Kind, it.Key, err)
		}
	}
	return s.commit(ctx, who, "revert", note, v, next, false)
}

func touches(c store.ConfigChange, kind, key string) bool {
	return slices.ContainsFunc(c.Items, func(it store.ConfigItem) bool { return it.Kind == kind && (key == "" || it.Key == key) })
}

// HistoryFilter chooses changes: those that touched Kind (and Key), made before version Before (0: up
// to the newest), at most Limit (default 50, at most 1000).
type HistoryFilter struct {
	Kind   string
	Key    string
	Before int64
	Limit  int
}

// History lists changes, the newest first, without the values (Change has them).
func (s *Service) History(f HistoryFilter) ([]store.ConfigChange, error) {
	if f.Kind != "" {
		k, ok := Kind(f.Kind)
		if !ok {
			return nil, fmt.Errorf("%w: "+kindsHint, ErrBadInput)
		}
		f.Kind, f.Key = k, normalKey(k, f.Key)
	} else if f.Key != "" {
		return nil, fmt.Errorf("%w: name the kind of the item too", ErrBadInput)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	f.Limit = min(f.Limit, 1000)
	var keep func(store.ConfigChange) bool
	if f.Kind != "" {
		keep = func(c store.ConfigChange) bool { return touches(c, f.Kind, f.Key) }
	}
	out := s.d.Store.ConfigChanges(f.Before, f.Limit, keep)
	for i := range out {
		for j := range out[i].Items {
			out[i].Items[j].Before, out[i].Items[j].After = nil, nil
		}
	}
	if out == nil {
		out = []store.ConfigChange{}
	}
	return out, nil
}

// Change returns the change that made version v, with every item's value before and after.
func (s *Service) Change(v int64) (store.ConfigChange, error) {
	ch, ok := s.d.Store.ConfigChangeAt(v)
	if !ok {
		return store.ConfigChange{}, fmt.Errorf("%w: no change made version %d", ErrNotFound, v)
	}
	return ch, nil
}
