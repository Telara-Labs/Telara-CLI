package handoffrunner

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var lookPath = exec.LookPath

// confinement is what the Codex process itself may touch (TENG-3693 B11). The
// native tools are disabled and proven disabled before any input; confining the
// process is the second, independent layer, so a tool or path a Codex version
// adds that the first layer misses still cannot read the person's files or reach
// the network.
type confinement struct {
	// Bin is the Codex binary, resolved; its directory holds its runtime files.
	Bin string
	// Home is the runner's Codex home (sign-in, catalog, Codex's own state).
	Home string
	// Workdir is the run's empty working directory.
	Workdir string
	// Tmp is the process's temporary directory, inside Home.
	Tmp string
	// Ports are the loopback ports the process may connect to: the run's model
	// proxy and session proxy. No other address is reachable.
	Ports []int
}

// errNotConfinable means this platform has no process confinement the runner
// can apply; the runner then relies on the proven tool surface alone and says so.
var errNotConfinable = errors.New("process confinement is not available on this platform")

// newConfinement resolves every path, so the profile names real paths (on macOS
// the temporary and home trees sit behind /var and /tmp symlinks).
func newConfinement(bin, home, workdir string, ports ...int) (*confinement, error) {
	resolvedBin, err := resolveBinary(bin)
	if err != nil {
		return nil, err
	}
	tmp := filepath.Join(home, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	c := &confinement{Bin: resolvedBin, Ports: ports}
	for dst, src := range map[*string]string{&c.Home: home, &c.Workdir: workdir, &c.Tmp: tmp} {
		real, err := filepath.EvalSymlinks(src)
		if err != nil {
			return nil, err
		}
		if *dst, err = filepath.Abs(real); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// resolveBinary finds the codex program and follows its links to the real file.
// A script launcher (the npm package's codex is a Node script) is refused: the
// confinement allows the native binary's own directory, not an interpreter.
func resolveBinary(bin string) (string, error) {
	path := bin
	if !filepath.IsAbs(path) {
		found, err := lookPath(bin)
		if err != nil {
			return "", err
		}
		path = found
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(real)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 2)
	if n, _ := f.Read(head); n == 2 && string(head) == "#!" {
		return "", errors.New("the codex program at " + real + " is a script launcher; point --codex-bin at the native codex binary so the runner can confine it")
	}
	return filepath.Abs(real)
}

// bwrapArgs is the bubblewrap command line for c, ending with the Codex binary;
// app-server's own arguments follow it. It is built on every platform so it is
// tested on every platform.
func bwrapArgs(c *confinement) []string {
	codexDir := filepath.Dir(c.Bin)
	args := []string{"bwrap",
		"--die-with-parent", "--new-session",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try",
		"--clearenv",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
	}
	for _, lib := range []string{"/lib", "/lib64", "/usr/lib", "/usr/lib64"} {
		args = append(args, "--ro-bind-try", lib, lib)
	}
	args = append(args,
		"--ro-bind", codexDir, codexDir,
		"--bind", c.Home, c.Home,
		"--bind", c.Workdir, c.Workdir,
		"--chdir", c.Workdir,
	)
	for _, kv := range childEnv(c.Home, c.Tmp) {
		k, v, _ := strings.Cut(kv, "=")
		if k == "PATH" {
			// Only the Codex binary's own directory exists to search.
			v = codexDir
		}
		args = append(args, "--setenv", k, v)
	}
	return append(args, "--", c.Bin)
}
