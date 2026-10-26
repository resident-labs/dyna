package nodedeps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// The tests build real git repositories and use a fake npm on PATH. They never
// touch the network.

func TestPrepareRunsNPMCIWithoutDonor(t *testing.T) {
	repo, log := newRepo(t)
	pkg := filepath.Join(repo, "desktop", "frontend")
	summary, err := Prepare(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "desktop/frontend: npm ci in") {
		t.Fatalf("summary = %q", summary)
	}
	if oneLine(summary) != summary {
		t.Fatalf("summary spans lines: %q", summary)
	}
	mustExist(t, filepath.Join(pkg, "node_modules", ".installed-by-fake-npm"))
	stamp := readStamp(t, filepath.Join(pkg, "node_modules"))
	if want := lockHash(t, filepath.Join(pkg, "package-lock.json")); stamp != want {
		t.Fatalf("stamp = %q, want %q", stamp, want)
	}
	if got := npmRuns(t, log); len(got) != 1 || got[0] != pkg {
		t.Fatalf("npm runs = %q", got)
	}
	if out := git(t, repo, "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Fatalf("git status is dirty after Prepare: %s", out)
	}
}

func TestPrepareCopiesFromStampedDonor(t *testing.T) {
	repo, log := newRepo(t)
	for _, donor := range []string{"main checkout", "linked worktree"} {
		t.Run(donor, func(t *testing.T) {
			resetLog(t, log)
			donorRoot := repo
			if donor == "linked worktree" {
				donorRoot = filepath.Join(t.TempDir(), "donor")
				git(t, repo, "worktree", "add", "--detach", donorRoot, "HEAD")
			}
			t.Cleanup(func() {
				_ = os.RemoveAll(filepath.Join(repo, "desktop", "frontend", "node_modules"))
			})
			installFakeTree(t, filepath.Join(donorRoot, "desktop", "frontend"), lockHash(t, filepath.Join(repo, "desktop", "frontend", "package-lock.json")), true)
			target := filepath.Join(t.TempDir(), "target")
			git(t, repo, "worktree", "add", "--detach", target, "HEAD")

			summary, err := Prepare(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(summary, "copied node_modules from "+filepath.Clean(donorRoot)) {
				t.Fatalf("summary = %q", summary)
			}
			nodeModules := filepath.Join(target, "desktop", "frontend", "node_modules")
			if b := readFile(t, filepath.Join(nodeModules, ".donor-file")); b != "donor tree\n" {
				t.Fatalf("copied content = %q", b)
			}
			if got := readStamp(t, nodeModules); got != lockHash(t, filepath.Join(target, "desktop", "frontend", "package-lock.json")) {
				t.Fatalf("stamp = %q", got)
			}
			if got := npmRuns(t, log); len(got) != 0 {
				t.Fatalf("npm ran despite donor: %q", got)
			}
		})
	}
}

func TestPrepareCopiesFromStamplessDonor(t *testing.T) {
	repo, log := newRepo(t)
	donorRoot := filepath.Join(t.TempDir(), "donor")
	git(t, repo, "worktree", "add", "--detach", donorRoot, "HEAD")
	// No stamp: the hidden npm lock file and an identical package-lock.json
	// are the fallback evidence.
	installFakeTree(t, filepath.Join(donorRoot, "desktop", "frontend"), "", false)
	target := filepath.Join(t.TempDir(), "target")
	git(t, repo, "worktree", "add", "--detach", target, "HEAD")

	summary, err := Prepare(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "copied node_modules from "+filepath.Clean(donorRoot)) {
		t.Fatalf("summary = %q", summary)
	}
	nodeModules := filepath.Join(target, "desktop", "frontend", "node_modules")
	mustExist(t, filepath.Join(nodeModules, ".donor-file"))
	if got := readStamp(t, nodeModules); got != lockHash(t, filepath.Join(target, "desktop", "frontend", "package-lock.json")) {
		t.Fatalf("stamp = %q", got)
	}
	if got := npmRuns(t, log); len(got) != 0 {
		t.Fatalf("npm ran despite donor: %q", got)
	}
}

func TestPrepareRejectsStaleDonors(t *testing.T) {
	repo, log := newRepo(t)

	for _, stale := range []string{"mismatched stamp", "different lockfile"} {
		t.Run(stale, func(t *testing.T) {
			resetLog(t, log)
			nodeModules := filepath.Join(repo, "desktop", "frontend", "node_modules")
			t.Cleanup(func() {
				_ = os.RemoveAll(nodeModules)
				git(t, repo, "checkout", "--", "desktop/frontend/package-lock.json")
			})
			if stale == "mismatched stamp" {
				installFakeTree(t, filepath.Join(repo, "desktop", "frontend"), strings.Repeat("0", 64), true)
			} else {
				installFakeTree(t, filepath.Join(repo, "desktop", "frontend"), "", false)
				writeFile(t, filepath.Join(repo, "desktop", "frontend", "package-lock.json"), `{"lockfileVersion": 3, "changed": true}`)
			}
			target := filepath.Join(t.TempDir(), "target")
			git(t, repo, "worktree", "add", "--detach", target, "HEAD")

			summary, err := Prepare(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(summary, "desktop/frontend: npm ci in") {
				t.Fatalf("summary = %q", summary)
			}
			if got := readStamp(t, filepath.Join(target, "desktop", "frontend", "node_modules")); got != lockHash(t, filepath.Join(target, "desktop", "frontend", "package-lock.json")) {
				t.Fatalf("stamp = %q", got)
			}
			if got := npmRuns(t, log); len(got) != 1 {
				t.Fatalf("npm runs = %q", got)
			}
		})
	}
}

func TestPrepareKeepsExistingNodeModules(t *testing.T) {
	repo, log := newRepo(t)
	pkg := filepath.Join(repo, "desktop", "frontend")
	nodeModules := filepath.Join(pkg, "node_modules")

	installFakeTree(t, pkg, lockHash(t, filepath.Join(pkg, "package-lock.json")), true)
	summary, err := Prepare(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "node_modules is up to date") {
		t.Fatalf("summary = %q", summary)
	}

	writeFile(t, filepath.Join(nodeModules, ".mine"), "mine\n")
	if err := os.Remove(filepath.Join(nodeModules, stampName)); err != nil {
		t.Fatal(err)
	}
	summary, err = Prepare(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "left unchanged") {
		t.Fatalf("summary = %q", summary)
	}
	if readFile(t, filepath.Join(nodeModules, ".mine")) != "mine\n" {
		t.Fatal("existing node_modules was touched")
	}
	if _, err := os.Stat(filepath.Join(nodeModules, stampName)); err == nil {
		t.Fatal("Prepare rewrote the stamp for an unknown tree")
	}
	if got := npmRuns(t, log); len(got) != 0 {
		t.Fatalf("npm ran on existing node_modules: %q", got)
	}
}

func TestPrepareSkipsPackagesGitWouldReport(t *testing.T) {
	repo, log := newRepo(t)
	writeFile(t, filepath.Join(repo, ".gitignore"), "desktop/frontend/node_modules/\n")
	// image/agents has no gitignore entry, so node_modules there would show
	// up in git status. Prepare must not create it.
	writeFile(t, filepath.Join(repo, "image", "agents", "package-lock.json"), `{"lockfileVersion": 3}`)
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "packages")

	summary, err := Prepare(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	mustExist(t, filepath.Join(repo, "desktop", "frontend", "node_modules", ".installed-by-fake-npm"))
	if _, err := os.Stat(filepath.Join(repo, "image", "agents", "node_modules")); err == nil {
		t.Fatal("installed node_modules where git would report it")
	}
	if !strings.Contains(summary, "image/agents: skipped because node_modules is not git-ignored") {
		t.Fatalf("summary = %q", summary)
	}
	if got := npmRuns(t, log); len(got) != 1 {
		t.Fatalf("npm runs = %q", got)
	}
	if out := git(t, repo, "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Fatalf("git status is dirty after Prepare: %s", out)
	}
}

func TestPrepareFindsPackagesUpToThreeLevels(t *testing.T) {
	repo, log := newRepo(t)
	for _, rel := range []string{"relay", "a/b/c", "x/y/z/w"} {
		writeFile(t, filepath.Join(repo, filepath.FromSlash(rel), "package-lock.json"), `{"lockfileVersion": 3}`)
	}
	writeFile(t, filepath.Join(repo, "package-lock.json"), `{"lockfileVersion": 3}`)
	// A lock file inside node_modules never counts as a package.
	writeFile(t, filepath.Join(repo, "a", "node_modules", "dep", "package-lock.json"), `{"lockfileVersion": 3}`)
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "packages")

	summary, err := Prepare(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{repo, filepath.Join(repo, "a", "b", "c"), filepath.Join(repo, "desktop", "frontend"), filepath.Join(repo, "relay")}
	got := npmRuns(t, log)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("npm runs = %q, want %q", got, want)
	}
	for _, note := range []string{".: npm ci in", "relay: npm ci in", "a/b/c: npm ci in", "desktop/frontend: npm ci in"} {
		if !strings.Contains(summary, note) {
			t.Fatalf("summary misses %q: %q", note, summary)
		}
	}
	if strings.Contains(summary, "x/y/z/w") || strings.Contains(summary, "a/node_modules") {
		t.Fatalf("summary covers packages out of scope: %q", summary)
	}
}

func TestPrepareNPMFailureLeavesNoTrace(t *testing.T) {
	repo, log := newRepo(t)
	t.Setenv("FAKE_NPM_FAIL", "1")

	summary, err := Prepare(context.Background(), repo)
	if err == nil {
		t.Fatal("npm ci failure was ignored")
	}
	if !strings.Contains(summary, "npm ci failed") {
		t.Fatalf("summary = %q", summary)
	}
	pkg := filepath.Join(repo, "desktop", "frontend")
	if _, err := os.Stat(filepath.Join(pkg, "node_modules")); err == nil {
		t.Fatal("failed install left node_modules behind")
	}
	assertNoStagingLeft(t, pkg)
	if got := npmRuns(t, log); len(got) != 1 {
		t.Fatalf("npm runs = %q", got)
	}
}

func TestPrepareNPMTimeout(t *testing.T) {
	repo, log := newRepo(t)
	t.Setenv("FAKE_NPM_SLEEP", "5")
	old := npmTimeout
	npmTimeout = 200 * time.Millisecond
	defer func() { npmTimeout = old }()

	summary, err := Prepare(context.Background(), repo)
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(summary, "npm ci failed") {
		t.Fatalf("summary = %q", summary)
	}
	pkg := filepath.Join(repo, "desktop", "frontend")
	if _, err := os.Stat(filepath.Join(pkg, "node_modules", stampName)); err == nil {
		t.Fatal("timed out install wrote a stamp")
	}
	assertNoStagingLeft(t, pkg)
	if got := npmRuns(t, log); len(got) != 1 {
		t.Fatalf("npm runs = %q", got)
	}
}

func TestPrepareWithoutPackagesDoesNothing(t *testing.T) {
	repo, _ := newRepo(t)
	if err := os.Remove(filepath.Join(repo, "desktop", "frontend", "package-lock.json")); err != nil {
		t.Fatal(err)
	}
	summary, err := Prepare(context.Background(), repo)
	if err != nil || summary != "" {
		t.Fatalf("summary = %q, err = %v", summary, err)
	}
}

// newRepo builds a git repository with one package in desktop/frontend and a
// fake npm on PATH. It returns the repository and the fake npm run log.
func newRepo(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Dyna Test")
	git(t, repo, "config", "user.email", "dyna-test@example.com")
	// No background gc or maintenance may prune worktrees under a test (D-188).
	git(t, repo, "config", "gc.auto", "0")
	git(t, repo, "config", "maintenance.auto", "false")
	writeFile(t, filepath.Join(repo, ".gitignore"), "node_modules/\n")
	writeFile(t, filepath.Join(repo, "desktop", "frontend", "package-lock.json"), `{"lockfileVersion": 3}`)
	writeFile(t, filepath.Join(repo, "desktop", "frontend", "package.json"), `{"name": "frontend"}`)
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "packages")
	return repo, fakeNPM(t)
}

