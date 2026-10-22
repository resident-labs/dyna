// Package testfixture creates local repositories for room integration tests.
package testfixture

import (
	"os/exec"
	"testing"

	"dyna-agent/internal/room"
)

// Repository gives an existing room a real origin and an explicit write policy.
func Repository(t *testing.T, rooms *room.Store, ref, host, repo, access string) room.Record {
	t.Helper()
	rec, err := rooms.Resolve(ref)
	if err != nil {
		t.Fatal(err)
	}
	root := rec.Repo
	if root == "" {
		root = t.TempDir()
	}
	for _, args := range [][]string{{"init", "-q", root}, {"-C", root, "remote", "add", "origin", "https://" + host + "/" + repo + ".git"}} {
		if out, err := exec.Command("/usr/bin/git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	if err := rooms.UpdatePreferences(rec.ID, func(r *room.Record) error { r.Repo = root; r.GitHubAccess = access; return nil }); err != nil {
		t.Fatal(err)
	}
	rec.Repo, rec.GitHubAccess = root, access
	return rec
}
