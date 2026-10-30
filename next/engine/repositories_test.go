package engine

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type fakeRepos struct{ prepared bool }

func (r *fakeRepos) Prepare(context.Context, contract.RoomID, contract.SessionID, contract.HostName) ([]contract.BoxRepo, error) {
	r.prepared = true
	return []contract.BoxRepo{{Name: "owner/repo", Cache: "/host/git/owner/repo.git"}}, nil
}
func (r *fakeRepos) List(context.Context, contract.RoomID, contract.SessionID, contract.HostName) ([]contract.BoxRepo, error) {
	return []contract.BoxRepo{{Name: "owner/repo", Cache: "/host/git/owner/repo.git"}}, nil
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
