package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestSetupRepos(t *testing.T) {
	for _, existingBranch := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existingBranch], func(t *testing.T) {
			root, stateRoot := t.TempDir(), t.TempDir()
			repo := contract.BoxRepo{Name: "owner/repo", Cache: "/cache/repo", Branch: "v2/s", Base: "main"}
			var calls [][]string
			git := func(_ context.Context, _, _ string, args ...string) (string, error) {
				calls = append(calls, append([]string(nil), args...))
				switch args[0] {
				case "clone":
					return "", os.MkdirAll(args[len(args)-1], 0755)
				case "show-ref":
					if !existingBranch {
						return "", errors.New("not found")
					}
				}
				return "", nil
			}
			if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", git); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "owner/repo")
			want := []string{"clone", "--no-checkout", "--reference", "/cache/repo", "/cache/repo", dir}
			if !reflect.DeepEqual(calls[0], want) {
				t.Fatal(calls[0])
			}
			base := "refs/remotes/origin/main"
			if existingBranch {
				base = "refs/remotes/origin/v2/s"
			}
			if want := []string{"checkout", "-b", "v2/s", base}; !reflect.DeepEqual(calls[len(calls)-1], want) {
				t.Fatal(calls)
			}
			calls = nil
			if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", git); err != nil || len(calls) != 0 {
				t.Fatalf("skip: %v %v", calls, err)
			}
		})
	}
}

func TestSetupRetryAfterCrashStartsFromEmptyDirectory(t *testing.T) {
	root, stateRoot := t.TempDir(), t.TempDir()
	repo := contract.BoxRepo{Name: "owner/repo", Cache: "/cache", Branch: "branch", Base: "main"}
	dir := filepath.Join(root, string(repo.Name))
	state := filepath.Join(stateRoot, string(repo.Name))
	crash := true
	git := func(_ context.Context, _, _ string, args ...string) (string, error) {
		switch args[0] {
		case "clone":
			target := args[len(args)-1]
			if err := os.MkdirAll(filepath.Join(target, ".git"), 0755); err != nil {
				return "", err
			}
			if !crash {
				return "", nil
			}
			if err := os.WriteFile(filepath.Join(target, ".git", "index.lock"), nil, 0600); err != nil {
				return "", err
			}
			return "", os.WriteFile(filepath.Join(target, "partial.txt"), []byte("half"), 0600)
		case "checkout":
			if crash {
				return "", errors.New("crash before checkout")
			}
		}
		return "", nil
	}
	if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", git); err == nil {
		t.Fatal("expected crash")
	}
	if got, err := os.ReadFile(state); err != nil || string(got) != setupStarted {
		t.Fatalf("state after crash: %q %v", got, err)
	}
	crash = false
	if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", git); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(state); err != nil || string(got) != setupDone {
		t.Fatalf("state after retry: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "partial.txt")); !os.IsNotExist(err) {
		t.Fatalf("partial file kept: %v", err)
	}
}

func TestSetupDoneCheckoutKeepsFilesWithoutIndex(t *testing.T) {
	root, stateRoot := t.TempDir(), t.TempDir()
	repo := contract.BoxRepo{Name: "owner/repo", Cache: "/cache", Branch: "branch", Base: "main"}
	dir := filepath.Join(root, string(repo.Name))
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("pi work"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeSetupState(filepath.Join(stateRoot, string(repo.Name)), setupDone); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	git := func(_ context.Context, _, _ string, args ...string) (string, error) {
		calls = append(calls, args)
		return "", nil
	}
	if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", git); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatal(calls)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "notes.txt")); err != nil || string(got) != "pi work" {
		t.Fatalf("pi work lost: %q %v", got, err)
	}
}

func TestSetupStateNamesDoNotCollide(t *testing.T) {
	root, stateRoot := t.TempDir(), t.TempDir()
	repos := []contract.BoxRepo{
		{Name: "owner/repo.tmp", Cache: "/cache", Branch: "branch", Base: "main"},
		{Name: "owner/repo", Cache: "/cache", Branch: "branch", Base: "main"},
	}
	git := func(_ context.Context, _, _ string, args ...string) (string, error) {
		if args[0] == "clone" {
			return "", os.MkdirAll(args[len(args)-1], 0755)
		}
		return "", nil
	}
	if err := setupRepos(context.Background(), root, stateRoot, repos, "", "", git); err != nil {
		t.Fatal(err)
	}
	for _, repo := range repos {
		if got, err := os.ReadFile(filepath.Join(stateRoot, string(repo.Name))); err != nil || string(got) != setupDone {
			t.Fatalf("%s state: %q %v", repo.Name, got, err)
		}
	}
}

