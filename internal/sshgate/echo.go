package sshgate

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

// The chat hears what agents did on machines in batches, one message per agent and machine: after
// echoQuiet without anything new, or echoMax after the first line, whichever comes first.
const (
	echoQuiet = 20 * time.Second
	echoMax   = time.Minute
	echoLines = 10
)

type batch struct {
	agent, machine string
	lines          []string
	more           int
	first, last    time.Time
}

type echoer struct {
	g       *Gate
	mu      sync.Mutex
	pending map[string]*batch
}

func (e *echoer) line(r store.SessionRecord) string {
	lang := e.g.d.Lang
	cmd := short(r.Command, 120)
	switch r.Kind {
	case "exec":
		if r.Exit != nil {
			return i18n.T(lang, "ran `%s` (exit %d)", cmd, *r.Exit)
		}
		return i18n.T(lang, "ran `%s`", cmd)
	case "shell":
		d := r.Ended.Sub(r.Started)
		took := i18n.T(lang, "under a minute")
		if d >= time.Minute {
			took = i18n.T(lang, "%d min", int(d.Minutes()))
		}
		return i18n.T(lang, "used a shell for %s, %d commands", took, r.Commands)
	case "sftp":
		return i18n.T(lang, "transferred files")
	case "forward":
		return i18n.T(lang, "forwarded a port to %s", cmd)
	case "file":
		verb, path, _ := strings.Cut(r.Command, " ")
		switch verb {
		case "write":
			return i18n.T(lang, "wrote %s", short(path, 120))
		case "edit":
			return i18n.T(lang, "edited %s", short(path, 120))
		}
		return "" // reading a file is recorded, not echoed
	}
	return i18n.T(lang, "opened %s", r.Kind)
}

// add queues the line for a finished session.
func (e *echoer) add(r store.SessionRecord) {
	line := e.line(r)
	if line == "" || e.g.d.Bot == nil {
		return
	}
	now := e.g.now()
	key := r.Agent + "\x00" + r.Machine
	e.mu.Lock()
	defer e.mu.Unlock()
	b := e.pending[key]
	if b == nil {
		b = &batch{agent: r.Agent, machine: r.Machine, first: now}
		e.pending[key] = b
	}
	if len(b.lines) < echoLines {
		b.lines = append(b.lines, line)
	} else {
		b.more++
	}
	b.last = now
}

// flush sends the batches that are due, or all of them.
func (e *echoer) flush(ctx context.Context, all bool) {
	now := e.g.now()
	var due []*batch
	e.mu.Lock()
	for k, b := range e.pending {
		if all || now.Sub(b.last) >= echoQuiet || now.Sub(b.first) >= echoMax {
			due = append(due, b)
			delete(e.pending, k)
		}
	}
	e.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].first.Before(due[j].first) })
	lang := e.g.d.Lang
	for _, b := range due {
		var sb strings.Builder
		sb.WriteString(i18n.T(lang, "%s on %s:", b.agent, b.machine))
		for _, l := range b.lines {
			sb.WriteString("\n- " + l)
		}
		if b.more > 0 {
			sb.WriteString("\n- " + i18n.T(lang, "and %d more", b.more))
		}
		e.g.post(ctx, sb.String())
	}
}
