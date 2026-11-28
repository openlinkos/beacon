package store

import "log/slog"

// OnAuditFailure has fn told of every audit entry that Audit could not write, after the change it
// records was made: the service logs it at ERROR and counts it (beacon_audit_write_failures_total).
// Without fn, the failure is logged at ERROR on the default logger. An entry written in the same
// transaction as its change (AppendAuditTx) fails the change instead, and never comes here.
func (s *Store) OnAuditFailure(fn func(AuditEntry, error)) {
	if fn == nil {
		s.auditFailed.Store(nil)
		return
	}
	s.auditFailed.Store(&fn)
}

func (s *Store) reportAuditFailure(e AuditEntry, err error) {
	if fn := s.auditFailed.Load(); fn != nil {
		(*fn)(e, err)
		return
	}
	slog.Error("audit entry not written", "action", e.Action, "target", e.Target, "via", e.Via, "err", err)
}