func TestSetupRetryFillsRunningProcessDirectory(t *testing.T) {
	root, stateRoot := t.TempDir(), t.TempDir()
	repo := contract.BoxRepo{Name: "owner/repo", Cache: "/cache", Branch: "branch", Base: "main"}
	dir := filepath.Join(root, string(repo.Name))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	fail := true
	git := func(_ context.Context, d, _ string, args ...string) (string, error) {
		if args[0] == "clone" && fail {
			return "", errors.New("offline")
		}
		if args[0] == "checkout" {
			return "", os.WriteFile(filepath.Join(d, "AGENTS.md"), []byte("repository"), 0600)
		}
		return "", nil
	}
	if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", git); err == nil {
		t.Fatal("expected setup failure")
	}
	cmd := exec.Command("sh", "-c", "read -r go; cat AGENTS.md")
	cmd.Dir = dir
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	fail = false
	if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", git); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(input, "go\n"); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(output)
	if err != nil || string(got) != "repository" {
		t.Fatalf("got %q: %v", got, err)
	}
}

func TestArchiveRetryAfterAnotherRepoFails(t *testing.T) {
	root := t.TempDir()
	repos := []contract.BoxRepo{{Name: "owner/a", Branch: "v2/s"}, {Name: "owner/b", Branch: "v2/s"}}
	pushed := map[string]bool{}
	fail := true
	git := func(_ context.Context, dir, _ string, args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			return "head", nil
		case "status":
			return "dirty", nil
		case "write-tree":
			return "tree", nil
		case "-c":
			return "snapshot", nil
		case "push":
			if strings.HasSuffix(dir, "/b") && fail {
				return "", errors.New("push failed")
			}
			if pushed[dir] && args[1] != "--force" {
				return "", errors.New("non-fast-forward")
			}
			pushed[dir] = true
		}
		return "", nil
	}
	first := archiveRepo(context.Background(), root, "s", repos[0], git)
	second := archiveRepo(context.Background(), root, "s", repos[1], git)
	if first.Error != "" || second.Error == "" {
		t.Fatal(first, second)
	}
	fail = false
	for _, repo := range repos {
		if got := archiveRepo(context.Background(), root, "s", repo, git); got.Error != "" {
			t.Fatal(got)
		}
	}
}

func TestArchiveRepo(t *testing.T) {
	for _, mode := range []string{"skip", "snapshot", "empty", "error"} {
		t.Run(mode, func(t *testing.T) {
			var calls [][]string
			var index string
			git := func(_ context.Context, _, idx string, args ...string) (string, error) {
				calls = append(calls, append([]string(nil), args...))
				switch args[0] {
				case "rev-parse":
					return "head", nil
				case "status":
					if mode == "error" {
						return "", errors.New("failed")
					}
					return "", nil
				case "ls-remote":
					if mode == "empty" {
						return "", nil
					}
					if mode == "skip" {
						return "head\trefs/heads/v2/s", nil
					}
					return "other\trefs/heads/v2/s", nil
				case "read-tree":
					index = idx
				case "ls-files", "add", "write-tree", "-c":
					if idx == "" || idx != index {
						t.Fatal("real index used")
					}
				}
				switch args[0] {
				case "write-tree":
					return "tree", nil
				case "-c":
					return "snapshot", nil
				}
				return "", nil
			}
			got := archiveRepo(context.Background(), t.TempDir(), "s", contract.BoxRepo{Name: "owner/repo", Branch: "v2/s"}, git)
			switch mode {
			case "skip":
				if !got.Skipped || got.Commit != "head" || len(calls) != 3 {
					t.Fatal(got, calls)
				}
			case "error":
				if !strings.Contains(got.Error, "failed") {
					t.Fatal(got)
				}
			case "snapshot", "empty":
				if got.Commit != "snapshot" || got.Error != "" || index == "" {
					t.Fatal(got)
				}
				want := [][]string{{"read-tree", "HEAD"}, {"ls-files", "--others", "--exclude-standard", "-z"}, {"add", "-A"}, {"write-tree"}, {"-c", "user.name=Dyna Archive", "-c", "user.email=archive@dyna.invalid", "commit-tree", "tree", "-p", "head", "-m", "archive s"}, {"push", "--force", "origin", "snapshot:refs/dyna/archive/s"}}
				if !reflect.DeepEqual(calls[3:], want) {
					t.Fatal(calls)
				}
				if _, err := os.Stat(index); !os.IsNotExist(err) {
					t.Fatalf("index remains: %v", err)
				}
			}
		})
	}
}

