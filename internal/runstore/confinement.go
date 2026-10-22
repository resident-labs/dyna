package runstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func validateRunID(id string) error {
	if !strings.HasPrefix(id, "wf_") || id == "wf_" || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) || strings.ContainsRune(id, 0) {
		return fmt.Errorf("invalid run id %q", id)
	}
	return nil
}

// ValidateRunID reports whether id is a well-formed run identifier that cannot
// escape RunsDir when used as a single path element.
func ValidateRunID(id string) error {
	return validateRunID(id)
}

// ConfinedRunPath resolves name under RunsDir/runID after validating the id
// and rejecting symlink escapes from the runs directory. A missing path is
// returned as the lexical location inside RunsDir so callers can treat
// os.IsNotExist as a normal absence.
func ConfinedRunPath(runID string, elem ...string) (string, error) {
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	for _, e := range elem {
		if err := validatePathElem(e); err != nil {
			return "", err
		}
	}
	root, err := confinedRootFor(runID)
	if err != nil {
		return "", err
	}
	parts := append([]string{root, runID}, elem...)
	lexical := filepath.Join(parts...)
	if err := pathInside(root, lexical); err != nil {
		return "", err
	}
	acc := root
	for _, e := range append([]string{runID}, elem...) {
		acc = filepath.Join(acc, e)
		if err := rejectEscapeIfExists(root, acc); err != nil {
			if os.IsNotExist(err) {
				return lexical, nil
			}
			return "", err
		}
	}
	if resolved, err := filepath.EvalSymlinks(lexical); err == nil {
		if err := pathInside(root, resolved); err != nil {
			return "", err
		}
		return resolved, nil
	}
	return lexical, nil
}

func validatePathElem(e string) error {
	if e == "" || filepath.Base(e) != e || e == "." || e == ".." || strings.ContainsAny(e, `/\\`) || strings.ContainsRune(e, 0) {
		return fmt.Errorf("invalid run path element %q", e)
	}
	return nil
}

// CanonicalRunID returns the run id of the real directory for runID after
// confined symlink resolution. A missing run directory returns runID.
func CanonicalRunID(runID string) (string, error) {
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	root, err := confinedRootFor(runID)
	if err != nil {
		return "", err
	}
	resolved, err := resolveConfinedDir(root, runID)
	if err != nil {
		if os.IsNotExist(err) {
			return runID, nil
		}
		return "", err
	}
	return canonicalIDFromResolved(root, resolved)
}

// OpenConfinedRunFile opens a regular file under RunsDir/runID. In-tree
// aliases are resolved first; the last component is then opened with
// O_NOFOLLOW|O_NONBLOCK so a swapped symlink or FIFO cannot escape RunsDir
// or block the caller. The canonical run id is the first path element of
// the resolved location relative to RunsDir.
func OpenConfinedRunFile(runID string, name string) (*os.File, string, error) {
	if err := validateRunID(runID); err != nil {
		return nil, "", err
	}
	if err := validatePathElem(name); err != nil {
		return nil, "", err
	}
	root, err := confinedRootFor(runID)
	if err != nil {
		return nil, "", err
	}
	runDir, err := resolveConfinedDir(root, runID)
	if err != nil {
		return nil, runID, err
	}
	canon, err := canonicalIDFromResolved(root, runDir)
	if err != nil {
		return nil, "", err
	}
	openPath, fileCanon, err := resolveConfinedFile(root, runDir, name)
	if err != nil {
		if fileCanon != "" {
			canon = fileCanon
		}
		return nil, canon, err
	}
	if fileCanon != "" {
		canon = fileCanon
	}
	fd, err := syscall.Open(openPath, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, canon, err
	}
	f := os.NewFile(uintptr(fd), openPath)
	if f == nil {
		_ = syscall.Close(fd)
		return nil, canon, fmt.Errorf("open confined run file: invalid file descriptor")
	}
	if err := confineOpenFile(root, f, name); err != nil {
		f.Close()
		return nil, canon, err
	}
	return f, canon, nil
}

