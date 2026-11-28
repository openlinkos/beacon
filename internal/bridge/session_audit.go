package bridge

import (
	"strings"

	"github.com/openlinkos/beacon/internal/store"
)

// FilterSessionAudit removes command previews when the reader has limited session access.
// The audit records name the machine but carry no enrolment identifier. A machine-bound
// reader can retrieve commands from the corresponding authorised session instead. A reader
// with access to all machines keeps the previews of every machine but the critical ones it
// does not reach.
func (b *Bridge) FilterSessionAudit(p store.Principal, entries []store.AuditEntry) {
	full := p.Has("sessions:read") || p.Has("machines:ssh")
	if full && !p.MachinesLimited() {
		return
	}
	for i := range entries {
		switch strings.TrimSuffix(entries[i].Action, "_refused") {
		case "ssh_exec", "ssh_shell", "ssh_file", "ssh_sftp", "ssh_subsystem", "ssh_forward":
			if full && p.IsAll() && p.Covers(entries[i].Target) {
				continue
			}
			entries[i].Detail = ""
		}
	}
}
