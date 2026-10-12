package skillshare

// closure.go resolves the primitives a primitive uses, so that pulling a
// parent installs everything it will run. The runner pins each child by its
// run digest in the parent's own primitive.yaml (`uses:`), and finds a child
// only in the local catalog, so a parent without its children installed
// cannot run.
//
// The pins are read from the parent's package, which the parent's signed
// attestation vouches for, never from anything the registry says on the side.
// Every member of the closure is fetched and verified (digest and signed
// attestation) before anything is written, and the child's run digest is
// checked three ways: recomputed from its own bytes, equal to the pin, and
// equal to the run digest its signed attestation carries.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// MaxClosureDepth is how deep `uses` may go, the root being 0. It is the
// runner's limit.
const MaxClosureDepth = 4

// Pin is one `uses` entry of a package.
type Pin struct {
	Alias   string `yaml:"alias"`
	Ref     string `yaml:"ref"` // publisher/name
	Version string `yaml:"version"`
	Digest  string `yaml:"digest"` // run digest of the child
}

var runDigestRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// readPackageFile returns one regular file of a gzip tar package.
func readPackageFile(pkg []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(pkg))
	if err != nil {
		return nil, fmt.Errorf("package is not gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	want := path.Clean(filepath.ToSlash(name))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("package has no %s", name)
		}
		if err != nil {
			return nil, fmt.Errorf("read package: %w", err)
		}
		if h.Typeflag == tar.TypeReg && path.Clean(filepath.ToSlash(h.Name)) == want {
			return io.ReadAll(io.LimitReader(tr, maxPrimitiveUnpacked))
		}
	}
}

// PackagePins returns the primitives a package declares under `uses`.
func PackagePins(pkg []byte) ([]Pin, error) {
	raw, err := readPackageFile(pkg, "primitive.yaml")
	if err != nil {
		return nil, err
	}
	var m struct {
		Uses []Pin `yaml:"uses"`
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("primitive.yaml: %w", err)
	}
	return m.Uses, nil
}

// PackageRunDigest is the digest the runner pins a package by: sha256 over
// primitive.yaml followed by the entrypoint program, in hex. It is the
// definition of the contract module's RunDigest, recomputed here from the
// delivered bytes so that a pin is compared with what will actually run.
func PackageRunDigest(pkg []byte) (string, error) {
	raw, err := readPackageFile(pkg, "primitive.yaml")
	if err != nil {
		return "", err
	}
	var m struct {
		Execution struct {
			Entrypoint string `yaml:"entrypoint"`
		} `yaml:"execution"`
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("primitive.yaml: %w", err)
	}
	if strings.TrimSpace(m.Execution.Entrypoint) == "" {
		return "", fmt.Errorf("primitive.yaml names no entrypoint")
	}
	script, err := readPackageFile(pkg, m.Execution.Entrypoint)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append(append([]byte{}, raw...), script...))
	return hex.EncodeToString(sum[:]), nil
}

// signedRunDigest is the run digest inside an attestation's signed bytes, ""
// when it carries none.
func signedRunDigest(v *VerifiedAttestation) string {
	var f struct {
		RunDigest string `json:"run_digest"`
	}
	if v == nil || json.Unmarshal(v.Payload, &f) != nil {
		return ""
	}
	return f.RunDigest
}

// signedDependencies are the pins inside a parent's signed attestation.
func signedDependencies(v *VerifiedAttestation) []struct{ Ref, Version, RunDigest string } {
	var f struct {
		Dependencies []struct {
			Ref       string `json:"ref"`
			Version   string `json:"version"`
			RunDigest string `json:"run_digest"`
		} `json:"dependencies"`
	}
	if v == nil || json.Unmarshal(v.Payload, &f) != nil {
		return nil
	}
	out := make([]struct{ Ref, Version, RunDigest string }, 0, len(f.Dependencies))
	for _, d := range f.Dependencies {
		out = append(out, struct{ Ref, Version, RunDigest string }{d.Ref, d.Version, d.RunDigest})
	}
	return out
}

