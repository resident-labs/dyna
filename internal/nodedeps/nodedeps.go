// Package nodedeps restores node_modules for npm packages in a git worktree.
//
// A fresh worktree has no node_modules, and a full npm ci is slow. Prepare
// finds another worktree of the same repository whose node_modules matches the
// local package-lock.json and reflinks it into place. It runs npm ci only when
// no other worktree has a matching install.
package nodedeps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/fsutil"
)

const (
	// stampName lives inside node_modules and records the package-lock.json
	// hash of the install it belongs to.
	stampName = ".dyna-lock-sha256"
	// hiddenLock is the lock file npm writes inside node_modules after an
	// install. It proves that npm produced the tree.
	hiddenLock = ".package-lock.json"
	// maxDepth is how many directory levels below the worktree root a
	// package-lock.json may live and still count.
	maxDepth = 3
)

// npmTimeout bounds one npm ci run. Tests replace it.
var npmTimeout = 5 * time.Minute

// Executor runs one dependency copy or install command.
type Executor func(context.Context, string, []string, io.Writer) error

// Prepare makes node_modules usable in a worktree or selected project directory.
// It handles every package directory, meaning a directory with a
// package-lock.json at most three levels below worktreeDir, and skips
// node_modules subtrees. For each package directory it copies node_modules
// from another worktree of the same repository whose install matches the local
// package-lock.json hash, or runs npm ci when no such worktree exists. It then
// writes node_modules/.dyna-lock-sha256 with the hash it installed.
//
// Prepare never replaces an existing node_modules, and it never installs where
// git would report the new files, so git status stays clean. It returns one log
// line that describes what it did.
func Prepare(ctx context.Context, worktreeDir string) (string, error) {
	return PrepareWithExecutor(ctx, worktreeDir, execute)
}

