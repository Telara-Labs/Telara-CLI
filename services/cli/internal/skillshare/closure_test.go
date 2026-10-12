package skillshare

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A member is a package the "registry" holds, with the attestation it would
// sign for it.
type member struct {
	pkg       PrimitiveInstall
	runDigest string
}

// buildMember makes a package named name that uses the given members, and the
// verified attestation the registry would send for it. signedRun overrides
// the run digest the attestation signs ("-" signs none).
func buildMember(t *testing.T, s signer, name string, uses []member, signedRun string) member {
	t.Helper()
	yaml := fmt.Sprintf("apiVersion: primitives.telara.dev/v3\nkind: Primitive\nmetadata:\n  publisher: dev.telara\n  name: %s\n  version: 1.0.0\n  description: %s does a thing.\nexecution:\n  entrypoint: main.sh\n", name, name)
	var deps []map[string]string
	if len(uses) > 0 {
		yaml += "uses:\n"
		for i, u := range uses {
			yaml += fmt.Sprintf("  - {alias: u%d, ref: dev.telara/%s, version: 1.0.0, digest: %s, effect: read}\n", i, u.pkg.Name, u.runDigest)
			deps = append(deps, map[string]string{"ref": "dev.telara/" + u.pkg.Name, "version": "1.0.0", "run_digest": u.runDigest})
		}
	}
	script := "echo " + name + "\n"
	pkg := packageOf(t, entry{name: "primitive.yaml", body: yaml}, entry{name: "main.sh", body: script, mode: 0o755})
	sum := sha256.Sum256(pkg)
	rsum := sha256.Sum256([]byte(yaml + script)) // sha256(primitive.yaml || entrypoint), computed here, not by the code under test
	rd := hex.EncodeToString(rsum[:])
	p := PrimitiveInstall{Publisher: "dev.telara", Name: name, Version: "1.0.0", ArtifactDigest: "sha256:" + hex.EncodeToString(sum[:]), Package: pkg}

	signed := map[string]any{"validated": true, "artifact_digest": p.ArtifactDigest, "publisher": p.Publisher, "name": p.Name, "version": p.Version}
	switch signedRun {
	case "":
		signed["run_digest"] = rd
	case "-":
	default:
		signed["run_digest"] = signedRun
	}
	if deps != nil {
		signed["dependencies"] = deps
	}
	payload, _ := json.Marshal(signed)
	v, err := VerifyAttestation(p, s.attest(string(payload)), []SigningKey{s.key})
	if err != nil {
		t.Fatal(err)
	}
	p.Attestation = v
	return member{pkg: p, runDigest: rd}
}

func registryOf(members ...member) (Fetcher, *[]string) {
	var asked []string
	return func(ref string) (PrimitiveInstall, error) {
		asked = append(asked, ref)
		for _, m := range members {
			if ref == "dev.telara/"+m.pkg.Name+"@"+m.pkg.Version || ref == "dev.telara/"+m.pkg.Name {
				return m.pkg, nil
			}
		}
		return PrimitiveInstall{}, fmt.Errorf("not promoted: %s", ref)
	}, &asked
}

func names(ms []PrimitiveInstall) string {
	var n []string
	for _, m := range ms {
		n = append(n, m.Name)
	}
	return strings.Join(n, ",")
}

