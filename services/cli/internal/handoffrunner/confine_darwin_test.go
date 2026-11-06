//go:build darwin

package handoffrunner

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSeatbeltProfileIsClosedByDefault(t *testing.T) {
	p := seatbeltProfile([]int{4101, 4102})
	for _, want := range []string{
		"(deny default)",
		`(allow process-exec (subpath (param "CODEX_DIR")))`,
		`(allow file-write* (subpath (param "CODEX_HOME")) (subpath (param "WORKDIR")))`,
		`(allow network-outbound (remote ip "localhost:4101"))`,
		`(allow network-outbound (remote ip "localhost:4102"))`,
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("profile lacks %s", want)
		}
	}
	for _, never := range []string{"/etc/passwd", "/Users", "/bin", "localhost:*", "(remote ip \"*", "network-inbound", "network-bind", "com.apple.SecurityServer", "/private/tmp", "(subpath \"/private/var/folders"} {
		if strings.Contains(p, never) {
			t.Fatalf("profile allows %q", never)
		}
	}
	if strings.Count(p, "network-outbound") != 2 {
		t.Fatalf("network rules: %s", p)
	}
}

func TestConfineCommandRunsTheBinaryUnderTheProfile(t *testing.T) {
	cmd := exec.Command("/opt/c/codex", "app-server", "--disable", "shell_tool")
	c := &confinement{Bin: "/opt/c/codex", Home: "/r/h", Workdir: "/r/w", Ports: []int{9}}
	if _, err := confineCommand(cmd, c); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	if cmd.Path != sandboxExec || !strings.HasPrefix(joined, sandboxExec+" -D CODEX_DIR=/opt/c -D CODEX_HOME=/r/h -D WORKDIR=/r/w -p ") || !strings.HasSuffix(joined, " /opt/c/codex app-server --disable shell_tool") {
		t.Fatalf("confined command %s", joined)
	}
}
