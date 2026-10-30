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
}

func (r *fakeRepos) Ref(_ context.Context, repo contract.RepoName, _ string) (string, error) {
	r.calls++
	if hash := r.commits[repo]; hash != "" {
		return hash, r.err
	}
	return "abc", r.err
}

func (r *fakeRepos) Get(_ context.Context, _ contract.RoomID, _ contract.SessionID, _ contract.HostName, fetch bool) ([]contract.BoxRepo, error) {
	if fetch {
		r.prepared = true
	}
	return []contract.BoxRepo{{Name: "owner/repo", Cache: "/host/git/owner/repo.git"}}, nil
}

type placementRepos struct{}

func (placementRepos) Ref(context.Context, contract.RepoName, string) (string, error) {
	return "", nil
}

func (placementRepos) Get(_ context.Context, _ contract.RoomID, _ contract.SessionID, _ contract.HostName, fetch bool) ([]contract.BoxRepo, error) {
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
	_, err := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "second", Profile: "test"})
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
