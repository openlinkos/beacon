package bridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// AgentRunScope allows execution reports and first-drill configuration checks for a hook that names the reporting principal.
const AgentRunScope = "alerts:report"

var (
	ErrAgentRunNotFound = errors.New("no reportable hook delivery with this id on the alert")
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
	g, live := b.d.Store.Group(id)
	i := slices.IndexFunc(g.Agents, func(c store.AgentCall) bool { return c.Delivery == delivery })
	if i < 0 {
		run, err := b.saveArchivedAgentRun(id, delivery, who, in)
		if !live && errors.Is(err, ErrAgentRunNotFound) {
			err = ErrNotFound
		}
		return run, "", err
	}
	c := &g.Agents[i]
	if !b.agentReporter(c.Hook, c.Reporter, who) {
		return store.AgentRun{}, "", fmt.Errorf("%w: this principal cannot report execution for this hook delivery", ErrForbidden)
	}
	run, changed, err := b.nextAgentRun(c.Run, who, in)
	if err != nil || !changed {
		return run, "", err
	}
	if c.Run != nil {
		c.RunStart = c.Run
	}
	c.Run = &run
	entry := agentRunAudit(g.ID, c.Hook, c.Event, c.Delivery, run)
	if err := b.d.Store.PutGroupAndAudit(g, b.outcome(g, run.At, time.Time{}), entry); err != nil {
		b.d.Log.Error("record agent run", "group", g.ID, "delivery", delivery, "err", err)
		return store.AgentRun{}, "", errors.New("agent run report could not be recorded")
	}
	return run, b.agentRunNotice(&g, c), nil
}

func (b *Bridge) agentReporter(name, original string, who Actor) bool {
	hook, ok := b.hook(name)
	return ok && original != "" && original == who.Key && hook.Reporter == who.Key
}

func (b *Bridge) nextAgentRun(previous *store.AgentRun, who Actor, in AgentRunInput) (store.AgentRun, bool, error) {
	if previous != nil {
		if (previous.Status == in.Status && runExit(previous.ExitCode) == runExit(in.ExitCode)) || in.Status == "running" {
			return *previous, false, nil // duplicate or a delayed start after the terminal report
		}
		if previous.Status != "running" || in.Status == "not_started" {
			return store.AgentRun{}, false, ErrAgentRunConflict
		}
	}
	return store.AgentRun{Status: in.Status, ExitCode: in.ExitCode, At: b.now().UTC(), By: who.Name, Via: who.Via}, true, nil
}

func agentRunAudit(target, hook, event, delivery string, run store.AgentRun) store.AuditEntry {
	detail := fmt.Sprintf("%s (%s, %s) %s", hook, event, delivery, run.Status)
	if run.ExitCode != nil {
		detail += fmt.Sprintf(" (exit %d)", *run.ExitCode)
	}
	return store.AuditEntry{Time: run.At, Actor: run.By, Via: run.Via, Action: "report_agent_run", Target: target, Detail: detail}
}

func (b *Bridge) saveArchivedAgentRun(group, delivery string, who Actor, in AgentRunInput) (store.AgentRun, error) {
	var run store.AgentRun
	err := b.d.Store.UpdateOutcomeDelivery(group, delivery, b.outcomeCutoff(b.now()),
		func(o store.IncidentOutcome, d *store.OutcomeAgentDelivery, reporter string) (bool, *store.AuditEntry, error) {
			if !b.agentReporter(d.Hook, reporter, who) {
				return false, nil, ErrAgentRunNotFound
			}
			next, changed, err := b.nextAgentRun(d.Run, who, in)
			run = next
			if err != nil || !changed {
				return false, nil, err
			}
			if run.Status == "running" {
				d.StartedAt = run.At
			}
			d.Run = &run
			entry := agentRunAudit(o.ID, d.Hook, d.Event, d.ID, run)
			return true, &entry, nil
		})
	switch {
	case err == nil, errors.Is(err, ErrAgentRunConflict), errors.Is(err, ErrAgentRunNotFound):
		return run, err
	case errors.Is(err, store.ErrNotFound):
		return store.AgentRun{}, ErrAgentRunNotFound
	default:
		b.d.Log.Error("record archived agent run", "group", group, "delivery", delivery, "err", err)
		return store.AgentRun{}, errors.New("agent run report could not be recorded")
	}
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
