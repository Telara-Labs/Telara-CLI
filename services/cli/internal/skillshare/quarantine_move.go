package skillshare

// quarantine_move.go moves a skill directory to the quarantine root when a
// plain rename cannot (TENG-2760).
//
// os.Rename fails with EXDEV when the skill and the quarantine root are on
// different filesystems, the ordinary layout of a project skill on a bind mount
// or a separate volume. Without a fallback the enforcement fails and the
// withdrawn skill keeps loading.
//
// THE RULE: NOTHING THE PERSON OWNS IS EVER UNLINKED. Checking a file and then
// removing it by path is a race. An editor's atomic save, or the person
// re-creating the skill, can put new bytes at that path between the check and
// the unlink, and the unlink destroys bytes that were never copied. No amount of
// re-checking closes that window, so the fallback has no delete step at all.
//
// The fallback is hold, copy, verify, commit:
//
//  1. Hold. One atomic rename on the skill's own filesystem takes the whole
//     directory out of the load path and into a fresh private container inside
//     <skills root>/../.telara-hold/, which is outside every known skills root.
//     From this instant the original path is empty or whatever the person
//     writes there next, and the fallback never touches that path again. If the
//     whole package cannot be held on its own filesystem the move fails and
//     reports it. There is no partial substitute, so a skill is never reported
//     quarantined while any of it still loads.
//  2. Copy. The held tree is copied into a private sibling of the destination.
//     Every read and write goes through an os.Root opened on the held tree or on
//     the staging directory, so a directory swapped for a link to somewhere
//     else is refused instead of followed. Links are recreated as links and
//     never read through. A pipe, socket or device aborts the move.
//  3. Verify. The staged copy is compared with the manifest (kind, permission
//     bits, size, SHA-256) and the held tree is compared with it again. A
//     difference aborts with the staging directory removed and the held
//     original kept.
//  4. Commit. The staging directory is renamed onto the destination, one atomic
//     rename on the destination's filesystem.
//
// The held original is retained after success. The caller reports its path in
// the action and in REMOVED.md so the person can delete it when they choose. A
// duplicate of a withdrawn skill outside every load path is a smaller cost than
// a deleted edit.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
)

const holdDir = ".telara-hold"

// Seams for tests. Production uses os.Rename and no hooks.
var (
	renameDir                   = os.Rename
	knownSkillRoots             = discovery.SkillRoots
	holdRename                  = func(r *os.Root, from, to string) error { return r.Rename(from, to) }
	betweenOpenAndRenameForTest func()           // after the handles are open, before the hold rename
	afterStageForTest           func()           // after the copy, before verification
	beforeCommitForTest         func()           // after verification, before the commit rename
	beforeEntryForTest          func(rel string) // before each entry of the held tree is inspected
)

type manifestEntry struct {
	rel     string // path inside the held tree
	dstRel  string // path inside the copy
	mode    fs.FileMode
	size    int64
	modTime time.Time
	sum     string // hex SHA-256 of a regular file
	link    string // target of a symbolic link, verbatim
}

// moveDir renames from to to, falling back across filesystems. The returned
// string is where the original bytes were retained, empty after a plain rename.
func moveDir(from, to, skillsRoot string) (string, error) {
	err := renameDir(from, to)
	if err == nil {
		return "", nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return "", err
	}
	return moveAcrossFilesystems(from, to, skillsRoot)
}

func moveAcrossFilesystems(from, to, skillsRoot string) (string, error) {
	if _, err := os.Lstat(to); err == nil {
		return "", fmt.Errorf("quarantine destination %s already exists", to)
	}
	src, held, err := holdOriginal(from, skillsRoot)
	if err != nil {
		return "", err
	}
	defer src.Close()
	if err := copyHeld(src, to); err != nil {
		return held, fmt.Errorf("the skill no longer loads and its original is kept at %s, but the quarantine copy failed: %w", held, err)
	}
	return held, nil
}

