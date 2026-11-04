package git

import (
	"context"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// RoomRepositories is implemented by the room store.
type RoomRepositories interface {
	RoomRepos(context.Context, contract.RoomID) ([]contract.RoomRepo, error)
	Session(context.Context, contract.SessionID) (store.Session, error)
	OwnsPRBranch(context.Context, contract.RepoName, contract.SessionID, string) (bool, error)
	ResumedSessions(context.Context, contract.RepoName, contract.SessionID) ([]contract.SessionID, error)
}

// Hosts supplies the cache connection.
type Hosts interface {
	CacheHost(context.Context, contract.HostName) (CacheHost, error)
}

type Repositories struct {
	Rooms  RoomRepositories
	Hosts  Hosts
	Mirror *Mirror
}

func (r *Repositories) Ref(ctx context.Context, repo contract.RepoName, ref string) (string, error) {
	return r.Mirror.Ref(ctx, repo, ref)
}

// Seed is Get for a session's first setup: it refreshes from GitHub first, and the notice says when that did not happen.
func (r *Repositories) Seed(ctx context.Context, session contract.SessionID, host contract.HostName) ([]contract.BoxRepo, string, error) {
	selected, err := r.Rooms.Session(ctx, session)
	if err != nil || selected.Repo == "" {
		return nil, "", err
	}
	cache, err := r.Hosts.CacheHost(ctx, host)
	if err != nil {
		return nil, "", err
	}
	item, notice, err := r.Mirror.Seed(ctx, cache, selected.Repo, session)
	if err != nil {
		return nil, "", err
	}
	return []contract.BoxRepo{item}, notice, nil
}

func (r *Repositories) Get(ctx context.Context, session contract.SessionID, host contract.HostName, fetch bool) ([]contract.BoxRepo, error) {
	selected, err := r.Rooms.Session(ctx, session)
	if err != nil {
		return nil, err
	}
	if selected.Repo == "" {
		return nil, nil
	}
	cache, err := r.Hosts.CacheHost(ctx, host)
	if err != nil {
		return nil, err
	}
	item, err := r.Mirror.Prepare(ctx, cache, selected.Repo, session, fetch)
	if err != nil {
		return nil, err
	}
	return []contract.BoxRepo{item}, nil
}