// fakeNPM puts a fake npm first on PATH. It appends its working directory to
// FAKE_NPM_LOG for every npm ci run.
func fakeNPM(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "npm.log")
	script := `#!/bin/sh
if [ "$1" != "ci" ]; then
	echo "fake npm: unexpected args: $*" >&2
	exit 2
fi
echo "$PWD" >> "$FAKE_NPM_LOG"
if [ -n "$FAKE_NPM_FAIL" ]; then
	echo "fake npm failure" >&2
	exit 1
fi
if [ -n "$FAKE_NPM_SLEEP" ]; then
	sleep "$FAKE_NPM_SLEEP"
fi
mkdir -p node_modules
echo '{"lockfileVersion": 3}' > node_modules/.package-lock.json
echo installed > node_modules/.installed-by-fake-npm
`
	writeFile(t, filepath.Join(bin, "npm"), script)
	if err := os.Chmod(filepath.Join(bin, "npm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_NPM_LOG", log)
	t.Setenv("FAKE_NPM_FAIL", "")
	t.Setenv("FAKE_NPM_SLEEP", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// installFakeTree writes a node_modules that looks like an npm install. A
// non-empty stamp adds the stamp file.
func installFakeTree(t *testing.T, pkg, stamp string, stampedTree bool) {
	t.Helper()
	nodeModules := filepath.Join(pkg, "node_modules")
	writeFile(t, filepath.Join(nodeModules, ".donor-file"), "donor tree\n")
	writeFile(t, filepath.Join(nodeModules, hiddenLock), `{"lockfileVersion": 3}`)
	if stampedTree {
		writeFile(t, filepath.Join(nodeModules, stampName), stamp+"\n")
	}
}

// resetLog empties the fake npm run log so a subtest counts only its own runs.
func resetLog(t *testing.T, log string) {
	t.Helper()
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func npmRuns(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(b)), "\n", " "))
}