func TestArchiveRetriesBeforeReporting(t *testing.T) {
	attempts := 0
	git := func(_ context.Context, _, _ string, args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			attempts++
			if attempts < 3 {
				return "", errors.New("temporary failure")
			}
			return "head", nil
		case "status":
			return "modified", nil
		case "write-tree":
			return "tree", nil
		case "-c":
			return "snapshot", nil
		}
		return "", nil
	}
	var waits []time.Duration
	after := func(d time.Duration) <-chan time.Time {
		waits = append(waits, d)
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	var reports []contract.ArchiveDoneRequest
	err := archiveAndReport(context.Background(), t.TempDir(), "s", []contract.BoxRepo{{Name: "owner/repo"}}, git, archiveLimit, after, func(_ context.Context, req contract.ArchiveDoneRequest) error {
		reports = append(reports, req)
		return nil
	})
	if err != nil || attempts != 3 || len(reports) != 1 || reports[0].Error != "" || reports[0].Commit != "snapshot" {
		t.Fatalf("err=%v attempts=%d reports=%+v", err, attempts, reports)
	}
	if !reflect.DeepEqual(waits, []time.Duration{transcriptRetryMin, 2 * transcriptRetryMin}) {
		t.Fatalf("waits=%v", waits)
	}
}

func TestArchiveThatHangsIsReportedAsFailed(t *testing.T) {
	git := func(ctx context.Context, _, _ string, args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			return "head", nil
		case "status":
			return "modified", nil
		case "write-tree":
			return "tree", nil
		case "-c":
			return "snapshot", nil
		case "push":
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "", nil
	}
	var reports []contract.ArchiveDoneRequest
	err := archiveAndReport(context.Background(), t.TempDir(), "s", []contract.BoxRepo{{Name: "owner/repo"}}, git, time.Millisecond, time.After, func(_ context.Context, req contract.ArchiveDoneRequest) error {
		reports = append(reports, req)
		return nil
	})
	if err != nil || len(reports) != 1 || !strings.Contains(reports[0].Error, "ran past") || reports[0].Commit != "" {
		t.Fatalf("err=%v reports=%+v", err, reports)
	}
}

