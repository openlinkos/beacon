package bridge

import (
	"errors"
	"fmt"

	"github.com/openlinkos/beacon/internal/store"
)

// ErrUnrecorded means a change that gives access was undone because its audit entry could not be
// written: access is never given without a record.
var ErrUnrecorded = errors.New("the audit log cannot be written now, so nothing was changed; try again once it can")

// recordGrant writes the audit entry for a change that gives access (a token, an agent, a key) and
// reports a failure, for the caller to undo the change and refuse it. A change that takes access away
// goes ahead whatever the audit log says: its failure is logged and counted (store.OnAuditFailure).
func (b *Bridge) recordGrant(e store.AuditEntry) error {
	if err := b.d.Store.Audit(e); err != nil {
		return fmt.Errorf("%w (%s %s)", ErrUnrecorded, e.Action, e.Target)
	}
	return nil
}