// Fetcher returns a promoted package whose digest and signed attestation are
// already verified, for a ref written publisher/name or publisher/name@version.
type Fetcher func(ref string) (PrimitiveInstall, error)

// ResolveClosure returns root and everything it uses, in install order:
// children before the parents that use them, root last, each member once.
// Nothing is written. Any failure refuses the whole closure.
func ResolveClosure(root PrimitiveInstall, fetch Fetcher) ([]PrimitiveInstall, error) {
	var order []PrimitiveInstall
	seen := map[string]bool{} // run digest -> already in order
	onPath := map[string]bool{}
	var walk func(p PrimitiveInstall, runDigest string, depth int) error
	walk = func(p PrimitiveInstall, runDigest string, depth int) error {
		if seen[runDigest] {
			return nil
		}
		if onPath[runDigest] {
			return fmt.Errorf("%s uses itself through a chain of primitives", p.Ref())
		}
		pins, err := PackagePins(p.Package)
		if err != nil {
			return fmt.Errorf("%s: %w", p.Ref(), err)
		}
		if len(pins) > 0 && depth >= MaxClosureDepth {
			return fmt.Errorf("%s: primitives use primitives more than %d deep; the runner would refuse this", p.Ref(), MaxClosureDepth)
		}
		onPath[runDigest] = true
		defer delete(onPath, runDigest)

		signed := signedDependencies(p.Attestation)
		for _, pin := range pins {
			if !runDigestRe.MatchString(pin.Digest) {
				return fmt.Errorf("%s: uses %s with digest %q, which is not a run digest", p.Ref(), pin.Ref, pin.Digest)
			}
			if len(signed) > 0 {
				ok := false
				for _, d := range signed {
					ok = ok || (d.Ref == pin.Ref && d.RunDigest == pin.Digest)
				}
				if !ok {
					return fmt.Errorf("%s: its signed attestation does not list the pin %s@%s that its primitive.yaml declares", p.Ref(), pin.Ref, pin.Digest)
				}
			}
			version := pin.Version
			if version == "" {
				for _, d := range signed {
					if d.Ref == pin.Ref && d.RunDigest == pin.Digest {
						version = d.Version
					}
				}
			}
			ref := pin.Ref
			if version != "" {
				ref += "@" + version
			}
			child, err := fetch(ref)
			if err != nil {
				return fmt.Errorf("%s uses %s (alias %s): %w", p.Ref(), ref, pin.Alias, err)
			}
			if child.Publisher+"/"+child.Name != pin.Ref {
				return fmt.Errorf("%s uses %s, but the registry delivered %s", p.Ref(), pin.Ref, child.Ref())
			}
			if err := child.Verify(); err != nil {
				return err
			}
			if child.Attestation == nil {
				return fmt.Errorf("%s: used by %s but delivered without a verified attestation; refusing", child.Ref(), p.Ref())
			}
			got, err := PackageRunDigest(child.Package)
			if err != nil {
				return fmt.Errorf("%s: %w", child.Ref(), err)
			}
			if got != pin.Digest {
				return fmt.Errorf("%s: its run digest is %s, but %s pins %s; refusing a child that is not the one that was approved", child.Ref(), got, p.Ref(), pin.Digest)
			}
			sd := signedRunDigest(child.Attestation)
			if sd == "" {
				return fmt.Errorf("%s: signed before attestations carried a run digest, so nothing signed binds it to the pin %s; republish it", child.Ref(), pin.Digest)
			}
			if sd != pin.Digest {
				return fmt.Errorf("%s: its signed run digest is %s, but %s pins %s", child.Ref(), sd, p.Ref(), pin.Digest)
			}
			if err := walk(child, got, depth+1); err != nil {
				return err
			}
		}
		seen[runDigest] = true
		order = append(order, p)
		return nil
	}
	rd, err := PackageRunDigest(root.Package)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", root.Ref(), err)
	}
	if err := walk(root, rd, 0); err != nil {
		return nil, err
	}
	return order, nil
}
