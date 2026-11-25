package bridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// AgentRunScope allows only execution reports. The hook must also name the reporting principal.
const AgentRunScope = "alerts:report"

var (
	ErrAgentRunNotFound = errors.New("no hook delivery with this id on the current alert episode")
	ErrAgentRunConflict = errors.New("the delivery already has a different terminal report")
)

// AgentRunInput is a programme phase or outcome. Only a normal exit has a numeric exit code.
type AgentRunInput struct {
	Status   string `json:"status" jsonschema:"running, succeeded, failed, timed_out or not_started"`
	ExitCode *int   `json:"exit_code,omitempty" jsonschema:"exit status from 0 to 255, when known"`
}

func (in AgentRunInput) validate() error {
	code := runExit(in.ExitCode)
	if in.ExitCode != nil && (code < 0 || code > 255) {
		return fmt.Errorf("%w: exit_code must be from 0 to 255", ErrBadInput)
	}
	switch in.Status {
	case "running", "timed_out", "not_started":
		if in.ExitCode == nil {
			return nil
		}
	case "succeeded":
		if code == 0 {
			return nil
		}
	case "failed":
		if code != 0 {
			return nil
		}
	default:
		return fmt.Errorf("%w: unknown agent run status", ErrBadInput)
	}
	return fmt.Errorf("%w: exit_code does not match the reported status", ErrBadInput)
}

func runExit(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

// ReportAgentRun writes an execution report independently of the hook's delivery response. Neither
// a start nor a terminal report changes the group's escalation, ownership or recovery state.
func (b *Bridge) ReportAgentRun(ctx context.Context, id, delivery string, who Actor, in AgentRunInput) (store.AgentRun, error) {
	if err := in.validate(); err != nil {
		return store.AgentRun{}, err
	}
	run, notice, err := b.saveAgentRun(id, delivery, who, in)
	if err == nil && notice != "" {
		b.echo(ctx, notice)
	}
	return run, err
}

func (b *Bridge) saveAgentRun(id, delivery string, who Actor, in AgentRunInput) (store.AgentRun, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.d.Store.Group(id)
	if !ok {
		return store.AgentRun{}, "", ErrNotFound
	}
	i := slices.IndexFunc(g.Agents, func(c store.AgentCall) bool { return c.Delivery == delivery })
	if i < 0 {
		return store.AgentRun{}, "", ErrAgentRunNotFound
	}
	c := &g.Agents[i]
	hook, ok := b.hook(c.Hook)
	if !ok || c.Reporter == "" || c.Reporter != who.Key || hook.Reporter != who.Key {
		return store.AgentRun{}, "", fmt.Errorf("%w: this principal cannot report execution for this hook delivery", ErrForbidden)
	}
	if previous := c.Run; previous != nil {
		if (previous.Status == in.Status && runExit(previous.ExitCode) == runExit(in.ExitCode)) || in.Status == "running" {
			return *previous, "", nil // duplicate or a delayed start after the terminal report
		}
		if previous.Status != "running" || in.Status == "not_started" {
			return store.AgentRun{}, "", ErrAgentRunConflict
		}
		c.RunStart = previous
	}
	run := store.AgentRun{Status: in.Status, ExitCode: in.ExitCode, At: b.now().UTC(), By: who.Name, Via: who.Via}
	c.Run = &run
	detail := fmt.Sprintf("%s (%s, %s) %s", c.Hook, c.Event, c.Delivery, run.Status)
	if run.ExitCode != nil {
		detail += fmt.Sprintf(" (exit %d)", *run.ExitCode)
	}
	entry := store.AuditEntry{Time: run.At, Actor: who.Name, Via: who.Via, Action: "report_agent_run", Target: g.ID, Detail: detail}
	if err := b.d.Store.PutGroupAndAudit(g, b.outcome(g, run.At, time.Time{}), entry); err != nil {
		b.d.Log.Error("record agent run", "group", g.ID, "delivery", delivery, "err", err)
		return store.AgentRun{}, "", errors.New("agent run report could not be recorded")
	}
	return run, b.agentRunNotice(&g, c), nil
}

func (b *Bridge) agentRunNotice(g *store.Group, c *store.AgentCall) string {
	hook, title := chatSafe(c.Hook), chatSafe(b.title(g))
	switch c.Run.Status {
	case "running":
		return b.t("Hook %s reported starting its programme for \"%s\"", hook, title)
	case "succeeded":
		return b.t("Hook %s reported an exit status of 0 for \"%s\"", hook, title)
	case "timed_out":
		return b.t("Hook %s reported that its programme timed out for \"%s\"", hook, title)
	case "not_started":
		return b.t("Hook %s reported that its programme could not start for \"%s\"", hook, title)
	default:
		if c.Run.ExitCode != nil {
			return b.t("Hook %s reported an exit status of %d for \"%s\"", hook, *c.Run.ExitCode, title)
		}
		return b.t("Hook %s reported that its programme failed for \"%s\"", hook, title)
	}
}
