package room

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitHubAccessMigrationPreservesPolicyOnce(t *testing.T) {
	s := recordsStore(t)
	cases := []struct {
		name, origin string
		stored, want GitHubAccess
	}{
		{"old-personal", "resident-labs/project", "", GitHubReadWrite},
		{"old-other", "acme/project", "", GitHubReadOnly},
		{"explicit-other", "acme/project", GitHubReadWrite, GitHubReadWrite},
		{"explicit-personal", "resident-labs/project", GitHubReadOnly, GitHubReadOnly},
	}
	for _, tc := range cases {
		repo := filepath.Join(t.TempDir(), tc.name)
		for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "remote", "add", "origin", "https://github.com/" + tc.origin + ".git"}} {
			if out, err := exec.Command("/usr/bin/git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git: %v: %s", err, out)
			}
		}
		rec, err := s.Create(CreateInput{Name: tc.name, Repo: repo})
		if err != nil {
			t.Fatal(err)
		}
		// Retain an old row that has not yet received an explicit policy.
		rec.GitHubAccess = tc.stored
		if err := s.writeRecord(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrateRecords(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		rec, err := s.Resolve(tc.name)
		if err != nil || rec.GitHubAccess != tc.want {
			t.Fatalf("%s: stored policy = %s, %v; want %s", tc.name, rec.GitHubAccess, err, tc.want)
		}
	}
	before := roomsRevision(t, s)
	if err := s.migrateRecords(); err != nil {
		t.Fatal(err)
	}
	if after := roomsRevision(t, s); after != before {
		t.Fatalf("repeated migration changed revision: %d -> %d", before, after)
	}
}
