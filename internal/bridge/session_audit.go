package bridge

import "github.com/openlinkos/beacon/internal/store"

// FilterSessionAudit removes command previews when the reader has limited session access.
// The audit records name the machine but carry no enrolment identifier. A machine-bound
// reader can retrieve commands from the corresponding authorised session instead.
func (b *Bridge) FilterSessionAudit(p store.Principal, entries []store.AuditEntry) {
	if !p.MachineScopeEmpty() && len(p.Machines) == 0 && (p.Has("sessions:read") || p.Has("machines:ssh")) {
		return
	}
	for i := range entries {
		switch entries[i].Action {
		case "ssh_exec", "ssh_shell", "ssh_file", "ssh_sftp", "ssh_forward":
			entries[i].Detail = ""
		}
	}
}
