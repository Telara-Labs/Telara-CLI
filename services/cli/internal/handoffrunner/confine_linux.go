//go:build linux

package handoffrunner

import "os/exec"

const confinementName = "bubblewrap"

// confinementAvailable is bubblewrap when it is installed. Without it the runner
// starts Codex only after proving its native tools are off, and says the process
// itself is not confined.
func confinementAvailable() (string, bool) {
	if _, err := lookPath("bwrap"); err != nil {
		return "bubblewrap (bwrap) is not installed", false
	}
	return confinementName, true
}

// confineCommand rewrites cmd to start under bubblewrap with a filesystem made of
// the Codex binary's own directory, the runner's Codex home and the run's empty
// working directory, plus the read-only system libraries a dynamically linked
// build maps. No /usr/bin, no /bin, no /etc and no home directory are present, so
// a command Codex tries to start has no shell to run and the person's files and
// the system's account database are not there to read. PID, IPC and UTS
// namespaces are private and the process dies with the runner.
//
// The network namespace is shared: Codex must reach the run's loopback proxies,
// which a private namespace would cut off. On Linux the network is therefore
// constrained by the first layer (no native tool, proven before input and checked
// on every model request), not independently; `telara handoff runner check` says so.
func confineCommand(cmd *exec.Cmd, c *confinement) (string, error) {
	bwrap, err := lookPath("bwrap")
	if err != nil {
		return "", errNotConfinable
	}
	cmd.Path = bwrap
	cmd.Args = append(bwrapArgs(c), cmd.Args[1:]...)
	return confinementName, nil
}