// PrepareWithExecutor uses the caller's bounded process runner for copies and installs.
func PrepareWithExecutor(ctx context.Context, worktreeDir string, run Executor) (string, error) {
	root, err := filepath.Abs(worktreeDir)
	if err != nil {
		return "", fmt.Errorf("resolve worktree: %w", err)
	}
	dirs, err := packageDirs(root)
	if err != nil {
		return "", err
	}
	if len(dirs) == 0 {
		return "", nil
	}
	// Without git there are no donor worktrees; npm ci still works.
	roots, _ := worktreeRoots(ctx, root)
	if len(roots) > 0 {
		top, err := gitOutput(ctx, root, "rev-parse", "--show-toplevel")
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(strings.TrimSpace(string(top)), root)
		if err != nil {
			return "", err
		}
		for i := range roots {
			roots[i] = filepath.Join(roots[i], rel)
		}
	}

	var notes []string
	var errs []error
	for _, rel := range dirs {
		note, err := prepareDir(ctx, root, rel, roots, run)
		notes = append(notes, note)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return "node_modules: " + strings.Join(notes, "; "), errors.Join(errs...)
}

func prepareDir(ctx context.Context, root, rel string, roots []string, run Executor) (string, error) {
	dir := filepath.Join(root, filepath.FromSlash(rel))
	hash, err := hashFile(filepath.Join(dir, "package-lock.json"))
	if err != nil {
		return fmt.Sprintf("%s: cannot read package-lock.json", rel), fmt.Errorf("%s: %w", rel, err)
	}
	nodeModules := filepath.Join(dir, "node_modules")
	if info, err := os.Stat(nodeModules); err == nil && info.IsDir() {
		if stamped(nodeModules, hash) {
			return fmt.Sprintf("%s: node_modules is up to date", rel), nil
		}
		return fmt.Sprintf("%s: node_modules already exists, left unchanged", rel), nil
	}
	if !gitWouldIgnore(ctx, dir) {
		return fmt.Sprintf("%s: skipped because node_modules is not git-ignored", rel), nil
	}
	start := time.Now()
	action := "npm ci"
	var copyErr error
	if donor := findDonor(roots, root, rel, hash); donor != "" {
		copyErr = copyNodeModules(ctx, filepath.Join(donor, filepath.FromSlash(rel), "node_modules"), dir, run)
		if copyErr == nil {
			if err := writeStamp(nodeModules, hash); err != nil {
				return fmt.Sprintf("%s: %s", rel, err), fmt.Errorf("%s: %w", rel, err)
			}
			return fmt.Sprintf("%s: copied node_modules from %s in %s", rel, donor, since(start)), nil
		}
		// A donor can vanish mid-copy. npm ci is the safe fallback.
		action = "copy failed, npm ci"
	}
	if err := runNPMCI(ctx, dir, run); err != nil {
		if copyErr != nil {
			err = fmt.Errorf("%v (copy: %s)", err, tail(copyErr.Error()))
		}
		return fmt.Sprintf("%s: %s failed: %v", rel, action, err), fmt.Errorf("%s: %s failed: %w", rel, action, err)
	}
	if err := writeStamp(nodeModules, hash); err != nil {
		return fmt.Sprintf("%s: %s", rel, err), fmt.Errorf("%s: %w", rel, err)
	}
	return fmt.Sprintf("%s: %s in %s", rel, action, since(start)), nil
}

// packageDirs lists package directories up to maxDepth levels below root and
// skips node_modules and .git subtrees.
func packageDirs(root string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel != "." {
			switch d.Name() {
			case "node_modules", ".git":
				return filepath.SkipDir
			}
			if len(strings.Split(filepath.ToSlash(rel), "/")) > maxDepth {
				return filepath.SkipDir
			}
		}
		if _, err := os.Stat(filepath.Join(path, "package-lock.json")); err == nil {
			dirs = append(dirs, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("find package-lock.json files: %w", err)
	}
	sort.Strings(dirs)
	return dirs, nil
}

// findDonor returns the root of the worktree with the best matching
// node_modules for rel, or "" when none matches. A donor with a matching stamp
// is preferred. Without a stamp, a donor counts only when its own
// package-lock.json has the same hash and npm left its hidden lock file
// behind.
func findDonor(roots []string, self, rel, hash string) string {
	self = filepath.Clean(self)
	fallback := ""
	for _, root := range roots {
		if root = filepath.Clean(root); root == self {
			continue
		}
		nodeModules := filepath.Join(root, filepath.FromSlash(rel), "node_modules")
		if !isDir(nodeModules) {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(nodeModules, stampName)); err == nil {
			// A stamp can sit on a tree copied from a stale donor, so check
			// the packages here too.
			if strings.TrimSpace(string(b)) == hash && hasDeclaredDeps(filepath.Join(root, filepath.FromSlash(rel))) {
				return root
			}
			continue
		}
		if fallback != "" || !isFile(filepath.Join(nodeModules, hiddenLock)) {
			continue
		}
		// An unstamped donor's lock file can be newer than what it installed,
		// for example after a merge without npm ci. Use it only when every
		// package in package.json is really present.
		if h, err := hashFile(filepath.Join(root, filepath.FromSlash(rel), "package-lock.json")); err == nil && h == hash && hasDeclaredDeps(filepath.Join(root, filepath.FromSlash(rel))) {
			fallback = root
		}
	}
	return fallback
}

// copyNodeModules copies src into a temporary sibling of the target and
// renames it into place. No reader ever sees a partial tree.
func copyNodeModules(ctx context.Context, src, dir string, run Executor) error {
	tmp, err := os.MkdirTemp(dir, ".node_modules-tmp-")
	if err != nil {
		return fmt.Errorf("stage node_modules: %w", err)
	}
	defer os.RemoveAll(tmp)
	args := []string{"-a"}
	if runtime.GOOS == "linux" {
		args = append(args, "--reflink=auto")
	}
	args = append(args, src, filepath.Join(tmp, "node_modules"))
	var out bytes.Buffer
	if err := run(ctx, dir, append([]string{"cp"}, args...), &out); err != nil {
		return fmt.Errorf("cp -a --reflink=auto: %w: %s", err, tail(out.String()))
	}
	target := filepath.Join(dir, "node_modules")
	if err := os.Rename(filepath.Join(tmp, "node_modules"), target); err != nil {
		// Another process may have finished first. Keep its tree.
		if isDir(target) {
			return nil
		}
		return fmt.Errorf("rename node_modules into place: %w", err)
	}
	return nil
}

// runNPMCI installs exactly package-lock.json, bounded by npmTimeout.
func runNPMCI(ctx context.Context, dir string, run Executor) error {
	runCtx, cancel := context.WithTimeout(ctx, npmTimeout)
	defer cancel()
	var out bytes.Buffer
	if err := run(runCtx, dir, []string{"npm", "ci"}, &out); err != nil {
		if runCtx.Err() != nil {
			return fmt.Errorf("npm ci: %w after %s", runCtx.Err(), npmTimeout)
		}
		return fmt.Errorf("npm ci: %w: %s", err, tail(out.String()))
	}
	return nil
}

func execute(ctx context.Context, dir string, args []string, output io.Writer) error {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = output, output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	return childenv.Clean(cmd).Run()
}

// writeStamp records the package-lock.json hash of the install inside
// node_modules, which is git-ignored.
func writeStamp(nodeModules, hash string) error {
	if err := os.MkdirAll(nodeModules, 0o755); err != nil {
		return fmt.Errorf("create node_modules: %w", err)
	}
	if err := fsutil.WriteFile(filepath.Join(nodeModules, stampName), []byte(hash+"\n"), 0o644, true); err != nil {
		return fmt.Errorf("write node_modules stamp: %w", err)
	}
	return nil
}

func stamped(nodeModules, hash string) bool {
	b, err := os.ReadFile(filepath.Join(nodeModules, stampName))
	return err == nil && strings.TrimSpace(string(b)) == hash
}

// gitWouldIgnore reports whether git would hide dir/node_modules from status.
// When git cannot judge, for example outside a repository, it reports true so
// the install is not blocked.
func gitWouldIgnore(ctx context.Context, dir string) bool {
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "/usr/bin/git", "-C", dir, "check-ignore", "-q", "--", "node_modules/")
	err := childenv.Clean(cmd).Run()
	if err == nil {
		return true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false
	}
	return true
}

// worktreeRoots lists the root of every worktree of the repository that
// contains dir, including the main checkout.
func worktreeRoots(ctx context.Context, dir string) ([]string, error) {
	out, err := gitOutput(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list git worktrees: %v: %s", err, tail(string(out)))
	}
	seen := map[string]bool{}
	var roots []string
	for _, line := range strings.Split(string(out), "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		path = filepath.Clean(path)
		if !seen[path] {
			seen[path] = true
			roots = append(roots, path)
		}
	}
	sort.Strings(roots)
	return roots, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"-C", dir}, args...)...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	return childenv.Clean(cmd).CombinedOutput()
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
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

// hasDeclaredDeps reports whether node_modules in dir holds every
// dependency and devDependency that dir/package.json declares.
func hasDeclaredDeps(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return false
	}
	for _, deps := range []map[string]string{pkg.Dependencies, pkg.DevDependencies} {
		for name := range deps {
			if !isFile(filepath.Join(dir, "node_modules", filepath.FromSlash(name), "package.json")) {
				return false
			}
		}
	}
	return true
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func since(start time.Time) string {
	return time.Since(start).Round(time.Millisecond).String()
}

// tail keeps error text and log lines short.
func tail(s string) string {
	s = strings.TrimSpace(s)
	const max = 300
	if len(s) > max {
		return "..." + s[len(s)-max:]
	}
	return s
}
