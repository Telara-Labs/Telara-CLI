//go:build !windows

package handoffrunner

import (
	"os/exec"
	"syscall"
)

// isolateProcess puts the agent in its own process group, so a stop ends every
// process it started, not only the one the runner holds.
func isolateProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
