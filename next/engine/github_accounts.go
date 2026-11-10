package engine

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/githubaccount"
	"github.com/resident-labs/dyna/engine/prs"
)

func (s *Service) accountEntry(token string, makeEntry func(string) approve.Entry) approve.Entry {
	entry := makeEntry(token)
	if s.githubAccounts == nil {
		return entry
	}
	resolve := func(ctx context.Context, who contract.Principal) (approve.Entry, error) {
		if who.Kind != contract.PrincipalSession {
			return approve.Entry{}, approve.ErrForbidden
		}
		session, err := s.store.Session(ctx, who.Session)
		if err != nil {
			return approve.Entry{}, err
		}
		credential, _, err := s.githubAccounts.Room(ctx, session.Room)
		if err != nil {
			return approve.Entry{}, err
		}
		return makeEntry(credential), nil
	}
	entry.Check = func(ctx context.Context, args contract.OpArgs, who contract.Principal) error {
		selected, err := resolve(ctx, who)
		if err != nil {
			return err
		}
		return selected.Check(ctx, args, who)
	}
	entry.Execute = func(ctx context.Context, id contract.OpID, args contract.OpArgs, who contract.Principal) contract.OpResult {
		selected, err := resolve(ctx, who)
		if err != nil {
			return contract.OpResult{Cause: contract.CauseGithubFailed, Error: err.Error()}
		}
		return selected.Execute(ctx, id, args, who)
	}
	return entry
}

func (s *Service) SetGitHubAccounts(resolver *githubaccount.Resolver, clients *prs.Accounts) {
	s.githubAccounts, s.githubClients = resolver, clients
}
func (s *Service) roomGitHub(ctx context.Context, room contract.RoomID) (*prs.Client, error) {
	if s.githubAccounts == nil {
		return s.pulls, nil
	}
	token, login, err := s.githubAccounts.Room(ctx, room)
	if err != nil {
		return nil, err
	}
	return s.githubClients.Client(token, login), nil
}
func (s *Service) repoViewer(ctx context.Context, repo contract.RepoName) string {
	if s.githubClients == nil {
		return s.prViewer()
	}
	client, err := s.githubClients.Repo(ctx, repo)
	if err != nil {
		return ""
	}
	return client.Viewer()
}