// holdOriginal renames the whole package into a fresh private container beside
// the skills root. It returns an os.Root bound to the package's own directory
// and the path where the original is kept.
//
// Every step runs through one os.Root on the directory that contains both the
// skills root and the holding parent, with paths relative to it, so a holding
// parent swapped for a link out of that directory is refused before anything is
// written. The returned handle is opened BEFORE the rename and refers to the
// directory itself, so the copy reads the inode that was held, never whatever a
// path resolves to afterwards. Two identity checks bind the pieces together:
// the handle must be the directory at the source path just before the rename,
// and the directory at the holding path must be the handle just after it. If
// either fails nothing is rolled back (the original path may by then belong to
// the person); the error says what was seen.
func holdOriginal(from, skillsRoot string) (*os.Root, string, error) {
	fail := func(err error) (*os.Root, string, error) {
		return nil, "", fmt.Errorf("the skill could not be held on its own filesystem, so it was not quarantined and may still load: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(skillsRoot)
	if err != nil {
		return fail(fmt.Errorf("resolve skills root: %w", err))
	}
	relInRoot, err := filepath.Rel(skillsRoot, from)
	if err != nil {
		return fail(err)
	}
	commonDir := filepath.Dir(realRoot)
	relFrom := filepath.Join(filepath.Base(realRoot), relInRoot)
	parent := filepath.Join(commonDir, holdDir)
	if err := checkHoldParent(parent, realRoot); err != nil {
		return fail(err)
	}
	common, err := os.OpenRoot(commonDir)
	if err != nil {
		return fail(err)
	}
	defer common.Close()
	holdRoot, err := common.OpenRoot(holdDir)
	if err != nil {
		return fail(err)
	}
	defer holdRoot.Close()
	if err := sameEntry(common, holdDir, holdRoot); err != nil {
		return fail(err)
	}
	src, err := common.OpenRoot(relFrom)
	if err != nil {
		return fail(err)
	}
	ok := false
	defer func() {
		if !ok {
			src.Close()
		}
	}()
	if err := sameEntry(common, relFrom, src); err != nil {
		return fail(err)
	}
	container, err := newContainer(holdRoot)
	if err != nil {
		return fail(err)
	}
	heldRel := filepath.Join(holdDir, container, filepath.Base(from))
	if betweenOpenAndRenameForTest != nil {
		betweenOpenAndRenameForTest()
	}
	if err := sameEntry(common, relFrom, src); err != nil {
		_ = holdRoot.Remove(container)
		return fail(err)
	}
	if err := holdRename(common, relFrom, heldRel); err != nil {
		_ = holdRoot.Remove(container) // empty, created above
		return fail(err)
	}
	held := filepath.Join(commonDir, heldRel)
	if err := sameEntry(common, heldRel, src); err != nil {
		return nil, "", fmt.Errorf("the skill was renamed to %s but that is not the directory that was opened, so it was not copied and the outcome is unconfirmed: %w", held, err)
	}
	ok = true
	return src, held, nil
}

// sameEntry requires the entry at rel inside root to be exactly the directory
// the handle refers to, without following a final link.
func sameEntry(root *os.Root, rel string, handle *os.Root) error {
	a, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	b, err := handle.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(a, b) {
		return fmt.Errorf("%s is not the directory that was opened", rel)
	}
	return nil
}

// checkHoldParent creates the holding parent if it is absent and refuses it if it
// is not a real directory outside every known skills root.
func checkHoldParent(parent, realRoot string) error {
	info, err := os.Lstat(parent)
	switch {
	case os.IsNotExist(err):
		if err := os.Mkdir(parent, 0o700); err != nil {
			return err
		}
	case err != nil:
		return err
	case info.Mode()&fs.ModeSymlink != 0 || !info.IsDir():
		return fmt.Errorf("%s is not a plain directory", parent)
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	roots := append([]string{realRoot}, knownSkillRoots()...)
	for _, r := range roots {
		cr := canonical(r)
		if isWithin(cr, realParent) || isWithin(realParent, cr) {
			return fmt.Errorf("%s overlaps the skills root %s", parent, r)
		}
	}
	return nil
}

func newContainer(parent *os.Root) (string, error) {
	for i := 0; i < 100; i++ {
		var b [6]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		c := fmt.Sprintf("%s-%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(b[:]), "hold")
		if err := parent.Mkdir(c, 0o700); err == nil {
			return c, nil
		} else if !os.IsExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("could not create a unique holding container")
}

// canonical resolves symbolic links in the longest existing prefix of path.
func canonical(path string) string {
	path = filepath.Clean(path)
	rest := ""
	for {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return filepath.Join(path, rest)
		}
		rest = filepath.Join(filepath.Base(path), rest)
		path = parent
	}
}

// isWithin reports whether path is dir or below it, lexically.
func isWithin(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func copyHeld(src *os.Root, to string) error {
	staging := to + ".partial"
	// Mkdir, not MkdirAll: an existing staging path is somebody else's.
	if err := os.Mkdir(staging, 0o700); err != nil {
		return fmt.Errorf("create quarantine staging directory: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging) // the directory created above, before the commit
		}
	}()

	dst, err := os.OpenRoot(staging)
	if err != nil {
		return err
	}
	defer dst.Close()

	manifest, err := copyTree(src, dst, func(rel string) string { return rel })
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if afterStageForTest != nil {
		afterStageForTest()
	}
	if err := verifyTree(dst, manifest); err != nil {
		return fmt.Errorf("verify the copy: %w", err)
	}
	if err := verifyHeldUnchanged(src, manifest); err != nil {
		return fmt.Errorf("the original changed while it was copied: %w", err)
	}
	if beforeCommitForTest != nil {
		beforeCommitForTest()
	}
	if err := os.Rename(staging, to); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true
	return nil
}

func osPath(rel string) string { return filepath.FromSlash(rel) }

func copyTree(src, dst *os.Root, dstName func(string) string) ([]manifestEntry, error) {
	var manifest []manifestEntry
	err := fs.WalkDir(src.FS(), ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		p := osPath(rel)
		if beforeEntryForTest != nil {
			beforeEntryForTest(p)
		}
		info, err := src.Lstat(p)
		if err != nil {
			return err
		}
		e := manifestEntry{rel: p, dstRel: dstName(p), mode: info.Mode(), size: info.Size(), modTime: info.ModTime()}
		switch {
		case info.IsDir():
			if p != "." {
				if err := dst.Mkdir(e.dstRel, 0o700); err != nil {
					return err
				}
			}
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := src.Readlink(p)
			if err != nil {
				return err
			}
			e.link = link
			if err := dst.Symlink(link, e.dstRel); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			sum, err := copyFile(src, dst, p, e.dstRel, info)
			if err != nil {
				return err
			}
			e.sum = sum
		default:
			return fmt.Errorf("%s is a %s, which cannot be copied safely", p, kindOf(info.Mode()))
		}
		manifest = append(manifest, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Directory permissions last, children first, so a read-only directory does
	// not block copying its own contents.
	for i := len(manifest) - 1; i >= 0; i-- {
		if e := manifest[i]; e.mode.IsDir() {
			if err := dst.Chmod(e.dstRel, e.mode.Perm()); err != nil {
				return nil, err
			}
			_ = dst.Chtimes(e.dstRel, e.modTime, e.modTime)
		}
	}
	return manifest, nil
}

// copyFile opens the file through the root, then checks that the open file is
// the one just inspected, so a swap between inspection and open is refused.
func copyFile(src, dst *os.Root, rel, dstRel string, inspected fs.FileInfo) (string, error) {
	in, err := src.Open(rel)
	if err != nil {
		return "", err
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(inspected, opened) || !opened.Mode().IsRegular() {
		return "", fmt.Errorf("%s changed while it was being copied", rel)
	}
	out, err := dst.OpenFile(dstRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := dst.Chmod(dstRel, inspected.Mode().Perm()); err != nil {
		return "", err
	}
	_ = dst.Chtimes(dstRel, inspected.ModTime(), inspected.ModTime())
	return hex.EncodeToString(h.Sum(nil)), nil
}

func kindOf(m fs.FileMode) string {
	switch {
	case m&fs.ModeNamedPipe != 0:
		return "named pipe"
	case m&fs.ModeSocket != 0:
		return "socket"
	case m&fs.ModeDevice != 0:
		return "device"
	}
	return "special file"
}

func verifyTree(dst *os.Root, manifest []manifestEntry) error {
	for _, e := range manifest {
		info, err := dst.Lstat(e.dstRel)
		if err != nil {
			return fmt.Errorf("%s missing from the copy", e.rel)
		}
		if info.Mode().Type() != e.mode.Type() {
			return fmt.Errorf("%s has a different kind in the copy", e.rel)
		}
		switch {
		case e.mode.IsDir():
			if info.Mode().Perm() != e.mode.Perm() {
				return fmt.Errorf("%s has different permissions in the copy", e.rel)
			}
		case e.mode&fs.ModeSymlink != 0:
			if link, err := dst.Readlink(e.dstRel); err != nil || link != e.link {
				return fmt.Errorf("%s points elsewhere in the copy", e.rel)
			}
		default:
			if info.Mode().Perm() != e.mode.Perm() || info.Size() != e.size {
				return fmt.Errorf("%s differs in the copy", e.rel)
			}
			if sum, err := hashFile(dst, e.dstRel); err != nil || sum != e.sum {
				return fmt.Errorf("%s has different content in the copy", e.rel)
			}
		}
	}
	return nil
}

// verifyHeldUnchanged requires the held tree to be exactly the manifest.
func verifyHeldUnchanged(src *os.Root, manifest []manifestEntry) error {
	seen := map[string]bool{}
	for _, e := range manifest {
		seen[e.rel] = true
		if err := heldMatches(src, e); err != nil {
			return err
		}
	}
	return fs.WalkDir(src.FS(), ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !seen[osPath(rel)] {
			return fmt.Errorf("%s was added", rel)
		}
		return nil
	})
}

func heldMatches(src *os.Root, e manifestEntry) error {
	info, err := src.Lstat(e.rel)
	if err != nil {
		return fmt.Errorf("%s is unreadable or gone: %w", e.rel, err)
	}
	if info.Mode() != e.mode {
		return fmt.Errorf("%s changed kind or permissions", e.rel)
	}
	switch {
	case e.mode.IsDir():
	case e.mode&fs.ModeSymlink != 0:
		if link, err := src.Readlink(e.rel); err != nil || link != e.link {
			return fmt.Errorf("%s now points elsewhere", e.rel)
		}
	default:
		if info.Size() != e.size || !info.ModTime().Equal(e.modTime) {
			return fmt.Errorf("%s was edited", e.rel)
		}
		if sum, err := hashFile(src, e.rel); err != nil || sum != e.sum {
			return fmt.Errorf("%s was edited", e.rel)
		}
	}
	return nil
}

func hashFile(r *os.Root, rel string) (string, error) {
	f, err := r.Open(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
