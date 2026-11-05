//go:build unix

package backup

import (
	"os"
	"syscall"
)

// lockExclusive takes the lock file for this process alone, without waiting, so a restore can
// tell a stopped service from a running one before it replaces the database (the service holds
// the same lock from the other side, as internal/kv takes it).
func lockExclusive(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // the operator owns the directory
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