// archiveTestRepo is a real checkout with one commit and a bare origin that holds its branch.
func archiveTestRepo(t *testing.T) (root, origin string, repo contract.BoxRepo, git func(...string) string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root, origin = t.TempDir(), t.TempDir()
	repo = contract.BoxRepo{Name: "repo", Branch: "v2/s"}
	dir := filepath.Join(root, string(repo.Name))
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	git = func(args ...string) string {
		t.Helper()
		out, err := runGit(context.Background(), dir, "", args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if _, err := runGit(context.Background(), origin, "", "init", "--bare"); err != nil {
		t.Fatal(err)
	}
	git("init", "-b", "v2/s")
	git("remote", "add", "origin", origin)
	for name, text := range map[string]string{"tracked.txt": "first", "kept.bin": "first", ".gitignore": "ignored.bin\n"} {
		writeFile(t, filepath.Join(dir, name), text)
	}
	git("add", "-A")
	git("-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-m", "first")
	git("push", "origin", "v2/s")
	return root, origin, repo, git
}

// writeLarge makes a sparse file of size bytes, so a test costs no disk and any hashing of it would show as time.
func writeLarge(t *testing.T, path string, size int64) {
	t.Helper()
	writeFile(t, path, "")
	if err := os.Truncate(path, size); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveLeavesOutLargeUntrackedFiles(t *testing.T) {
	root, origin, repo, git := archiveTestRepo(t)
	dir := filepath.Join(root, string(repo.Name))
	if err := os.MkdirAll(filepath.Join(dir, ".proof", "rust"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "tracked.txt"), "second")
	writeFile(t, filepath.Join(dir, "small.txt"), "small")
	writeFile(t, filepath.Join(dir, ".proof", "notes [1].txt"), "notes")
	writeLarge(t, filepath.Join(dir, "kept.bin"), archiveFileLimit+1)    // tracked, so kept however large
	writeLarge(t, filepath.Join(dir, "at-limit.bin"), archiveFileLimit)  // not over the limit
	writeLarge(t, filepath.Join(dir, "ignored.bin"), archiveFileLimit+1) // ignored, so no news
	large := []string{".proof/a b [1].bin", ".proof/rust/*.bin"}
	for _, path := range large {
		writeLarge(t, filepath.Join(dir, path), archiveFileLimit+1)
	}

	got := archiveRepo(context.Background(), root, "s", repo, runGit)
	if got.Error != "" || got.Commit == "" {
		t.Fatalf("%+v", got)
	}
	want := &contract.ArchiveLeftOut{Files: 2, Bytes: 2 * (archiveFileLimit + 1), Paths: large}
	if !reflect.DeepEqual(got.LeftOut, want) {
		t.Fatalf("left out %+v, want %+v", got.LeftOut, want)
	}
	if pushed := git("ls-remote", origin, "refs/dyna/archive/s"); !strings.HasPrefix(pushed, got.Commit) {
		t.Fatalf("pushed %q, want %s", pushed, got.Commit)
	}
	files := strings.Split(git("ls-tree", "-r", "--name-only", got.Commit), "\n")
	if want := []string{".gitignore", ".proof/notes [1].txt", "at-limit.bin", "kept.bin", "small.txt", "tracked.txt"}; !reflect.DeepEqual(files, want) {
		t.Fatalf("archived %q, want %q", files, want)
	}
	if text := git("show", got.Commit+":tracked.txt"); text != "second" {
		t.Fatalf("tracked.txt: %q", text)
	}
	if status := git("status", "--porcelain", "--", "tracked.txt"); status != "M tracked.txt" {
		t.Fatalf("the real index changed: %q", status)
	}
}

func TestArchiveCarriesEverythingUnderTheLimit(t *testing.T) {
	root, _, repo, git := archiveTestRepo(t)
	writeFile(t, filepath.Join(root, string(repo.Name), "small.txt"), "small")
	got := archiveRepo(context.Background(), root, "s", repo, runGit)
	if got.Error != "" || got.LeftOut != nil {
		t.Fatalf("%+v", got)
	}
	if text := git("show", got.Commit+":small.txt"); text != "small" {
		t.Fatalf("small.txt: %q", text)
	}
}

func TestSetupGitIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, email string
		want        [][]string
	}{
		{name: "Example User", email: "user@example.invalid", want: [][]string{{"config", "--global", "user.name", "Example User"}, {"config", "--global", "user.email", "user@example.invalid"}}},
		{},
	} {
		var calls [][]string
		git := func(_ context.Context, _, _ string, args ...string) (string, error) {
			calls = append(calls, args)
			return "", nil
		}
		if err := setupRepos(context.Background(), t.TempDir(), t.TempDir(), nil, tc.name, tc.email, git); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(calls, tc.want) {
			t.Fatalf("calls = %v, want %v", calls, tc.want)
		}
	}
}

func TestACrewStartsOnItsFixedCommitUnlessItPushedABranch(t *testing.T) {
	for _, pushed := range []bool{false, true} {
		root, stateRoot := t.TempDir(), t.TempDir()
		repo := contract.BoxRepo{Name: "owner/repo", Cache: "/cache/repo", Branch: "v2/s", Base: "main", Commit: "abc123"}
		var checkout []string
		git := func(_ context.Context, _, _ string, args ...string) (string, error) {
			switch args[0] {
			case "clone":
				return "", os.MkdirAll(args[len(args)-1], 0755)
			case "show-ref":
				if !pushed {
					return "", errors.New("not found")
				}
			case "checkout":
				checkout = args
			}
			return "", nil
		}
		if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", git); err != nil {
			t.Fatal(err)
		}
		want := "abc123"
		if pushed {
			want = "refs/remotes/origin/v2/s"
		}
		if checkout[len(checkout)-1] != want {
			t.Fatalf("pushed %v: checkout %v", pushed, checkout)
		}
	}
}
