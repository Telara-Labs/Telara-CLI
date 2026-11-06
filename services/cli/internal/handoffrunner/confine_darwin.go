//go:build darwin

package handoffrunner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// sandboxExec is macOS's Seatbelt launcher. It is deprecated as a public tool but
// is what Codex itself uses for its own sandbox, and it is present on every macOS.
const sandboxExec = "/usr/bin/sandbox-exec"

// confinementName is how the check and the log name the layer in force.
const confinementName = "macOS sandbox-exec"

// seatbeltProfile is the whole policy for the Codex process and everything it
// starts. Closed by default; it allows, and nothing else:
//   - running programs from the Codex binary's own directory only (so no shell,
//     no /bin, no interpreter: a command Codex tries to start fails);
//   - reading the system libraries and frameworks the loader maps, the time zone
//     data and the standard device files (the same minimal platform set Codex's
//     own sandbox uses, without /etc/passwd and /etc/master.passwd);
//   - reading and writing the runner's Codex home and the run's empty working
//     directory, and stat'ing their parent directories (not listing them);
//   - the Mach services the Rust runtime and TLS stack look up, and no others;
//   - connecting to the run's own loopback ports only (the model proxy and the
//     session proxy); no other address, no DNS, no listening socket.
//
// Paths arrive as -D parameters, so no path is spliced into the policy text.
func seatbeltProfile(ports []int) string {
	var net strings.Builder
	for _, p := range ports {
		fmt.Fprintf(&net, "(allow network-outbound (remote ip \"localhost:%d\"))\n", p)
	}
	return `(version 1)
(deny default)
(allow process-fork)
(allow process-exec (subpath (param "CODEX_DIR")))
(allow signal (target same-sandbox))
(allow process-info* (target same-sandbox))
(allow sysctl-read)
(allow pseudo-tty)
(allow ipc-posix-sem)
(allow system-mac-syscall (mac-policy-name "vnguard"))
(allow system-mac-syscall (require-all (mac-policy-name "Sandbox") (mac-syscall-number 67)))
(allow system-fsctl (fsctl-command FSIOC_CAS_BSDFLAGS))
(allow iokit-open (iokit-registry-entry-class "RootDomainUserClient"))
(allow mach-lookup
  (global-name "com.apple.bsd.dirhelper")
  (global-name "com.apple.system.opendirectoryd.membership")
  (global-name "com.apple.system.opendirectoryd.libinfo")
  (global-name "com.apple.system.notification_center")
  (global-name "com.apple.logd")
  (global-name "com.apple.system.logger"))
(allow file-map-executable
  (subpath "/System/Library") (subpath "/Library/Apple") (subpath "/usr/lib") (subpath "/System/iOSSupport")
  (subpath (param "CODEX_DIR")))
(allow file-read* file-test-existence
  (subpath "/System/Library") (subpath "/Library/Apple") (subpath "/usr/lib") (subpath "/System/iOSSupport")
  (subpath "/private/var/db/timezone") (subpath "/usr/share/zoneinfo") (subpath "/private/var/db/dyld")
  (literal "/") (literal "/dev/null") (literal "/dev/random") (literal "/dev/urandom") (literal "/dev/autofs_nowait")
  (literal "/private/etc/localtime")
  (subpath (param "CODEX_DIR")) (subpath (param "CODEX_HOME")) (subpath (param "WORKDIR")))
(allow file-read-metadata file-test-existence
  (literal "/etc") (literal "/tmp") (literal "/var")
  (path-ancestors "/System/Volumes/Data/private")
  (path-ancestors (param "CODEX_HOME")) (path-ancestors (param "WORKDIR")) (path-ancestors (param "CODEX_DIR")))
(allow file-write* (subpath (param "CODEX_HOME")) (subpath (param "WORKDIR")))
(allow file-write-data file-ioctl (literal "/dev/null"))
` + net.String()
}

// confinementAvailable is sandbox-exec, which every macOS has.
func confinementAvailable() (string, bool) {
	if _, err := os.Stat(sandboxExec); err != nil {
		return sandboxExec + " is missing", false
	}
	return confinementName, true
}

// confineCommand rewrites cmd to start under the profile. cmd.Path and cmd.Args
// must name the Codex binary and its arguments.
func confineCommand(cmd *exec.Cmd, c *confinement) (string, error) {
	args := []string{sandboxExec,
		"-D", "CODEX_DIR=" + filepath.Dir(c.Bin),
		"-D", "CODEX_HOME=" + c.Home,
		"-D", "WORKDIR=" + c.Workdir,
		"-p", seatbeltProfile(c.Ports),
		c.Bin}
	cmd.Path = sandboxExec
	cmd.Args = append(args, cmd.Args[1:]...)
	return confinementName, nil
}
