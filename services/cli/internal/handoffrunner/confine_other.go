//go:build !darwin && !linux

package handoffrunner

import "os/exec"

// confinementAvailable names the confinement this platform applies. There is none
// here, so the runner starts Codex only after proving its native tools are off
// (proveToolSurface, and the model proxy's check of every request), and says the
// process itself is not confined.
func confinementAvailable() (string, bool) {
	return "this platform has no process confinement the runner can apply", false
}

func confineCommand(*exec.Cmd, *confinement) (string, error) {
	return "", errNotConfinable
}