func TestClosureInstallsChildrenFirstThenTheParent(t *testing.T) {
	s := newSigner(t, "v1")
	leaf := buildMember(t, s, "leaf", nil, "")
	mid := buildMember(t, s, "mid", []member{leaf}, "")
	other := buildMember(t, s, "other", []member{leaf}, "") // diamond: leaf used twice
	top := buildMember(t, s, "top", []member{mid, other}, "")
	fetch, asked := registryOf(leaf, mid, other, top)

	got, err := ResolveClosure(top.pkg, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if names(got) != "leaf,mid,other,top" {
		t.Fatalf("order %s", names(got))
	}
	if len(*asked) != 4 { // mid, leaf, other, leaf again (verified each time it is asked for)
		t.Fatalf("asked %v", *asked)
	}

	// Installing the closure puts every member where the runner catalogs it.
	root := t.TempDir()
	for _, m := range got {
		if _, err := InstallPrimitive(root, m, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range got {
		marker, err := os.ReadFile(filepath.Join(root, m.Name, RunnerMarker))
		if err != nil || !strings.Contains(string(marker), "dev.telara/"+m.Name) {
			t.Fatalf("%s has no runner marker: %v", m.Name, err)
		}
		if _, err := os.Stat(filepath.Join(root, m.Name, AttestationFile)); err != nil {
			t.Fatalf("%s has no attestation beside it: %v", m.Name, err)
		}
	}
}

func TestAPrimitiveWithoutUsesIsItsOwnClosure(t *testing.T) {
	s := newSigner(t, "v1")
	solo := buildMember(t, s, "solo", nil, "")
	fetch, asked := registryOf()
	got, err := ResolveClosure(solo.pkg, fetch)
	if err != nil || names(got) != "solo" || len(*asked) != 0 {
		t.Fatalf("got %s %v asked %v", names(got), err, *asked)
	}
}

func TestClosureRefusesAChildWhoseRunDigestIsNotThePin(t *testing.T) {
	s := newSigner(t, "v1")
	approved := buildMember(t, s, "kid", nil, "")
	parent := buildMember(t, s, "parent", []member{approved}, "")

	// The registry delivers a different build of the same ref and version.
	swapped := buildMember(t, s, "kid", nil, "")
	swapped.pkg.Package = packageOf(t,
		entry{name: "primitive.yaml", body: "apiVersion: primitives.telara.dev/v3\nkind: Primitive\nmetadata:\n  publisher: dev.telara\n  name: kid\n  version: 1.0.0\n  description: kid does a thing.\nexecution:\n  entrypoint: main.sh\n"},
		entry{name: "main.sh", body: "echo something else\n", mode: 0o755})
	sum := sha256.Sum256(swapped.pkg.Package)
	swapped.pkg.ArtifactDigest = "sha256:" + hex.EncodeToString(sum[:]) // digest-consistent: Verify alone would pass

	fetch, _ := registryOf(swapped)
	_, err := ResolveClosure(parent.pkg, fetch)
	if err == nil || !strings.Contains(err.Error(), "run digest is") || !strings.Contains(err.Error(), approved.runDigest) {
		t.Fatalf("err %v", err)
	}
}

func TestClosureRefusesWhatTheRegistryCannotVouchFor(t *testing.T) {
	s := newSigner(t, "v1")
	ok := buildMember(t, s, "kid", nil, "")
	parent := buildMember(t, s, "parent", []member{ok}, "")

	cases := map[string]struct {
		child func() PrimitiveInstall
		want  string
	}{
		"no attestation": {func() PrimitiveInstall { c := ok.pkg; c.Attestation = nil; return c }, "without a verified attestation"},
		"bytes differ from the recorded digest": {func() PrimitiveInstall {
			c := ok.pkg
			c.Package = append(append([]byte{}, c.Package...), 0)
			return c
		}, "refusing to use them"},
		"signed run digest is another": {func() PrimitiveInstall {
			return buildMember(t, s, "kid", nil, strings.Repeat("e", 64)).pkg
		}, "signed run digest is"},
		"signed before run digests existed": {func() PrimitiveInstall {
			return buildMember(t, s, "kid", nil, "-").pkg
		}, "signed before attestations carried a run digest"},
		"another primitive": {func() PrimitiveInstall { c := ok.pkg; c.Name = "impostor"; return c }, "but the registry delivered"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			child := tc.child()
			fetch := func(string) (PrimitiveInstall, error) { return child, nil }
			if _, err := ResolveClosure(parent.pkg, fetch); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
		})
	}
}

func TestClosureRefusesWhenTheRegistryWillNotDeliverAChild(t *testing.T) {
	s := newSigner(t, "v1")
	kid := buildMember(t, s, "kid", nil, "")
	parent := buildMember(t, s, "parent", []member{kid}, "")
	fetch, _ := registryOf() // the child is not promoted any more
	_, err := ResolveClosure(parent.pkg, fetch)
	if err == nil || !strings.Contains(err.Error(), "dev.telara/kid@1.0.0") || !strings.Contains(err.Error(), "not promoted") {
		t.Fatalf("err %v", err)
	}
}

func TestClosureRefusesAPinTheParentsSignedAttestationDoesNotList(t *testing.T) {
	s := newSigner(t, "v1")
	kid := buildMember(t, s, "kid", nil, "")
	other := buildMember(t, s, "other", nil, "")
	parent := buildMember(t, s, "parent", []member{kid}, "")
	// A parent whose signed dependencies name a different child than its yaml.
	signed := map[string]any{"validated": true, "artifact_digest": parent.pkg.ArtifactDigest, "publisher": "dev.telara", "name": "parent", "version": "1.0.0",
		"dependencies": []map[string]string{{"ref": "dev.telara/other", "version": "1.0.0", "run_digest": other.runDigest}}}
	b, _ := json.Marshal(signed)
	v, err := VerifyAttestation(parent.pkg, s.attest(string(b)), []SigningKey{s.key})
	if err != nil {
		t.Fatal(err)
	}
	p := parent.pkg
	p.Attestation = v
	fetch, _ := registryOf(kid, other)
	if _, err := ResolveClosure(p, fetch); err == nil || !strings.Contains(err.Error(), "does not list the pin") {
		t.Fatalf("err %v", err)
	}
}

func TestClosureRefusesTooDeepATree(t *testing.T) {
	s := newSigner(t, "v1")
	chain := buildMember(t, s, "n0", nil, "")
	all := []member{chain}
	for i := 1; i <= 5; i++ {
		chain = buildMember(t, s, fmt.Sprintf("n%d", i), []member{chain}, "")
		all = append(all, chain)
	}
	fetch, _ := registryOf(all...)
	if _, err := ResolveClosure(chain.pkg, fetch); err == nil || !strings.Contains(err.Error(), "more than 4 deep") {
		t.Fatalf("err %v", err)
	}
}

func TestPackagePinsAndRunDigestReadTheDeliveredBytes(t *testing.T) {
	s := newSigner(t, "v1")
	kid := buildMember(t, s, "kid", nil, "")
	parent := buildMember(t, s, "parent", []member{kid}, "")
	pins, err := PackagePins(parent.pkg.Package)
	if err != nil || len(pins) != 1 || pins[0].Ref != "dev.telara/kid" || pins[0].Digest != kid.runDigest {
		t.Fatalf("pins %+v %v", pins, err)
	}
	got, err := PackageRunDigest(parent.pkg.Package)
	if err != nil || got != parent.runDigest {
		t.Fatalf("run digest %s %v, want %s", got, err, parent.runDigest)
	}
}
