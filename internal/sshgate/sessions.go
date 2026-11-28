package sshgate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

// Transcript is a session as an agent reads it back: the record, plus what a command was sent and printed
// (cleaned of terminal codes) or, for an interactive shell, each command with its output and exit status.
// A shell's commands are told apart by the shell-integration marks in its output, which any program the
// shell runs can print too, so CommandsAdvisory says they are a guide; the recording, with what was typed
// and the raw output, is the record. Page is the session's replay URL.
type Transcript struct {
	store.SessionRecord
	Stdin                 string    `json:"stdin,omitempty"`
	Stdout                string    `json:"stdout,omitempty"`
	Stderr                string    `json:"stderr,omitempty"`
	Commands              []Command `json:"commands,omitempty"`
	CommandsAdvisory      bool      `json:"commands_advisory,omitempty"`
	Truncated             bool      `json:"truncated,omitempty"` // the recording or structured text exceeded its capture limit
	Page                  string    `json:"page,omitempty"`
	RecordingAvailable    *bool     `json:"recording_available,omitempty"`
	RecordingPublication  string    `json:"recording_publication,omitempty"`
	TranscriptPublication string    `json:"transcript_publication,omitempty"`

	raw           string // what a command printed, before cleaning: the file tools need the bytes as they are
	textTruncated bool
}

// SessionView is a session as the API and MCP list it: its record and the link to its page.
type SessionView struct {
	store.SessionRecord
	Page string `json:"page,omitempty"`
}

// page is the signed link to a session's page; "" without a public URL.
func (g *Gate) page(id string) string { return g.d.Pages.Session(id) }

var idRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{6}(-[0-9]+)?$`)

// file seals the structured transcript independently of the recording.
func (g *Gate) file(t Transcript) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return writeEvidence(g.d.Files, t.Recording+".json", b)
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

// Sessions lists sessions for a trusted caller, newest first.
func (g *Gate) Sessions(f SessionFilter) ([]SessionView, error) {
	if g == nil {
		return nil, ErrDisabled
	}
	return g.sessionViews(f, nil), nil
}

// SessionsFor lists sessions visible to p. The machine scope is applied before the result limit.
func (g *Gate) SessionsFor(p store.Principal, f SessionFilter) ([]SessionView, error) {
	if g == nil {
		return nil, ErrDisabled
	}
	access := g.d.Store.SessionAccess(p)
	return g.sessionViews(f, &access), nil
}

func (g *Gate) sessionViews(f SessionFilter, access *store.SessionAccess) []SessionView {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	rs := g.d.Store.Sessions(min(f.Limit, 1000), func(r store.SessionRecord) bool {
		return (f.Machine == "" || r.Machine == f.Machine) && (f.Agent == "" || RanBy(r.Agent, f.Agent)) &&
			(access == nil || access.Visible(r))
	})
	out := make([]SessionView, 0, len(rs))
	for _, r := range rs {
		view := SessionView{SessionRecord: r}
		switch {
		case access == nil:
			view.Page = g.page(r.ID)
		case access.Details():
			view.Page = g.sessionSignIn(r.ID)
		default:
			view.Command, view.Recording = "", ""
		}
		out = append(out, view)
	}
	return out
}

// Session returns a session's transcript for a trusted caller; an open session has only its record.
func (g *Gate) Session(id string) (Transcript, error) {
	if g == nil {
		return Transcript{}, ErrDisabled
	}
	r, err := g.sessionRecord(id)
	if err != nil {
		return Transcript{}, err
	}
	return g.transcript(context.Background(), r, g.page(id)), nil
}

// SessionFor returns a transcript only when p may see this session's details.
func (g *Gate) SessionFor(p store.Principal, id string) (Transcript, error) {
	r, err := g.sessionRecordFor(p, id)
	if err != nil {
		return Transcript{}, err
	}
	return g.transcript(context.Background(), r, g.sessionSignIn(id)), nil
}

// SessionDetail verifies access to the replay for a caller who has already authorised the session.
func (g *Gate) SessionDetail(ctx context.Context, id string) (Transcript, error) {
	if g == nil {
		return Transcript{}, ErrDisabled
	}
	r, err := g.sessionRecord(id)
	if err != nil {
		return Transcript{}, err
	}
	return g.sessionDetail(ctx, r, g.page(id)), nil
}

// SessionDetailFor verifies the replay only after checking the caller's machine and session scope.
func (g *Gate) SessionDetailFor(ctx context.Context, p store.Principal, id string) (Transcript, error) {
	r, err := g.sessionRecordFor(p, id)
	if err != nil {
		return Transcript{}, err
	}
	return g.sessionDetail(ctx, r, g.sessionSignIn(id)), nil
}

func (g *Gate) sessionDetail(ctx context.Context, r store.SessionRecord, page string) Transcript {
	// Shared storage can be slow or unavailable while a recording is still on the primary.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	t := g.transcript(ctx, r, page)
	f, err := g.openRecording(ctx, r)
	t.RecordingAvailable = new(err == nil)
	if f != nil {
		_ = f.Close()
	}
	if r.Recording != "" {
		if files, ok := g.d.Files.(interface{ Publication(string) string }); ok {
			t.RecordingPublication = files.Publication(r.Recording + ".cast")
			t.TranscriptPublication = files.Publication(r.Recording + ".json")
		}
	}
	return t
}

func (g *Gate) transcript(ctx context.Context, r store.SessionRecord, page string) Transcript {
	t := Transcript{SessionRecord: r, Page: page}
	if r.Ended.IsZero() || r.TranscriptState == store.EvidenceMetadataOnly || r.TranscriptState == store.EvidenceUnavailable {
		return t
	}
	if r.Recording == "" {
		t.TranscriptState = store.EvidenceUnavailable
		return t
	}
	name := r.Recording + ".json"
	var data []byte
	var err error
	if files, ok := g.d.Files.(interface {
		ReadFileContext(context.Context, string) ([]byte, error)
	}); ok {
		data, err = files.ReadFileContext(ctx, name)
	} else {
		data, err = g.d.Files.ReadFile(name)
	}
	var saved Transcript
	if err != nil || json.Unmarshal(data, &saved) != nil || !transcriptMatches(r, saved) {
		t.TranscriptState = store.EvidenceUnavailable
		return t
	}
	t.Stdin, t.Stdout, t.Stderr, t.Commands, t.Truncated = saved.Stdin, saved.Stdout, saved.Stderr, saved.Commands, saved.Truncated
	t.CommandsAdvisory = len(t.Commands) > 0
	return t
}

func transcriptMatches(r store.SessionRecord, saved Transcript) bool {
	if saved.ID != "" || r.TranscriptState != "" {
		return saved.ID == r.ID
	}
	// Accept the older sidecars that carried output without a session ID.
	return saved.Stdout != "" || saved.Stderr != "" || saved.Commands != nil
}

func (g *Gate) sessionSignIn(id string) string {
	if g.d.Pages == nil || g.d.Pages.PublicURL == "" || !idRe.MatchString(id) {
		return ""
	}
	return strings.TrimSuffix(g.d.Pages.PublicURL, "/") + link.SignInPath(link.SessionPath(id))
}

func (g *Gate) sessionRecord(id string) (store.SessionRecord, error) {
	if idRe.MatchString(id) {
		if r, ok := g.d.Store.Session(id); ok {
			return r, nil
		}
	}
	return store.SessionRecord{}, fmt.Errorf("%w %q", ErrNoSession, id)
}

func (g *Gate) sessionRecordFor(p store.Principal, id string) (store.SessionRecord, error) {
	if g == nil {
		return store.SessionRecord{}, ErrDisabled
	}
	access := g.d.Store.SessionAccess(p)
	if !access.Details() {
		return store.SessionRecord{}, fmt.Errorf("%w %q", ErrNoSession, id)
	}
	r, err := g.sessionRecord(id)
	if err != nil {
		return store.SessionRecord{}, err
	}
	if !access.Visible(r) {
		return store.SessionRecord{}, fmt.Errorf("%w %q", ErrNoSession, id)
	}
	return r, nil
}

// Recording opens a session's recording (asciicast v3) for a trusted caller.
func (g *Gate) Recording(id string) (io.ReadCloser, error) {
	if g == nil {
		return nil, ErrDisabled
	}
	r, err := g.sessionRecord(id)
	if err != nil {
		return nil, err
	}
	return g.openRecording(context.Background(), r)
}

// RecordingFor opens a recording only when p may see this session's details.
func (g *Gate) RecordingFor(p store.Principal, id string) (io.ReadCloser, error) {
	r, err := g.sessionRecordFor(p, id)
	if err != nil {
		return nil, err
	}
	return g.openRecording(context.Background(), r)
}

// ErrRecordingUnavailable identifies a visible session that cannot currently be replayed.
var ErrRecordingUnavailable = errors.New("the session recording is unavailable")

func (g *Gate) openRecording(ctx context.Context, r store.SessionRecord) (io.ReadCloser, error) {
	if r.Recording == "" || r.Ended.IsZero() {
		return nil, ErrRecordingUnavailable
	}
	switch r.RecordingState {
	case "", store.EvidenceComplete, store.EvidenceTruncated:
	default:
		return nil, ErrRecordingUnavailable
	}
	name := r.Recording + ".cast"
	var f io.ReadCloser
	var err error
	if files, ok := g.d.Files.(interface {
		OpenContext(context.Context, string) (io.ReadCloser, error)
	}); ok {
		f, err = files.OpenContext(ctx, name)
	} else {
		f, err = g.d.Files.Open(name)
	}
	if err != nil {
		return nil, ErrRecordingUnavailable
	}
	if !terminal(r) {
		return replayable(f), nil
	}
	return f, nil
}

// terminal reports whether a session had a terminal. Sessions recorded before Beacon kept it had one
// when they were an interactive shell: the tools (commands, files) never ask for one, and an agent's
// own ssh runs a command without one unless it says -t.
func terminal(r store.SessionRecord) bool {
	if r.PTY != nil {
		return *r.PTY
	}
	return r.Kind == "shell"
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

// RunPending publishes sealed evidence when the files store supports it. Every instance runs the
// worker, including a standby which may have local evidence from an earlier term as primary.
func (g *Gate) RunPending(ctx context.Context) {
	if g == nil {
		return
	}
	if worker, ok := g.d.Files.(interface {
		RunPending(context.Context, time.Duration)
	}); ok {
		worker.RunPending(ctx, g.d.Keep)
	}
}
