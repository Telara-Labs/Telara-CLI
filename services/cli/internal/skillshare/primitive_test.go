package skillshare

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name string
	body string
	mode int64
	typ  byte
}

func packageOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h := &tar.Header{Name: e.name, Mode: mode, Typeflag: typ, Size: int64(len(e.body))}
		if typ == tar.TypeSymlink {
			h.Linkname, h.Size = e.body, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const manifestYAML = `apiVersion: primitives.telara.dev/v3
kind: Primitive
metadata:
  publisher: dev.telara
  name: recent-mail
  version: 0.2.0
  description: >-
    Lists recent mail
    in one line.
`

func install(pkg []byte) PrimitiveInstall {
	sum := sha256.Sum256(pkg)
	return PrimitiveInstall{Publisher: "dev.telara", Name: "recent-mail", Version: "0.2.0",
		ArtifactDigest: "sha256:" + hex.EncodeToString(sum[:]), Package: pkg}
}

func goodPackage(t *testing.T) []byte {
	return packageOf(t,
		entry{name: "primitive.yaml", body: manifestYAML},
		entry{name: "src/", typ: tar.TypeDir},
		entry{name: "src/main.sh", body: "echo hi\n", mode: 0o755},
	)
}

func TestInstallPrimitiveWritesARunnableSkillFolder(t *testing.T) {
	root := t.TempDir()
	res, err := InstallPrimitive(root, install(goodPackage(t)), false)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "recent-mail")
	if res.Path != dir || res.Overwrote || res.Unchanged {
		t.Fatalf("result %+v", res)
	}
	skill, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: recent-mail", "description: Lists recent mail in one line.", "tap_run", "package: " + dir, "dev.telara/recent-mail@0.2.0"} {
		if !strings.Contains(string(skill), want) {
			t.Errorf("SKILL.md lacks %q:\n%s", want, skill)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "src", "main.sh"))
	if err != nil || info.Mode()&0o100 == 0 {
		t.Fatalf("entrypoint missing or not executable: %v %v", info, err)
	}
	if m, ok := readPrimitiveMarker(dir); !ok || m.Ref != "dev.telara/recent-mail@0.2.0" {
		t.Fatalf("marker %+v %v", m, ok)
	}
	// The TAP runner lists a skills folder only when it carries its own
	// marker naming publisher/name with a digest.
	raw, err := os.ReadFile(filepath.Join(dir, RunnerMarker))
	if err != nil {
		t.Fatalf("the runner's marker is missing, so tap_search cannot list it: %v", err)
	}
	var rm runnerMarker
	if err := json.Unmarshal(raw, &rm); err != nil || rm.Name != "dev.telara/recent-mail" || rm.Digest == "" {
		t.Fatalf("runner marker %s: %+v %v", raw, rm, err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(root, ".recent-mail.installing-*"))
	if len(leftovers) != 0 {
		t.Fatalf("staging left behind: %v", leftovers)
	}
}

func TestInstallPrimitiveRefusesBytesThatAreNotWhatWasPromoted(t *testing.T) {
	root := t.TempDir()
	in := install(goodPackage(t))
	in.ArtifactDigest = "sha256:" + strings.Repeat("0", 64)
	if _, err := InstallPrimitive(root, in, false); err == nil || !strings.Contains(err.Error(), "refusing to use them") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "recent-mail")); !os.IsNotExist(err) {
		t.Fatal("nothing may be written when the digest disagrees")
	}
}

func TestInstallPrimitiveRefusesEntriesThatLeaveTheFolder(t *testing.T) {
	cases := map[string]entry{
		"parent path": {name: "../escape.sh", body: "x"},
		"deep parent": {name: "src/../../escape.sh", body: "x"},
		"absolute":    {name: "/tmp/escape.sh", body: "x"},
		"symlink":     {name: "link", body: "/etc/passwd", typ: tar.TypeSymlink},
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			pkg := packageOf(t, entry{name: "primitive.yaml", body: manifestYAML}, bad)
			if _, err := InstallPrimitive(root, install(pkg), false); err == nil {
				t.Fatal("expected a refusal")
			}
			if _, err := os.Stat(filepath.Join(root, "recent-mail")); !os.IsNotExist(err) {
				t.Fatal("a refused package must leave nothing installed")
			}
		})
	}
}

func TestInstallPrimitiveReinstallAndUpdate(t *testing.T) {
	root := t.TempDir()
	if _, err := InstallPrimitive(root, install(goodPackage(t)), false); err != nil {
		t.Fatal(err)
	}
	res, err := InstallPrimitive(root, install(goodPackage(t)), false)
	if err != nil || !res.Unchanged {
		t.Fatalf("same digest again: %+v %v", res, err)
	}
	next := packageOf(t,
		entry{name: "primitive.yaml", body: strings.Replace(manifestYAML, "0.2.0", "0.3.0", 1)},
		entry{name: "src/main.sh", body: "echo v3\n"},
	)
	in := install(next)
	in.Version = "0.3.0"
	res, err = InstallPrimitive(root, in, false)
	if err != nil || !res.Overwrote {
		t.Fatalf("update: %+v %v", res, err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "recent-mail", "src", "main.sh"))
	if string(body) != "echo v3\n" {
		t.Fatalf("not updated: %q", body)
	}
}

func TestInstallPrimitiveDoesNotReplaceSomeonesOwnSkill(t *testing.T) {
	root := t.TempDir()
	own := filepath.Join(root, "recent-mail")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "SKILL.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := InstallPrimitive(root, install(goodPackage(t)), false)
	var notOurs *ErrNotAPrimitiveFolder
	if !errors.As(err, &notOurs) {
		t.Fatalf("err %v", err)
	}
	if body, _ := os.ReadFile(filepath.Join(own, "SKILL.md")); string(body) != "mine" {
		t.Fatal("the user's skill was touched")
	}
	if _, err := InstallPrimitive(root, install(goodPackage(t)), true); err != nil {
		t.Fatalf("--force: %v", err)
	}
}

func TestInstallPrimitiveNeedsAManifest(t *testing.T) {
	pkg := packageOf(t, entry{name: "src/main.sh", body: "echo\n"})
	if _, err := InstallPrimitive(t.TempDir(), install(pkg), false); err == nil || !strings.Contains(err.Error(), "primitive.yaml") {
		t.Fatalf("err %v", err)
	}
}

// A folder pulled before the runner's marker existed gets it on the next pull
// of the same digest, instead of staying invisible to tap_search.
func TestInstallPrimitiveBackfillsTheRunnerMarker(t *testing.T) {
	root := t.TempDir()
	if _, err := InstallPrimitive(root, install(goodPackage(t)), false); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "recent-mail", RunnerMarker)
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	res, err := InstallPrimitive(root, install(goodPackage(t)), false)
	if err != nil || !res.Unchanged {
		t.Fatalf("same digest again: %+v %v", res, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the runner's marker was not backfilled: %v", err)
	}
}
