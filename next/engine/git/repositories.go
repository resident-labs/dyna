package git

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// RoomRepositories is implemented by the room store.
type RoomRepositories interface {
	RoomRepos(context.Context, contract.RoomID) ([]contract.RoomRepo, error)
	Session(context.Context, contract.SessionID) (store.Session, error)
	OwnsPRBranch(context.Context, contract.RepoName, contract.SessionID, string) (bool, error)
	ResumedSessions(context.Context, contract.RepoName, contract.SessionID) ([]contract.SessionID, error)
	SessionBranches(context.Context, contract.RepoName, contract.SessionID) ([]string, error)
	BranchHolder(context.Context, contract.RepoName, string) (contract.SessionID, error)
	ClaimBranch(context.Context, contract.RepoName, contract.SessionID, string, map[string]bool, time.Time) (string, error)
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
	item, notice, err := r.Mirror.Seed(ctx, cache, selected.Repo)
	if err != nil {
		return nil, "", err
	}
	if item.Branch, err = r.branch(ctx, session, selected, item.Base); err != nil {
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
	item, err := r.Mirror.Prepare(ctx, cache, selected.Repo, fetch)
	if err != nil {
		return nil, err
	}
	if item.Branch, err = r.branch(ctx, session, selected, item.Base); err != nil {
		return nil, err
	}
	return []contract.BoxRepo{item}, nil
}

var dotRun = regexp.MustCompile(`\.{2,}`)

// branch returns the session's recorded branch on its repository, recording its name first if it has none: the session
// name, made valid for git, with a suffix when the mirror, a protected name or another session has it.
func (r *Repositories) branch(ctx context.Context, session contract.SessionID, selected store.Session, base string) (string, error) {
	recorded, err := r.Rooms.SessionBranches(ctx, selected.Repo, session)
	if err != nil || len(recorded) > 0 {
		if err != nil {
			return "", err
		}
		return recorded[0], nil
	}
	remote, err := r.Mirror.Branches(ctx, selected.Repo)
	if err != nil {
		return "", err
	}
	unavailable := map[string]bool{"main": true, "master": true, "HEAD": true, base: true}
	for _, name := range remote {
		unavailable[name] = true
	}
	wanted := dotRun.ReplaceAllString(selected.Name, "-")
	if strings.HasSuffix(wanted, ".lock") {
		wanted = strings.TrimSuffix(wanted, ".lock") + "-lock"
	}
	if strings.HasSuffix(wanted, ".") {
		wanted += "-"
	}
	return r.Rooms.ClaimBranch(ctx, selected.Repo, session, wanted, unavailable, time.Now())
}
