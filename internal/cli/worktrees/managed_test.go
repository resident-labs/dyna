package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/worktrees"
)

func TestManagedNewAndRepositoryList(t *testing.T) {
	storetest.Isolate(t)
	serveWorktreeEngine(t)
	repo := commandRepo(t)
	remote := t.TempDir()
	commandGit(t, remote, "init", "--bare")
	commandGit(t, repo, "remote", "add", "origin", remote)
	commandGit(t, repo, "push", "origin", "HEAD:refs/heads/main")
	root := filepath.Join(t.TempDir(), "managed trees")
	v, err := settings.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set(settings.KeyWorktreesRoot, root); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := NewCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"new", "feature/cli", "--repo", repo})
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(out.String())
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		t.Fatalf("new path %q is outside root %q", path, root)
	}
	if branch := commandGit(t, path, "branch", "--show-current"); branch != "feature/cli" {
		t.Fatalf("branch = %q", branch)
	}
	cmd = NewCommand()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--repo", repo, "--json"})
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	var trees []worktrees.Tree
	if err := json.Unmarshal(out.Bytes(), &trees); err != nil {
		t.Fatal(err)
	}
	if len(trees) != 2 {
		t.Fatalf("repository list = %#v", trees)
	}
	found := false
	for _, tree := range trees {
		found = found || tree.Path == path && tree.Branch == "feature/cli"
	}
	if !found {
		t.Fatalf("new worktree is missing: %#v", trees)
	}
	owner := "ses_20261017-000001-aaaaaaaa"
	record, err := worktrees.Get(path)
	if err != nil {
		t.Fatal(err)
	}
	record.Session = owner
	if err := worktrees.Track(record); err != nil {
		t.Fatal(err)
	}
	recordtest.Write(t, subagent.DefaultStore().Root, owner, subagent.Meta{ID: owner, Closed: time.Now()})
	for _, dryRun := range []bool{true, false} {
		cmd = NewCommand()
		out.Reset()
		cmd.SetOut(&out)
		args := []string{"gc", "--repo", repo, "--json"}
		want := "removed"
		if dryRun {
			args = append(args, "--dry-run")
			want = "would remove"
		}
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(ctx); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(out.Bytes(), &trees); err != nil {
			t.Fatal(err)
		}
		found = false
		for _, tree := range trees {
			found = found || tree.Path == path && tree.Action == want
		}
		if !found {
			t.Fatalf("GC dry-run=%t: %#v", dryRun, trees)
		}
		_, statErr := os.Stat(path)
		if dryRun && statErr != nil || !dryRun && !os.IsNotExist(statErr) {
			t.Fatalf("GC dry-run=%t: stat = %v", dryRun, statErr)
		}
	}
}

func TestOwnerClosedUsesStoredClosedTime(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := "ses_20261017-000001-aaaaaaaa"
	for _, closed := range []bool{false, true} {
		meta := subagent.Meta{ID: id, RoomID: "room-id", Room: "room"}
		if closed {
			meta.Closed = time.Now()
		}
		recordtest.Write(t, subagent.DefaultStore().Root, id, meta)
		if got, err := worktrees.SessionClosed(id); err != nil || got != closed {
			t.Fatalf("closed = %t, %v; want %t", got, err, closed)
		}
	}
	if closed, err := worktrees.SessionClosed("ses_20261017-000002-bbbbbbbb"); err == nil || closed {
		t.Fatalf("unknown owner = %t, %v", closed, err)
	}
}

func TestWorktreeAlias(t *testing.T) {
	cmd := NewCommand()
	if !cmd.HasAlias("worktree") {
		t.Fatalf("aliases = %v", cmd.Aliases)
	}
}
