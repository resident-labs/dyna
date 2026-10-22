package ghclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

func TestRoomPolicyDefaultsAndOverride(t *testing.T) {
	records := setupRooms(t, "resident-labs/project", "acme/cloudportal")
	if got := Access(context.Background(), records[0]); got != ReadWrite {
		t.Fatalf("personal repo policy = %s", got)
	}
	if got := Access(context.Background(), records[1]); got != ReadOnly {
		t.Fatalf("work repo policy = %s", got)
	}
	exe := filepath.Join(t.TempDir(), "gh")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("GH_CALLS", log)
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho called >> \"$GH_CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cmd := Command(ctx, exe, "", "pr", "merge", "https://github.com/acme/cloudportal/pull/1")
	if err := Run(ctx, cmd); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("write error = %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("read-only command reached gh")
	}
	if err := room.DefaultStore().UpdatePreferences(records[1].ID, func(r *room.Record) error { r.GitHubAccess = ReadWrite; return nil }); err != nil {
		t.Fatal(err)
	}
	cmd = Command(ctx, exe, "", "pr", "merge", "https://github.com/acme/cloudportal/pull/1")
	if err := Run(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(log); err != nil || !strings.Contains(string(raw), "called") {
		t.Fatal("explicit write access did not run gh")
	}
}

func TestUnscopedRepositoryNeverReachesGitHub(t *testing.T) {
	setupRooms(t, "acme/cloudportal")
	exe := filepath.Join(t.TempDir(), "gh")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("GH_CALLS", log)
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho called >> \"$GH_CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := Command(context.Background(), exe, "", "pr", "list", "--repo", "acme/unrelated-private")
	if err := Run(context.Background(), cmd); err == nil || !strings.Contains(err.Error(), "requires a Dyna room") {
		t.Fatalf("scope error = %v", err)
	}
	if _, err := AccountFor(context.Background(), exe, "github.com", "acme/unrelated-private"); err == nil {
		t.Fatal("selected an account outside rooms")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("unscoped request reached gh")
	}
}

func TestReadCommands(t *testing.T) {
	for _, args := range [][]string{
		{"--repo=work/guard", "pr", "list"}, {"-R", "work/guard", "pr", "view", "1"},
		{"repo", "view"}, {"pr", "list"}, {"pr", "view", "1"}, {"pr", "diff", "1"}, {"pr", "checks", "1"},
		{"issue", "view", "1"}, {"run", "download", "1"},
		{"api", "repos/o/r/pulls"}, {"api", "repos/o/r/pulls", "-X", "GET", "-f", "per_page=100"},
		{"api", "graphql", "-f", "query=query { viewer { login } }"},
	} {
		if !isRead(args) {
			t.Fatalf("read rejected: %q", args)
		}
	}
	for _, args := range [][]string{
		{"pr", "create"}, {"pr", "merge", "1"}, {"pr", "comment", "1"}, {"pr", "edit", "1"}, {"pr", "close", "1"},
		{"issue", "edit", "1"}, {"repo", "delete"}, {"api", "repos/o/r/issues", "-X", "POST"},
		{"api", "repos/o/r", "--method=DELETE"}, {"api", "repos/o/r", "-XPATCH"},
		{"api", "repos/o/r/issues", "--raw-field=title=test"}, {"api", "repos/o/r/issues", "-ftitle=test"},
		{"api", "repos/o/r", "--input", "body.json"},
		{"api", "graphql", "-f", "query=mutation { addComment(input:{}) { clientMutationId } }"},
	} {
		if isRead(args) {
			t.Fatalf("write allowed: %q", args)
		}
	}
}

func TestActingRoomKeepsClonePoliciesSeparate(t *testing.T) {
	records := setupRooms(t, "work/shared", "work/shared")
	for i, access := range []string{ReadWrite, ReadOnly} {
		if err := SetAccess(room.DefaultStore(), records[i].ID, access); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"pr", "merge", "https://github.com/work/shared/pull/1"}
	r := repository{"github.com", "work/shared"}
	ctx := WithRoom(context.Background(), records[1].ID)
	if err := checkPolicy(ctx, records[0].Repo, r, args); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("read-only room inherited another clone's policy: %v", err)
	}
	ctx = WithRoom(context.Background(), records[0].ID)
	if err := checkPolicy(ctx, records[1].Repo, r, args); err != nil {
		t.Fatalf("write room inherited another clone's policy: %v", err)
	}
	if err := checkPolicy(context.Background(), records[1].Repo, r, args); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("checkout policy lost: %v", err)
	}
	if _, err := roomFor(context.Background(), "", r); err == nil {
		t.Fatal("ambiguous repository accepted without a room or checkout")
	}
	if _, err := roomFor(WithRoom(context.Background(), records[0].ID), "", repository{"github.com", "other/repo"}); err == nil {
		t.Fatal("acting room accepted another repository")
	}
}

func TestMergeStrategiesRejectCrossRoomTargets(t *testing.T) {
	records := setupRooms(t, "resident-labs/project", "other/project")
	ctx := WithRoom(context.Background(), records[0].ID)
	for _, strategy := range []string{"-s", "-m", "-r"} {
		for _, target := range [][]string{{"https://github.com/other/project/pull/4"}, {"--repo", "other/project", "4"}} {
			args := append([]string{"pr", "merge", strategy}, target...)
			// This executable must never run: policy rejects the actual target first.
			cmd := Command(ctx, "/missing/gh", records[0].Repo, args...)
			if err := Run(ctx, cmd); err == nil || !strings.Contains(err.Error(), "does not belong to the acting room") {
				t.Fatalf("%q: cross-room target was not rejected: %v", args, err)
			}
		}
	}
}

func TestReviewCommentsRejectCrossRoomTargets(t *testing.T) {
	records := setupRooms(t, "resident-labs/project", "other/project")
	ctx := WithRoom(context.Background(), records[0].ID)
	for name, args := range map[string][]string{
		"comment":  {"pr", "review", "--comment", "https://github.com/other/project/pull/4"},
		"body URL": {"pr", "review", "-b", "https://github.com/resident-labs/project", "--comment", "https://github.com/other/project/pull/4"},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := Command(ctx, "/missing/gh", records[0].Repo, args...)
			if err := Run(ctx, cmd); err == nil || !strings.Contains(err.Error(), "does not belong to the acting room") {
				t.Fatalf("cross-room review was not rejected: %v", err)
			}
		})
	}
}

// An ended context cannot prove a repository mismatch; report the context error.
func TestRoomForReportsEndedContext(t *testing.T) {
	records := setupRooms(t, "resident-labs/project")
	ctx, cancel := context.WithCancel(WithRoom(context.Background(), records[0].ID))
	cancel()
	_, err := roomFor(ctx, "", repository{"github.com", "resident-labs/project"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ended context = %v", err)
	}
}
