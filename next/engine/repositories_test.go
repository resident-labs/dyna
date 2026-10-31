package engine

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type fakeRepos struct {
	prepared bool
	err      error
	calls    int
	commits  map[contract.RepoName]string
	size     contract.BoxSizeName
	deleted  string
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
	return []contract.BoxRepo{{Name: "owner/repo", Cache: "/host/git/owner/repo.git"}}, nil
}

func (r *fakeRepos) Size(context.Context, contract.RepoName) (contract.BoxSizeName, error) {
	return r.size, nil
}

type placementRepos struct{}

func (placementRepos) DeleteArchiveRef(context.Context, contract.RepoName, string, string) error {
	return nil
}

func (placementRepos) Size(context.Context, contract.RepoName) (contract.BoxSizeName, error) {
	return "", nil
}

func (placementRepos) Ref(context.Context, contract.RepoName, string) (string, error) {
	return "", nil
}

func (placementRepos) Get(_ context.Context, _ contract.SessionID, _ contract.HostName, fetch bool) ([]contract.BoxRepo, error) {
	if !fetch {
		return nil, nil
	}
	return []contract.BoxRepo{{Name: "owner/repo"}}, nil
}

type checkingBoxes struct {
	repos *fakeRepos
	t     *testing.T
}

func (b checkingBoxes) Create(_ context.Context, spec BoxSpec) (string, error) {
	if !b.repos.prepared || len(spec.Repos) != 1 || spec.Repos[0].Name != "owner/repo" {
		b.t.Fatal("repository was not prepared before box creation")
	}
	return "image", nil
}
func (checkingBoxes) Remove(context.Context, contract.BoxID) error { return nil }

func TestStartPreparesRepositoriesBeforeBox(t *testing.T) {
	s, room, _, ctx := setup(t)
	repos := &fakeRepos{}
	s.SetRepositories(repos)
	s.boxes = checkingBoxes{repos, t}
	resetLifecycle(s)
	_, err := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "second", Profile: "test", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
}
func TestHelloReturnsRepositories(t *testing.T) {
	s, _, session, ctx := setup(t)
	s.SetRepositories(&fakeRepos{})
	response, err := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.BoxHelloRequest{})
	if err != nil || len(response.Repos) != 1 || response.Repos[0].Name != "owner/repo" {
		t.Fatalf("response %+v, error %v", response, err)
	}
}

func TestSessionStartRepositorySize(t *testing.T) {
	s, room, _, ctx := setup(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	s.SetRepositories(&fakeRepos{size: contract.BoxSizeSmall})
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	for name, tc := range map[string]struct {
		size, want contract.BoxSizeName
	}{"repository default": {"", contract.BoxSizeSmall}, "request wins": {contract.BoxSizeLarge, contract.BoxSizeLarge}} {
		started, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "lead-" + string(tc.want), Profile: "test", Size: tc.size})
		if e != nil {
			t.Fatal(e)
		}
		if got, err := s.store.Box(ctx, started.Box); err != nil || got.Size.Name != tc.want {
			t.Fatalf("%s: size %v: %v", name, got.Size.Name, err)
		}
	}
}
