package ghclient

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/room"
)

func TestClassifyForApprovalUsesRoomAccountAndCommand(t *testing.T) {
	records := setupRooms(t, "owner/project")
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$*" in
"auth status"*) echo '{"hosts":{"github.com":[{"login":"writer"}]}}';;
"auth token"*) echo writer-secret;;
"api --hostname github.com repos/owner/project/issues/comments/55") echo '{"user":{"login":"writer"}}';;
"api --hostname"*) echo '{"permissions":{"push":true}}';;
"issue view"*) echo '{"author":{"login":"writer"}}';;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	class, err := ClassifyForApproval(context.Background(), exe, records[0].Repo, []string{"issue", "close", "1"})
	if err != nil || class != approvals.ClassAgent {
		t.Fatalf("class = %q, %v", class, err)
	}
	class, err = ClassifyForApproval(context.Background(), exe, records[0].Repo, []string{"api", "repos/owner/project/issues/comments/55", "-X", "PATCH", "-f", "body=hi"})
	if err != nil || class != approvals.ClassAgent {
		t.Fatalf("own comment class = %q, %v", class, err)
	}
}
