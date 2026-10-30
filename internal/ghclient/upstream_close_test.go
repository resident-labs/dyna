package ghclient

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/room"
)

func TestUpstreamCloseAuthorship(t *testing.T) {
	records := setupRooms(t, "fork/project")
	if err := room.DefaultStore().SetUpstream(records[0].ID, "github.com/upstream/project"); err != nil {
		t.Fatal(err)
	}
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"writer"}]}}';;
"auth token") echo secret;;
"api --hostname") echo '{"permissions":{"push":false}}';;
"pr view") echo "{\"author\":{\"login\":\"$TEST_AUTHOR\"}}";;
"pr close") exit 0;;
*) exit 1;;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(BinaryEnv, exe)
	ctx := WithRoom(context.Background(), records[0].ID)
	args := []string{"pr", "close", "--", "https://github.com/upstream/project/pull/7"}
	for _, tc := range []struct {
		name, author string
		allowed      bool
		class        approvals.Class
	}{
		{"own", "writer", true, approvals.ClassAgent}, {"foreign", "other", false, approvals.ClassOwner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TEST_AUTHOR", tc.author)
			class, err := ClassifyForApproval(ctx, exe, records[0].Repo, args)
			if err != nil || class != tc.class {
				t.Fatalf("class = %q, %v", class, err)
			}
			err = Run(ctx, Command(ctx, exe, records[0].Repo, args...))
			if (err == nil) != tc.allowed {
				t.Fatalf("close error = %v, allowed = %v", err, tc.allowed)
			}
		})
	}
}
