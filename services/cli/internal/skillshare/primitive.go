package skillshare

// primitive.go installs a TAP primitive the way a skill is installed
// (TENG-2962, doc 34 ruling 39): a folder in the client's skills directory.
// The folder holds the unpacked package, a SKILL.md that tells the agent to
// run it with the TAP runner's tap_run tool at this folder's path, and a
// marker naming the ref and digest it came from. The client needs no wiring
// beyond the runner connected as an MCP server.
//
// The package's bytes are checked against the digest the registry recorded
// before anything is unpacked, and unpacking refuses anything that could land
// outside the folder.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
	"gopkg.in/yaml.v3"
)

// PrimitiveMarker is the file that says a skills folder is an installed
// primitive, and which one.
const PrimitiveMarker = discovery.PrimitiveMarkerFileName

// Bounds on unpacking. Publish rejects packages over 4 MiB compressed; these
// stop a package that decompresses far past that.
const (
	maxPrimitiveFiles    = 2000
	maxPrimitiveUnpacked = 64 << 20
)

// PrimitiveInstall is what InstallPrimitive needs from the registry.
type PrimitiveInstall struct {
	Publisher      string
	Name           string
	Version        string
	ArtifactDigest string
	Package        []byte // gzip tar, as published
}

// Ref is publisher/name@version.
func (p PrimitiveInstall) Ref() string {
	return p.Publisher + "/" + p.Name + "@" + p.Version
}

// Verify checks the package bytes against the digest the registry recorded
// for the promoted version.
func (p PrimitiveInstall) Verify() error {
	sum := sha256.Sum256(p.Package)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != p.ArtifactDigest {
		return fmt.Errorf("%s: received %d bytes digesting to %s, but the registry records %s; refusing to use them",
			p.Ref(), len(p.Package), got, p.ArtifactDigest)
	}
	return nil
}

type primitiveMarker struct {
	Ref            string `json:"ref"`
	ArtifactDigest string `json:"artifact_digest"`
}

// PrimitiveInstallResult reports what was written.
type PrimitiveInstallResult struct {
	Path      string
	Overwrote bool
	Unchanged bool // the same digest was already installed
}

// ErrNotAPrimitiveFolder is returned when the target folder exists and was
// not written by InstallPrimitive: someone's own skill of the same name.
type ErrNotAPrimitiveFolder struct{ Path string }

func (e *ErrNotAPrimitiveFolder) Error() string {
	return fmt.Sprintf("%s exists and is not an installed primitive; not replacing it", e.Path)
}

// InstallPrimitive verifies p and writes it under root/<name>. force replaces
// a folder that is not an installed primitive.
func InstallPrimitive(root string, p PrimitiveInstall, force bool) (*PrimitiveInstallResult, error) {
	if err := checkSkillDirName(p.Name); err != nil {
		return nil, err
	}
	if err := p.Verify(); err != nil {
		return nil, err
	}

	dest := filepath.Join(root, p.Name)
	res := &PrimitiveInstallResult{Path: dest}
	if _, err := os.Stat(dest); err == nil {
		prev, ok := readPrimitiveMarker(dest)
		switch {
		case ok && prev.ArtifactDigest == p.ArtifactDigest:
			res.Unchanged = true
			return res, nil
		case !ok && !force:
			return nil, &ErrNotAPrimitiveFolder{Path: dest}
		}
		res.Overwrote = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(root, "."+p.Name+".installing-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)

	if err := unpackPrimitive(p.Package, stage); err != nil {
		return nil, fmt.Errorf("%s: %w", p.Ref(), err)
	}
	description, err := primitiveDescription(stage)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Ref(), err)
	}
	if err := os.WriteFile(filepath.Join(stage, "SKILL.md"), []byte(primitiveSkillMD(p, description, dest)), 0o644); err != nil {
		return nil, err
	}
	marker, _ := json.MarshalIndent(primitiveMarker{Ref: p.Ref(), ArtifactDigest: p.ArtifactDigest}, "", "  ")
	if err := os.WriteFile(filepath.Join(stage, PrimitiveMarker), append(marker, '\n'), 0o644); err != nil {
		return nil, err
	}

	// Swap in whole: a reader sees the old folder or the new one, never half.
	if res.Overwrote {
		old := stage + ".old"
		if err := os.Rename(dest, old); err != nil {
			return nil, err
		}
		if err := os.Rename(stage, dest); err != nil {
			_ = os.Rename(old, dest)
			return nil, err
		}
		_ = os.RemoveAll(old)
		return res, nil
	}
	if err := os.Rename(stage, dest); err != nil {
		return nil, err
	}
	return res, nil
}

func checkSkillDirName(name string) error {
	if strings.TrimSpace(name) == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return fmt.Errorf("refusing to install primitive with unsafe name %q", name)
	}
	return nil
}

func readPrimitiveMarker(dir string) (primitiveMarker, bool) {
	var m primitiveMarker
	raw, err := os.ReadFile(filepath.Join(dir, PrimitiveMarker))
	if err != nil || json.Unmarshal(raw, &m) != nil || m.ArtifactDigest == "" {
		return primitiveMarker{}, false
	}
	return m, true
}

// unpackPrimitive writes regular files and directories only. Links, devices
// and any path that is absolute or climbs out of dest are refused, not
// skipped: a package carrying one is not the package that was reviewed.
func unpackPrimitive(pkg []byte, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(pkg))
	if err != nil {
		return fmt.Errorf("package is not gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var files int
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read package: %w", err)
		}
		name := filepath.Clean(filepath.FromSlash(h.Name))
		if name == "." {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("package entry %q is outside the package", h.Name)
		}
		target := filepath.Join(dest, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			files++
			total += h.Size
			if files > maxPrimitiveFiles || total > maxPrimitiveUnpacked {
				return fmt.Errorf("package unpacks past %d files or %d bytes", maxPrimitiveFiles, maxPrimitiveUnpacked)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(tr, h.Size))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("package entry %q is not a regular file or directory", h.Name)
		}
	}
	if files == 0 {
		return fmt.Errorf("package is empty")
	}
	return nil
}

// primitiveDescription reads metadata.description from the unpacked
// primitive.yaml, which the publish pipeline requires.
func primitiveDescription(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "primitive.yaml"))
	if err != nil {
		return "", fmt.Errorf("package has no primitive.yaml")
	}
	var m struct {
		Metadata struct {
			Description string `yaml:"description"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("primitive.yaml: %w", err)
	}
	return strings.Join(strings.Fields(m.Metadata.Description), " "), nil
}

// primitiveSkillMD is the entry the client loads. It carries the absolute
// path because tap_run takes the package's directory.
func primitiveSkillMD(p PrimitiveInstall, description, dir string) string {
	if description == "" {
		description = "TAP primitive " + p.Ref() + "."
	}
	desc, _ := yaml.Marshal(description)
	return fmt.Sprintf(`---
name: %s
description: %s---

# %s

This skill is the TAP primitive %s, distributed by your Telara tenant.

Run it with the TAP runner's `+"`tap_run`"+` tool, giving this folder as the package:

    package: %s

Pass the arguments the primitive's schemas/ describe. The runner asks you to
approve any change it would make.

If no `+"`tap_run`"+` tool is available, the runner is not connected to this
client. Install it with `+"`tap-runtime install --client <client>`"+`.
`, p.Name, string(desc), p.Name, p.Ref(), dir)
}
