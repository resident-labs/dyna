package ghclient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/room"
)

func TestApprovedForeignCommentStillRefused(t *testing.T) {
	records := setupRooms(t, "owner/project")
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"writer"}]}}';;
"auth token") echo secret;;
"api --hostname") case "$*" in *comments/7*) echo '{"user":{"login":"other"}}';; *) echo '{"permissions":{"push":true}}';; esac;;
*) exit 1;;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(BinaryEnv, exe)
	ctx := WithApprovedClass(WithRoom(context.Background(), records[0].ID), approvals.ClassOwner)
	for _, args := range [][]string{
		{"api", "repos/owner/project/issues/comments/7", "-X", "DELETE"},
		{"api", "repos/owner/project/issues/comments/7", "-X", "PATCH", "--input", "body.json"},
	} {
		cmd := Command(ctx, exe, records[0].Repo, args...)
		err := RunWithAccount(ctx, cmd, nil)
		if err == nil || !strings.Contains(err.Error(), string(ReasonForeignComment)) {
			t.Fatalf("approved foreign comment %q: %v", args, err)
		}
	}
}
