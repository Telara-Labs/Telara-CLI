//go:build windows

package handoffrunner

import (
	"os/exec"
	"strconv"
)

func isolateProcess(*exec.Cmd) {}

func killTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
