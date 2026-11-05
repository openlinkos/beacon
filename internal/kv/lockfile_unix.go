//go:build unix

package kv

import (
	"os"
	"syscall"
)

// lockExclusive takes the whole lock file for this process alone, without waiting.
func lockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
