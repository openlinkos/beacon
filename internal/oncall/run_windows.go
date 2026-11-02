//go:build windows

package oncall

import "os/exec"

// The harness runs programs through /bin/sh and is meant for Unix hosts; on Windows the package only
// has to build, so that the portable tests run there too. Without process groups, only the program
// itself can be killed.

func setGroup(*exec.Cmd) {}

func terminate(cmd *exec.Cmd) { kill(cmd) }

func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
