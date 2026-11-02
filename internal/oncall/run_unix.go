//go:build !windows

package oncall

import (
	"os/exec"
	"syscall"
)

// setGroup puts the program in a process group of its own, so that a timeout reaches everything it
// started: the wrapper script, the agent and the agent's own children.
func setGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, sig)
	}
}

// terminate asks the process group to stop (SIGTERM), which lets an agent end its session.
func terminate(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGTERM) }

// kill ends the process group for good (SIGKILL).
func kill(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGKILL) }
