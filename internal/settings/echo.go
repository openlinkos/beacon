package settings

import (
	"context"
	"fmt"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

// record writes a change to the audit log and tells the alert chat, so that people see what
// changed in the settings — and, when an agent was granted the scope, what it changed — as they
// see what agents change anywhere else. Values stay out of both: a setting's value is no secret,
// but the audit trail and the chat echo say what was changed, by whom and why, and the pages show
// the values.
func (s *Service) record(ctx context.Context, who Actor, ch Change) {
	detail := fmt.Sprintf("%s at %s, version %d", ch.Key, scopeName(ch.Scope, ch.Target), ch.Version)
	if ch.Reverts != 0 {
		detail = fmt.Sprintf("%s at %s, version %d, undoing %d", ch.Key, scopeName(ch.Scope, ch.Target), ch.Version, ch.Reverts)
	}
	if ch.Reason != "" {
		detail += ": " + ch.Reason
	}
	_ = s.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "settings_" + ch.Action, Target: ch.Key, Detail: detail})
	if s.d.Bot == nil {
		return
	}
	if err := s.d.Bot.Send(ctx, s.message(who, ch)); err != nil {
		s.d.Log.Warn("echo to the alert chat failed", "err", err)
	}
}

func (s *Service) message(who Actor, ch Change) string {
	lang := s.d.Lang
	via := s.via(who.Via)
	where := scopeName(ch.Scope, ch.Target)
	switch {
	case ch.Unchanged:
		return i18n.T(lang, "%s left %s at %s as it was via %s (version %d)", who.Name, ch.Key, where, via, ch.Version)
	case ch.Action == "set" && len(ch.Before) == 0:
		return i18n.T(lang, "%s set %s at %s via %s (version %d)", who.Name, ch.Key, where, via, ch.Version)
	case ch.Action == "set":
		return i18n.T(lang, "%s changed %s at %s via %s (version %d)", who.Name, ch.Key, where, via, ch.Version)
	case ch.Action == "reset":
		return i18n.T(lang, "%s reset %s at %s to what the level above gives via %s (version %d)", who.Name, ch.Key, where, via, ch.Version)
	default:
		return i18n.T(lang, "%s reverted %s at %s to an earlier value via %s (version %d)", who.Name, ch.Key, where, via, ch.Version)
	}
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
