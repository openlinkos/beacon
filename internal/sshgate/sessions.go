package sshgate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	if err := g.d.Files.WriteFile(t.Recording+".json", b); err != nil {
		g.d.Log.Error("cannot write a transcript", "session", t.ID, "err", err)
	}
}

// RanBy reports whether a session's actor (as the audit log names it) is who: the whole name, or the
// person or the agent of "person / agent".
func RanBy(actor, who string) bool {
	if actor == who {
		return true
	}
	person, agent, ok := strings.Cut(actor, " / ")
	return ok && (person == who || agent == who)
}

// SessionFilter narrows a list of sessions. Agent matches who ran them: "person / agent", the agent or
// the person alone, or a token's name.
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
		return (f.Machine == "" || r.Machine == f.Machine) && (f.Agent == "" || RanBy(r.Agent, f.Agent))
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
	b, err := g.d.Files.ReadFile(r.Recording + ".json")
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
	f, err := g.d.Files.Open(r.Recording + ".cast")
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
	recheck := time.NewTicker(time.Minute)
	defer recheck.Stop()
	g.prune()
	for {
		select {
		case <-ctx.Done():
			g.echo.flush(context.Background(), true)
			return
		case <-flush.C:
			g.echo.flush(ctx, false)
		case <-recheck.C: // credentials that expired meanwhile
			g.Recheck()
		case <-prune.C:
			g.prune()
		}
	}
}

func (g *Gate) prune() {
	if g.d.Keep <= 0 {
		return
	}
	old := g.d.Store.SessionsStartedBefore(g.now().Add(-g.d.Keep))
	n := 0
	for _, r := range old {
		if r.Recording != "" {
			if err := g.d.Files.Remove(r.Recording + ".cast"); err != nil {
				g.d.Log.Warn("recording object was not deleted; the session stays until the next tidy", "session", r.ID, "err", err)
				continue
			}
			if err := g.d.Files.Remove(r.Recording + ".json"); err != nil {
				g.d.Log.Warn("transcript object was not deleted; the session stays until the next tidy", "session", r.ID, "err", err)
				continue
			}
		}
		if err := g.d.Store.DeleteSession(r.ID); err != nil {
			g.d.Log.Warn("session row was not deleted", "session", r.ID, "err", err)
			continue
		}
		n++
	}
	if n > 0 {
		g.d.Log.Info("old SSH sessions forgotten", "sessions", n)
	}
}

// RetryPending uploads leftover local recordings (a crash or a previous primary) when the files
// store supports it. Every instance runs this, including a standby.
func (g *Gate) RetryPending(ctx context.Context) {
	if g == nil {
		return
	}
	type pending interface {
		RetryPending(context.Context) error
	}
	if r, ok := g.d.Files.(pending); ok {
		if err := r.RetryPending(ctx); err != nil && g.d.Log != nil {
			g.d.Log.Warn("pending recordings were not uploaded", "err", err)
		}
	}
}

// TidyLocal removes leftover local staging files older than keep, when the files store supports it.
func (g *Gate) TidyLocal(keep time.Duration) {
	if g == nil {
		return
	}
	type local interface {
		TidyLocal(time.Duration)
	}
	if t, ok := g.d.Files.(local); ok {
		t.TidyLocal(keep)
	}
}
