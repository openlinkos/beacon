package configdb

import (
	"context"
	"fmt"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

// record writes a change to the audit log and tells the alert chat, so that people see what agents
// changed in the configuration as they see what agents do on machines.
func (s *Service) record(ctx context.Context, who Actor, ch store.ConfigChange) {
	target := ""
	if len(ch.Items) == 1 {
		target = ch.Items[0].Kind + " " + ch.Items[0].Key
	}
	detail := fmt.Sprintf("version %d", ch.Version)
	if ch.Note != "" {
		detail += ": " + ch.Note
	}
	_ = s.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "config_" + ch.Action, Target: target, Detail: detail})
	if s.d.Bot == nil {
		return
	}
	text := s.message(who, ch)
	if ch.Note != "" {
		text += "\n" + i18n.T(s.d.Lang, "Note: %s", ch.Note)
	}
	if err := s.d.Bot.Send(ctx, text); err != nil {
		s.d.Log.Warn("echo to the alert chat failed", "err", err)
	}
}

func (s *Service) message(who Actor, ch store.ConfigChange) string {
	lang := s.d.Lang
	added, changed, removed := 0, 0, 0
	for _, it := range ch.Items {
		switch it.Op {
		case "added":
			added++
		case "removed":
			removed++
		default:
			changed++
		}
	}
	counts := i18n.T(lang, "%d added, %d changed, %d removed", added, changed, removed)
	via := s.via(who.Via)
	switch {
	case who.Via == "start":
		return i18n.T(lang, "Beacon imported its configuration files at start: %s (version %d)", counts, ch.Version)
	case who.Via == "signal":
		return i18n.T(lang, "Beacon imported its configuration files on SIGHUP: %s (version %d)", counts, ch.Version)
	case ch.Action == "import":
		return i18n.T(lang, "%s imported the configuration via %s: %s (version %d)", who.Name, via, counts, ch.Version)
	case ch.Action == "revert":
		return i18n.T(lang, "%s reverted configuration change %d via %s: %s (version %d)", who.Name, ch.Reverts, via, counts, ch.Version)
	case len(ch.Items) != 1:
		return i18n.T(lang, "%s changed the configuration via %s: %s (version %d)", who.Name, via, counts, ch.Version)
	}
	it := ch.Items[0]
	what := s.kind(it.Kind)
	switch it.Op {
	case "added":
		return i18n.T(lang, "%s added %s %s via %s (version %d)", who.Name, what, it.Key, via, ch.Version)
	case "removed":
		return i18n.T(lang, "%s removed %s %s via %s (version %d)", who.Name, what, it.Key, via, ch.Version)
	}
	return i18n.T(lang, "%s changed %s %s via %s (version %d)", who.Name, what, it.Key, via, ch.Version)
}

func (s *Service) kind(k string) string {
	switch k {
	case KindLocation:
		return i18n.T(s.d.Lang, "location")
	case KindHeartbeat:
		return i18n.T(s.d.Lang, "heartbeat")
	case KindResource:
		return i18n.T(s.d.Lang, "resource")
	case KindTargets:
		return i18n.T(s.d.Lang, "target set")
	}
	return i18n.T(s.d.Lang, "module")
}

func (s *Service) via(v string) string {
	switch v {
	case "cli":
		return i18n.T(s.d.Lang, "the command line")
	case "mcp":
		return "MCP"
	case "console":
		return i18n.T(s.d.Lang, "the console")
	}
	return "API"
}
