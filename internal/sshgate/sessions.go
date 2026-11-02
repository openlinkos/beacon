package sshgate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// Transcript is a session as an agent reads it back: the record, plus what a command printed (cleaned
// of terminal codes) or, for an interactive shell, each command with its output and exit status. Page is
// the signed link to the session's page, where people replay it; an agent can hand it to a person.
type Transcript struct {
	store.SessionRecord
	Stdout    string    `json:"stdout,omitempty"`
	Stderr    string    `json:"stderr,omitempty"`
	Commands  []Command `json:"commands,omitempty"`
	Truncated bool      `json:"truncated,omitempty"` // the session was longer than what was kept
	Page      string    `json:"page,omitempty"`

	raw string // what a command printed, before cleaning: the file tools need the bytes as they are
}

// SessionView is a session as the API and MCP list it: its record and the link to its page.
type SessionView struct {
	store.SessionRecord
	Page string `json:"page,omitempty"`
}

// page is the signed link to a session's page; "" without a public URL.
func (g *Gate) page(id string) string { return g.d.Pages.Session(id) }

var idRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{6}(-[0-9]+)?$`)

// file writes the transcript next to the recording.
func (g *Gate) file(t Transcript) {
	if t.Recording == "" {
		return
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	path := filepath.Join(g.d.Dir, filepath.FromSlash(t.Recording)+".json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		g.d.Log.Error("cannot write a transcript", "session", t.ID, "err", err)
	}
}

// SessionFilter narrows a list of sessions.
type SessionFilter struct {
	Machine string
	Agent   string
	Limit   int
}

// Sessions lists sessions, the newest first.
func (g *Gate) Sessions(f SessionFilter) ([]SessionView, error) {
	if g == nil {
		return nil, ErrDisabled
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	rs := g.d.Store.Sessions(min(f.Limit, 1000), func(r store.SessionRecord) bool {
		return (f.Machine == "" || r.Machine == f.Machine) && (f.Agent == "" || r.Agent == f.Agent)
	})
	out := make([]SessionView, 0, len(rs))
	for _, r := range rs {
		out = append(out, SessionView{SessionRecord: r, Page: g.page(r.ID)})
	}
	return out, nil
}

// Session returns a session's transcript; one still open has only its record.
func (g *Gate) Session(id string) (Transcript, error) {
	if g == nil {
		return Transcript{}, ErrDisabled
	}
	r, ok := g.d.Store.Session(id)
	if !ok || !idRe.MatchString(id) {
		return Transcript{}, fmt.Errorf("%w %q", ErrNoSession, id)
	}
	t := Transcript{SessionRecord: r, Page: g.page(id)}
	if r.Recording == "" || r.Ended.IsZero() {
		return t, nil
	}
	b, err := os.ReadFile(filepath.Join(g.d.Dir, filepath.FromSlash(r.Recording)+".json"))
	if err != nil {
		return t, nil //nolint:nilerr // the record alone is still the answer
	}
	_ = json.Unmarshal(b, &t)
	t.SessionRecord, t.Page = r, g.page(id)
	return t, nil
}

// Recording opens a session's recording (asciicast v3).
func (g *Gate) Recording(id string) (io.ReadCloser, error) {
	if g == nil {
		return nil, ErrDisabled
	}
	r, ok := g.d.Store.Session(id)
	if !ok || !idRe.MatchString(id) || r.Recording == "" {
		return nil, fmt.Errorf("%w %q", ErrNoSession, id)
	}
	f, err := os.Open(filepath.Join(g.d.Dir, filepath.FromSlash(r.Recording)+".cast"))
	if err != nil {
		return nil, fmt.Errorf("%w: the recording of %s is gone", ErrNoSession, id)
	}
	return f, nil
}

// Tidy keeps the channel tidy until ctx ends: it forgets sessions older than Keep, with their files, and
// sends the chat what agents did.
func (g *Gate) Tidy(ctx context.Context) {
	if g == nil {
		return
	}
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	flush := time.NewTicker(10 * time.Second)
	defer flush.Stop()
	g.prune()
	for {
		select {
		case <-ctx.Done():
			g.echo.flush(context.Background(), true)
			return
		case <-flush.C:
			g.echo.flush(ctx, false)
		case <-prune.C:
			g.prune()
		}
	}
}

func (g *Gate) prune() {
	if g.d.Keep <= 0 {
		return
	}
	gone := g.d.Store.DeleteSessionsBefore(g.now().Add(-g.d.Keep))
	for _, r := range gone {
		if r.Recording == "" {
			continue
		}
		base := filepath.Join(g.d.Dir, filepath.FromSlash(r.Recording))
		_ = os.Remove(base + ".cast")
		_ = os.Remove(base + ".json")
		// drop the day's directory, and the month's and year's, once empty
		for d := filepath.Dir(base); strings.HasPrefix(d, filepath.Clean(g.d.Dir)+string(filepath.Separator)); d = filepath.Dir(d) {
			if os.Remove(d) != nil {
				break
			}
		}
	}
	if len(gone) > 0 {
		g.d.Log.Info("old SSH sessions forgotten", "sessions", len(gone))
	}
}