func readStamp(t *testing.T, nodeModules string) string {
	t.Helper()
	return strings.TrimSpace(readFile(t, filepath.Join(nodeModules, stampName)))
}

func lockHash(t *testing.T, path string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(readFile(t, path)))
	return hex.EncodeToString(sum[:])
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func assertNoStagingLeft(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".node_modules-tmp-") {
			t.Fatalf("staging directory left behind: %s", e.Name())
		}
	}
}

func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// An unstamped donor with the same package-lock.json can still miss a
// package, for example after a merge without npm ci. The landing check then
// failed on a missing @novnc/novnc. Such a donor must not be copied.
func TestPrepareRejectsStamplessDonorMissingAPackage(t *testing.T) {
	repo, log := newRepo(t)
	donorRoot := filepath.Join(t.TempDir(), "donor")
	git(t, repo, "worktree", "add", "--detach", donorRoot, "HEAD")
	donorPkg := filepath.Join(donorRoot, "desktop", "frontend")
	installFakeTree(t, donorPkg, "", false)
	writeFile(t, filepath.Join(donorPkg, "package.json"), `{"name": "frontend", "dependencies": {"@novnc/novnc": "^1.7.0", "left-pad": "1.0.0"}}`)
	writeFile(t, filepath.Join(donorPkg, "node_modules", "left-pad", "package.json"), `{"name": "left-pad"}`)
	target := filepath.Join(t.TempDir(), "target")
	git(t, repo, "worktree", "add", "--detach", target, "HEAD")

	summary, err := Prepare(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "desktop/frontend: npm ci in") {
		t.Fatalf("copied a donor missing @novnc/novnc: %q", summary)
	}
	if got := npmRuns(t, log); len(got) != 1 {
		t.Fatalf("npm runs = %q", got)
	}
}