func resolveConfinedDir(root, runID string) (string, error) {
	lexical := filepath.Join(root, runID)
	info, err := os.Lstat(lexical)
	if err != nil {
		return "", err
	}
	resolved := lexical
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err = filepath.EvalSymlinks(lexical)
		if err != nil {
			return "", fmt.Errorf("run path %q is an unresolvable symlink", lexical)
		}
	} else if !info.IsDir() {
		return "", fmt.Errorf("run path %q is not a directory", lexical)
	}
	if err := pathInside(root, resolved); err != nil {
		return "", fmt.Errorf("run path escapes the runs directory via symlink")
	}
	st, err := os.Lstat(resolved)
	if err != nil {
		return "", err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("run path %q is an unresolvable symlink", resolved)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("run path %q is not a directory", lexical)
	}
	return resolved, nil
}

func resolveConfinedFile(root, runDir, name string) (openPath, fileCanon string, err error) {
	lexical := filepath.Join(runDir, name)
	info, err := os.Lstat(lexical)
	if err != nil {
		return "", "", err
	}
	openPath = lexical
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, rerr := filepath.EvalSymlinks(lexical)
		if rerr != nil {
			return "", "", fmt.Errorf("run path %q is an unresolvable symlink", lexical)
		}
		if err := pathInside(root, resolved); err != nil {
			return "", "", fmt.Errorf("run path escapes the runs directory via symlink")
		}
		openPath = resolved
		if fileCanon, err = canonicalIDFromResolved(root, filepath.Dir(resolved)); err != nil {
			return "", "", err
		}
		info, err = os.Lstat(openPath)
		if err != nil {
			return "", fileCanon, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fileCanon, fmt.Errorf("run path %q is an unresolvable symlink", openPath)
		}
	}
	if !info.Mode().IsRegular() {
		return "", fileCanon, fmt.Errorf("run file %q must be a regular file", name)
	}
	return openPath, fileCanon, nil
}

func confineOpenFile(root string, f *os.File, name string) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("run file %q must be a regular file", name)
	}
	link, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", f.Fd()))
	if err != nil {
		return nil
	}
	if err := pathInside(root, link); err != nil {
		return fmt.Errorf("run path escapes the runs directory via symlink")
	}
	return nil
}

func canonicalIDFromResolved(root, resolvedDir string) (string, error) {
	if err := pathInside(root, resolvedDir); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, filepath.Clean(resolvedDir))
	if err != nil {
		return "", err
	}
	canon := rel
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		canon = rel[:i]
	}
	if err := validateRunID(canon); err != nil {
		return "", fmt.Errorf("resolved run id: %w", err)
	}
	return canon, nil
}

func confinedRunsRoot() (string, error) {
	return confinedRoot(RunsDir())
}

// confinedRootFor is the confinement root for one run id: the archive for an
// archived run (its runs/<id> entry is at most a pointer), else RunsDir.
func confinedRootFor(runID string) (string, error) {
	if _, ok := archivedRunDir(runID); ok {
		return confinedRoot(ArchiveDir())
	}
	return confinedRunsRoot()
}

func confinedRoot(dir string) (string, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve runs directory: %w", err)
	}
	root = filepath.Clean(root)
	if real, err := filepath.EvalSymlinks(root); err == nil {
		return real, nil
	}
	return root, nil
}

func pathInside(root, p string) error {
	rel, err := filepath.Rel(root, filepath.Clean(p))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q is outside the runs directory", p)
	}
	return nil
}

func rejectEscapeIfExists(root, p string) error {
	info, err := os.Lstat(p)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("run path %q is an unresolvable symlink", p)
		}
		return err
	}
	if err := pathInside(root, resolved); err != nil {
		return fmt.Errorf("run path escapes the runs directory via symlink")
	}
	return nil
}
