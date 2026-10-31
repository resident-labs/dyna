package agent

import (
	"context"
	"errors"
	"os"
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
			root := t.TempDir()
			repo := contract.BoxRepo{Name: "owner/repo", Cache: "/cache/repo", Branch: "v2/s", Base: "main"}
			var calls [][]string
			git := func(_ context.Context, _, _ string, args ...string) (string, error) {
				calls = append(calls, append([]string(nil), args...))
				if args[0] == "clone" {
					return "", os.MkdirAll(args[len(args)-1], 0755)
				}
				if args[0] == "show-ref" && !existingBranch {
					return "", errors.New("not found")
				}
				return "", nil
			}
			if err := setupRepos(context.Background(), root, []contract.BoxRepo{repo}, git); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "owner/repo")
			if !reflect.DeepEqual(calls[0], []string{"clone", "--no-checkout", "--reference", repo.Cache, repo.Cache, filepath.Join(root, "owner/.repo.setup")}) {
				t.Fatal(calls[0])
			}
			base := "refs/remotes/origin/main"
			if existingBranch {
				base = "refs/remotes/origin/v2/s"
			}
			if !reflect.DeepEqual(calls[3], []string{"checkout", "-b", "v2/s", base}) {
				t.Fatal(calls[3])
			}
			if _, err := os.Stat(dir); err != nil {
				t.Fatal(err)
			}
			calls = nil
			if err := setupRepos(context.Background(), root, []contract.BoxRepo{repo}, git); err != nil || len(calls) != 0 {
				t.Fatalf("skip: %v %v", calls, err)
			}
		})
	}
}

func TestSetupRetryAfterCheckoutFailure(t *testing.T) {
	root := t.TempDir()
	repo := contract.BoxRepo{Name: "owner/repo", Cache: "/cache", Branch: "branch", Base: "main"}
	fail := true
	git := func(_ context.Context, _, _ string, args ...string) (string, error) {
		if args[0] == "clone" {
			return "", os.MkdirAll(args[len(args)-1], 0755)
		}
		if args[0] == "checkout" && fail {
			return "", errors.New("checkout failed")
		}
		return "", nil
	}
	if err := setupRepos(context.Background(), root, []contract.BoxRepo{repo}, git); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(filepath.Join(root, "owner/repo")); !os.IsNotExist(err) {
		t.Fatalf("partial repository visible: %v", err)
	}
	fail = false
	if err := setupRepos(context.Background(), root, []contract.BoxRepo{repo}, git); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "owner/repo")); err != nil {
		t.Fatal(err)
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
				case "add", "write-tree", "-c":
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
				want := [][]string{{"read-tree", "HEAD"}, {"add", "-A"}, {"write-tree"}, {"-c", "user.name=Dyna Archive", "-c", "user.email=archive@dyna.invalid", "commit-tree", "tree", "-p", "head", "-m", "archive s"}, {"push", "--force", "origin", "snapshot:refs/dyna/archive/s"}}
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
	err := archiveAndReport(context.Background(), t.TempDir(), "s", []contract.BoxRepo{{Name: "owner/repo"}}, git, after, func(_ context.Context, req contract.ArchiveDoneRequest) error {
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
