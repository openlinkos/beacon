//go:build windows

package backup

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockExclusive takes the lock file for this process alone, without waiting, so a restore can
// tell a stopped service from a running one before it replaces the database (the service holds
// the same lock from the other side, as internal/kv takes it).
func lockExclusive(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // the operator owns the directory
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		var o windows.Overlapped
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &o)
		_ = f.Close()
	}, nil
}
