package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestSetupRepos(t *testing.T) {
	for _, existingBranch := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existingBranch], func(t *testing.T) {
			root, stateRoot := t.TempDir(), t.TempDir()
			repo := contract.BoxRepo{Name: "owner/repo", Cache: "/cache/repo", Branch: "v2/s", Base: "main"}
			var calls [][]string
			git := func(_ context.Context, _, _ string, args ...string) (string, error) {
				if args[0] == "config" {
					return "", nil // signing setup is TestSetupGitIdentity's
				}
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
			if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", "", git); err != nil {
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
			if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", "", git); err != nil || len(calls) != 0 {
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
	if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", "", git); err == nil {
		t.Fatal("expected crash")
	}
	if got, err := os.ReadFile(state); err != nil || string(got) != setupStarted {
		t.Fatalf("state after crash: %q %v", got, err)
	}
	crash = false
	if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", "", git); err != nil {
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
		if args[0] == "config" {
			return "", nil
		}
		calls = append(calls, args)
		return "", nil
	}
	if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", "", git); err != nil {
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
	if err := setupRepos(context.Background(), root, stateRoot, repos, "", "", "", git); err != nil {
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
	if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", "", git); err == nil {
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
	if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", "", git); err != nil {
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

func TestSetupGitIdentity(t *testing.T) {
	unset := [][]string{{"config", "--global", "--unset-all", "commit.gpgsign"}, {"config", "--global", "--unset-all", "tag.gpgsign"}}
	for _, tc := range []struct {
		name, email, key string
		want             [][]string
	}{
		{name: "Example User", email: "user@example.invalid", want: append([][]string{{"config", "--global", "user.name", "Example User"}, {"config", "--global", "user.email", "user@example.invalid"}}, unset...)},
		{want: unset},
		{name: "Example User", email: "user@example.invalid", key: "ssh-ed25519 AAAA", want: [][]string{
			{"config", "--global", "user.name", "Example User"}, {"config", "--global", "user.email", "user@example.invalid"},
			{"config", "--global", "gpg.format", "ssh"}, {"config", "--global", "gpg.ssh.program", "/opt/dyna/bin/current/dyna"},
			{"config", "--global", "user.signingkey", "key::ssh-ed25519 AAAA"}, {"config", "--global", "commit.gpgsign", "true"}, {"config", "--global", "tag.gpgsign", "true"}}},
	} {
		var calls [][]string
		git := func(_ context.Context, _, _ string, args ...string) (string, error) {
			calls = append(calls, args)
			return "", nil
		}
		if err := setupRepos(context.Background(), t.TempDir(), t.TempDir(), nil, tc.name, tc.email, tc.key, git); err != nil {
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
		if err := setupRepos(context.Background(), root, stateRoot, []contract.BoxRepo{repo}, "", "", "", git); err != nil {
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
