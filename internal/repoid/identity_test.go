package repoid

import "testing"

func TestRepoNameTransportNormalization(t *testing.T) {
	for _, remote := range []string{"git@github.com:owner/repo.git", "https://github.com/owner/repo.git", "ssh://git@github.com/owner/repo.git"} {
		if RepoName(remote) != "owner/repo" {
			t.Fatal(remote)
		}
	}
}
