package ghclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/room"
)

func TestCommentEditWithoutPushAccess(t *testing.T) {
	records := setupRooms(t, "owner/project")
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"writer"}]}}';;
"auth token") echo secret;;
"api --hostname")
  case "$4" in
  repos/owner/project/issues/comments/55)
    if [ "$TEST_AUTHOR" = fail ]; then exit 1; fi
    echo "{\"user\":{\"login\":\"$TEST_AUTHOR\"}}";;
  *) echo '{"permissions":{"push":false}}';;
  esac;;
*) exit 1;;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := WithRoom(context.Background(), records[0].ID)
	args := []string{"api", "repos/owner/project/issues/comments/55", "-X", "PATCH", "-f", "body=hi"}
	for _, tc := range []struct {
		author string
		reason Reason
	}{
		{"writer", ""},
		{"other", ReasonForeignComment},
		{"fail", ReasonCommentAuthor},
	} {
		t.Run(tc.author, func(t *testing.T) {
			t.Setenv("TEST_AUTHOR", tc.author)
			class, err := ClassifyForApproval(ctx, exe, records[0].Repo, args)
			if tc.reason == "" {
				if err != nil || class != approvals.ClassAgent {
					t.Fatalf("class = %q, error = %v", class, err)
				}
			} else {
				var refusal *RefusalError
				if !errors.As(err, &refusal) || refusal.Decision.Refusal != tc.reason {
					t.Fatalf("error = %v, want %s", err, tc.reason)
				}
			}
		})
	}
}
