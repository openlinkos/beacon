//go:build linux

package hostmetrics

import (
	"golang.org/x/sys/unix"

	"github.com/openlinkos/beacon/internal/probe"
)

func (c *Collector) filesystems() []probe.Sample {
	var out []probe.Sample
	for _, m := range c.mounts() {
		var st unix.Statfs_t
		if err := unix.Statfs(m.Point, &st); err != nil {
			continue // not visible from the service's sandbox, or gone
		}
		l := label("device", m.Device, "mountpoint", m.Point, "fstype", m.Type)
		bs := float64(st.Bsize)
		out = append(out,
			probe.Sample{Name: "node_filesystem_size_bytes", Help: "Filesystem size in bytes", Labels: l, Value: float64(st.Blocks) * bs},
			probe.Sample{Name: "node_filesystem_free_bytes", Help: "Filesystem free space in bytes", Labels: l, Value: float64(st.Bfree) * bs},
			probe.Sample{Name: "node_filesystem_avail_bytes", Help: "Filesystem space available to non-root users in bytes", Labels: l, Value: float64(st.Bavail) * bs})
	}
	return out
}

// system returns uname and the kernel's time sync state. adjtimex can be refused by a sandbox
// (systemd's ProtectClock); the sync state is then left out.
func (c *Collector) system() []probe.Sample {
	var out []probe.Sample
	var u unix.Utsname
	if err := unix.Uname(&u); err == nil {
		out = append(out, probe.Sample{Name: "node_uname_info", Help: "Labeled system information as provided by the uname system call",
			Labels: label("sysname", unix.ByteSliceToString(u.Sysname[:]), "release", unix.ByteSliceToString(u.Release[:]),
				"machine", unix.ByteSliceToString(u.Machine[:]), "nodename", unix.ByteSliceToString(u.Nodename[:])), Value: 1})
	}
	var tx unix.Timex
	if _, err := unix.Adjtimex(&tx); err == nil {
		synced := 1.0
		if tx.Status&unix.STA_UNSYNC != 0 {
			synced = 0
		}
		offset := float64(tx.Offset) / 1e6 // microseconds, unless the kernel says nanoseconds
		if tx.Status&unix.STA_NANO != 0 {
			offset = float64(tx.Offset) / 1e9
		}
		out = append(out,
			probe.Sample{Name: "node_timex_sync_status", Help: "Is clock synchronized to a reliable server (1 = yes, 0 = no)", Value: synced},
			probe.Sample{Name: "node_timex_offset_seconds", Help: "Time offset in between local system and reference clock", Value: offset})
	}
	return out
}
