package git

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

// RoomRepositories is implemented by the room store.
type RoomRepositories interface {
	RoomRepos(context.Context, contract.RoomID) ([]contract.RoomRepo, error)
}

// Hosts supplies the cache connection and its host data folder.
type Hosts interface {
	CacheHost(context.Context, contract.HostName) (CacheHost, string, error)
}

type Repositories struct {
	Rooms  RoomRepositories
	Hosts  Hosts
	Mirror *Mirror
}

func (r *Repositories) List(ctx context.Context, room contract.RoomID, session contract.SessionID, host contract.HostName) ([]contract.BoxRepo, error) {
	repos, err := r.Rooms.RoomRepos(ctx, room)
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return nil, nil
	}
	_, hostData, err := r.Hosts.CacheHost(ctx, host)
	if err != nil {
		return nil, err
	}
	result := make([]contract.BoxRepo, 0, len(repos))
	for _, repo := range repos {
		item, err := r.Mirror.Repo(ctx, hostData, repo.Repo, session)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (r *Repositories) Prepare(ctx context.Context, room contract.RoomID, session contract.SessionID, host contract.HostName) ([]contract.BoxRepo, error) {
	repos, err := r.Rooms.RoomRepos(ctx, room)
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return nil, nil
	}
	cache, hostData, err := r.Hosts.CacheHost(ctx, host)
	if err != nil {
		return nil, err
	}
	result := make([]contract.BoxRepo, 0, len(repos))
	for _, repo := range repos {
		item, err := r.Mirror.Prepare(ctx, cache, hostData, repo.Repo, session)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}
