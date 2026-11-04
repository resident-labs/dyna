package engine

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

type fakeRepos struct {
	prepared bool
	err      error
	calls    int
	commits  map[contract.RepoName]string
	size     contract.BoxSizeName
	deleted  string
	stale    string // the notice Seed returns
	taken    map[string]bool
}

func (r *fakeRepos) Unavailable(context.Context, contract.RepoName) (map[string]bool, error) {
	return r.taken, r.err
}

func (r *fakeRepos) DeleteArchiveRef(_ context.Context, _ contract.RepoName, ref, _ string) error {
	r.deleted = ref
	return r.err
}

func (r *fakeRepos) Ref(_ context.Context, repo contract.RepoName, _ string) (string, error) {
	r.calls++
	if hash := r.commits[repo]; hash != "" {
		return hash, r.err
	}
	return "abc", r.err
}

func (r *fakeRepos) Get(_ context.Context, _ contract.SessionID, _ contract.HostName, fetch bool) ([]contract.BoxRepo, error) {
	if fetch {
		r.prepared = true
	}
	return []contract.BoxRepo{{Name: "owner/repo", Cache: "/host/git/owner/repo.git", Branch: "named-branch"}}, nil
}

func (r *fakeRepos) Seed(ctx context.Context, session contract.SessionID, host contract.HostName) ([]contract.BoxRepo, string, error) {
	repos, err := r.Get(ctx, session, host, true)
	return repos, r.stale, err
}

func (r *fakeRepos) Size(context.Context, contract.RepoName) (contract.BoxSizeName, error) {
	return r.size, nil
}