// A stamp can be written over a tree that was copied from a stale donor, as
// the #344 donor was. A stamped donor missing a declared package must not be
// copied either.
func TestPrepareRejectsStampedDonorMissingAPackage(t *testing.T) {
	repo, log := newRepo(t)
	donorRoot := filepath.Join(t.TempDir(), "donor")
	git(t, repo, "worktree", "add", "--detach", donorRoot, "HEAD")
	donorPkg := filepath.Join(donorRoot, "desktop", "frontend")
	installFakeTree(t, donorPkg, lockHash(t, filepath.Join(donorPkg, "package-lock.json")), true)
	writeFile(t, filepath.Join(donorPkg, "package.json"), `{"name": "frontend", "dependencies": {"@novnc/novnc": "^1.7.0"}}`)
	target := filepath.Join(t.TempDir(), "target")
	git(t, repo, "worktree", "add", "--detach", target, "HEAD")

	summary, err := Prepare(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "desktop/frontend: npm ci in") {
		t.Fatalf("copied a stamped donor missing @novnc/novnc: %q", summary)
	}
	if got := npmRuns(t, log); len(got) != 1 {
		t.Fatalf("npm runs = %q", got)
	}
}

func TestPrepareSelectedProjectReusesDonor(t *testing.T) {
	repo, log := newRepo(t)
	pkg := filepath.Join(repo, "desktop", "frontend")
	installFakeTree(t, pkg, lockHash(t, filepath.Join(pkg, "package-lock.json")), true)
	target := filepath.Join(t.TempDir(), "target")
	git(t, repo, "worktree", "add", "--detach", target, "HEAD")
	selected := filepath.Join(target, "desktop", "frontend")
	summary, err := Prepare(context.Background(), selected)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "copied node_modules") || len(npmRuns(t, log)) != 0 {
		t.Fatal("selected project did not reuse its matching donor", summary)
	}
	mustExist(t, filepath.Join(selected, "node_modules", ".donor-file"))
}
