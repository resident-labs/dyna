package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
			base := "main"
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

func TestArchiveRepo(t *testing.T) {
	for _, mode := range []string{"skip", "snapshot", "error"} {
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
					if mode == "skip" {
						return "head\trefs/heads/v2/s", nil
					}
					return "other\trefs/heads/v2/s", nil
				case "read-tree":
					index = idx
				case "add", "write-tree", "commit-tree":
					if idx == "" || idx != index {
						t.Fatal("real index used")
					}
				}
				switch args[0] {
				case "write-tree":
					return "tree", nil
				case "commit-tree":
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
			case "snapshot":
				if got.Commit != "snapshot" || got.Error != "" || index == "" {
					t.Fatal(got)
				}
				want := [][]string{{"read-tree", "HEAD"}, {"add", "-A"}, {"write-tree"}, {"commit-tree", "tree", "-p", "head", "-m", "archive s"}, {"push", "origin", "snapshot:refs/dyna/archive/s"}}
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
