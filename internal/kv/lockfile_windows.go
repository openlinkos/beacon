//go:build windows

package kv

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockExclusive takes the first byte of the lock file for this process alone, without waiting.
func lockExclusive(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
}
