package ghclient

import (
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

func TestSetAccessServiceAuthority(t *testing.T) {
	rooms := &room.Store{Root: t.TempDir()}
	rec, err := rooms.Create(room.CreateInput{Name: "access", CreatedBy: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range []string{"", "root", "subagent", "workflow"} {
		t.Run(process, func(t *testing.T) {
			t.Setenv("DYNA_MODE", process)
			t.Setenv("DYNA_SESSION", "")
			if process != "" {
				t.Setenv("DYNA_SESSION", "ses-test")
			}
			if err := rooms.UpdatePreferences(rec.ID, func(r *room.Record) error { r.GitHubAccess = ReadOnly; return nil }); err != nil {
				t.Fatal(err)
			}
			err := SetAccess(rooms, rec.ID, ReadWrite)
			if process == "subagent" || process == "workflow" {
				if err == nil || !strings.HasPrefix(err.Error(), "SetGitHubAccess is ") {
					t.Fatalf("worker: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			got, err := rooms.Resolve(rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := ReadWrite
			if process == "subagent" || process == "workflow" {
				want = ReadOnly
			}
			if got.GitHubAccess != want {
				t.Fatalf("access=%s, want=%s", got.GitHubAccess, want)
			}
		})
	}
}
