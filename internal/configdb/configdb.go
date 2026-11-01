// Package configdb keeps what Beacon monitors in its database: the catalogue (probe locations,
// heartbeats, expiring resources), the probe targets in named sets that keep the meaning of the file
// names they came from (all-, overseas-, mainland-, <location>-), and the probe module file. People and
// agents change it through the API, MCP and the command line; every change is a version that records
// who made it, when, through what, and each item's value before and after, so the history can be
// listed and a change reverted. Files are for import and export.
//
// Every change is checked whole, with the checks the files always had (packages catalog, targets and
// probe), and takes effect at once: the gateway, the catalogue's metrics and the rotation check read
// the state held here, so nothing needs a reload.
package configdb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

// The kinds of item, in the order they are listed.
const (
	KindLocation  = "location"
	KindHeartbeat = "heartbeat"
	KindResource  = "resource"
	KindTargets   = "targets" // a target set, once a file in the targets directory
	KindModule    = "module"
)

var kinds = []string{KindLocation, KindHeartbeat, KindResource, KindTargets, KindModule}

// Lists are the names of the kinds' lists in the files and the API.
var Lists = map[string]string{KindLocation: "locations", KindHeartbeat: "heartbeats", KindResource: "resources",
	KindTargets: "targets", KindModule: "modules"}

// Kind reads a kind as people write it, singular or plural: location or locations.
func Kind(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for k, l := range Lists {
		if s == k || s == l {
			return k, true
		}
	}
	return "", false
}

// Errors the API, MCP and the admin socket turn into answers. They are wrapped with details.
var (
	ErrBadInput = errors.New("bad input")
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("the configuration would not pass its checks")
	ErrConflict = errors.New("the configuration changed")
)

// Actor is who changes the configuration: a token's name or the person at the command line, and how
// (api, mcp, cli; start and signal when the service imports its files itself).
type Actor struct {
	Name string
	Via  string
}

// Poster sends a plain message to the alert chat.
type Poster interface {
	Send(ctx context.Context, text string) error
}

// Deps are what the configuration needs. Bot may be nil.
type Deps struct {
	Store *store.Store
	Bot   Poster
	Lang  i18n.Lang
	Log   *slog.Logger
	Now   func() time.Time
}

// State is the configuration in force.
type State struct {
	Version int64
	Catalog *catalog.Catalog
	Targets *targets.Set
	Modules []byte // the module file, handed to probes as it is; nil when there is none

	entries map[string][]byte // as stored, by key; never changed once built
	problem error             // why the state does not pass the checks, if it does not
}

// Service holds the configuration.
type Service struct {
	d     Deps
	mu    sync.Mutex // serialises changes
	state atomic.Pointer[State]
}

// Open loads the configuration from the store. One that no longer passes the checks (a newer Beacon
// may check more) is still served, so that monitoring goes on; the problem is logged and Check
// reports it, and every change is refused until it is put right.
func Open(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Service{d: d}
	entries, version := d.Store.Config()
	st := build(entries)
	st.Version = version
	if st.problem != nil {
		d.Log.Error("the configuration in the database does not pass its checks; serving it until it is put right",
			"version", version, "err", st.problem)
	}
	s.state.Store(st)
	return s
}

// Catalog is the catalogue in force.
func (s *Service) Catalog() *catalog.Catalog { return s.state.Load().Catalog }

// Targets are the probe targets in force.
func (s *Service) Targets() *targets.Set { return s.state.Load().Targets }

// Modules is the probe module file in force, as probes get it; nil when there is none.
func (s *Service) Modules() []byte { return s.state.Load().Modules }

// Version is the version in force: 0 until the configuration is first changed.
func (s *Service) Version() int64 { return s.state.Load().Version }

// Check reports whether the configuration in force passes its checks.
func (s *Service) Check() error { return s.state.Load().problem }

// Summary counts what the configuration in force holds, for logs and the command line.
func (s *Service) Summary() string { return s.state.Load().Summary() }

// Summary counts what the state holds.
func (st *State) Summary() string {
	n := 0
	if len(st.Modules) > 0 {
		if mods, err := moduleList(st.Modules); err == nil {
			n = len(mods)
		}
	}
	return fmt.Sprintf("%d probe locations, %d heartbeats, %d resources, %d probe targets in %d sets, %d modules",
		len(st.Catalog.Locations), len(st.Catalog.Heartbeats), len(st.Catalog.Resources), st.Targets.Count(), len(st.Targets.Files()), n)
}

// commit makes next the configuration, if it passes the checks and changes anything, as one version.
// The caller holds s.mu. With dryRun the change is worked out but not made. An import is exact: files
// that differ only in what is not an item (the module file's comments, say) are a change too, with no
// items, so that probes get the module file as it was imported.
func (s *Service) commit(ctx context.Context, who Actor, action, note string, reverts int64, next map[string][]byte, dryRun bool) (store.ConfigChange, error) {
	cur := s.state.Load()
	st := build(next)
	if st.problem != nil {
		return store.ConfigChange{}, fmt.Errorf("%w: %w", ErrInvalid, st.problem)
	}
	items := diff(cur.entries, next)
	ch := store.ConfigChange{Time: s.d.Now().UTC(), Actor: who.Name, Via: who.Via, Action: action, Reverts: reverts,
		Note: strings.TrimSpace(note), Items: items}
	exact := action == "import"
	if len(items) == 0 && (!exact || maps.EqualFunc(cur.entries, next, bytes.Equal)) {
		return store.ConfigChange{Version: cur.Version, Action: action, Items: []store.ConfigItem{}, Unchanged: true}, nil
	}
	if dryRun {
		return ch, nil
	}
	var writes []store.ConfigWrite
	for k, v := range next {
		if was, ok := cur.entries[k]; !ok || string(was) != string(v) {
			writes = append(writes, store.ConfigWrite{Key: k, Was: cur.entries[k], Value: v})
		}
	}
	for k, was := range cur.entries {
		if _, ok := next[k]; !ok {
			writes = append(writes, store.ConfigWrite{Key: k, Was: was})
		}
	}
	ch, err := s.d.Store.CommitConfig(ch, writes)
	if errors.Is(err, store.ErrConflict) {
		return store.ConfigChange{}, fmt.Errorf("%w meanwhile; read it again and retry", ErrConflict)
	}
	if err != nil {
		return store.ConfigChange{}, err
	}
	st.Version = ch.Version
	s.state.Store(st)
	s.record(ctx, who, ch)
	return ch, nil
}
