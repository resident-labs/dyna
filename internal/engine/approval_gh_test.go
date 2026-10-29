package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/room"
)

func TestGHExecutorReportsTruncatedOutput(t *testing.T) {
	s := testServices(t, false)
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://github.com/owner/project.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	rm, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "output", Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if err := ghclient.SetAccess(s.rooms.Notify.Rooms, rm.ID, ghclient.ReadWrite); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"writer"}]}}';;
"auth token") echo writer-secret;;
"api --hostname") echo '{"permissions":{"push":true}}';;
"pr create") head -c 1048577 /dev/zero | tr '\\000' x;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ghclient.BinaryEnv, binary)
	payload, err := json.Marshal(approvals.GHPayload{Argv: []string{"pr", "create", "--repo", "owner/project", "--title", "title", "--body", "body"}, Cwd: repo, Repo: "owner/project"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ghApprovalExecutor(s.rooms.Notify.Rooms)(context.Background(), approvals.Request{Room: rm.ID, Payload: payload})
	if err == nil || !strings.Contains(err.Error(), "stdout exceeded 1 MiB") || result.ExitCode == nil || *result.ExitCode == 0 {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

func TestBindApprovalFilesRequiresSnapshot(t *testing.T) {
	_, _, err := bindApprovalFiles(approvals.GHPayload{Argv: []string{"pr", "create", "--body-file", "body.md"}})
	if err == nil || !strings.Contains(err.Error(), "no immutable snapshot") {
		t.Fatalf("error = %v", err)
	}
}

func TestBindApprovalFilesRestoresOriginalContents(t *testing.T) {
	payload := approvals.GHPayload{
		Argv: []string{"pr", "create", "--body-file", "body.md", "--body-file=review.md"},
		Files: []approvals.FileSnapshot{
			{Path: "body.md", Data: []byte("approved body")},
			{Path: "review.md", Data: []byte("approved review")},
		},
	}
	argv, cleanup, err := bindApprovalFiles(payload)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	body, err := os.ReadFile(argv[3])
	if err != nil || string(body) != "approved body" {
		t.Fatalf("body = %q, %v", body, err)
	}
	path, ok := strings.CutPrefix(argv[4], "--body-file=")
	if !ok {
		t.Fatalf("argument = %q", argv[4])
	}
	review, err := os.ReadFile(path)
	if err != nil || string(review) != "approved review" {
		t.Fatalf("review = %q, %v", review, err)
	}
}
