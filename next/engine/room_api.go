package engine

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

var mentionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (s *Service) RoomCreate(ctx context.Context, caller contract.Principal, req contract.RoomCreateRequest) (contract.RoomCreateResponse, *contract.Error) {
	if !mentionName.MatchString(req.Name) {
		return contract.RoomCreateResponse{}, invalid("room name must be 1–64 letters, digits, dots, underscores or hyphens and start with a letter or digit")
	}
	if req.RootProfile == "" {
		return contract.RoomCreateResponse{}, invalid("room name and root profile are required")
	}
	now := s.now()
	value, err := id("room_", now)
	if err != nil {
		return contract.RoomCreateResponse{}, failure(err)
	}
	room := contract.RoomID(value)
	if _, e := s.startSession(ctx, caller, contract.SessionStartRequest{Room: room, Name: "root", Profile: req.RootProfile}, req.Name, nil); e != nil {
		return contract.RoomCreateResponse{}, e
	}
	return contract.RoomCreateResponse{Room: room}, nil
}

func (s *Service) RoomRepoAdd(ctx context.Context, caller contract.Principal, req contract.RoomRepoAddRequest) (contract.RoomRepoAddResponse, *contract.Error) {
	owner, name, ok := strings.Cut(string(req.Repo), "/")
	ownerOK, _ := regexp.MatchString(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`, owner)
	nameOK, _ := regexp.MatchString(`^[A-Za-z0-9_.-]+$`, name)
	if !ok || !ownerOK || !nameOK || name == "." || name == ".." {
		return contract.RoomRepoAddResponse{}, invalid("repository must be owner/name")
	}
	if err := s.store.AddRoomRepo(ctx, req.Room, req.Repo, req.Default); err != nil {
		if errors.Is(err, store.ErrRoomNotFound) {
			return contract.RoomRepoAddResponse{}, invalid("unknown room")
		}
		return contract.RoomRepoAddResponse{}, failure(err)
	}
	return contract.RoomRepoAddResponse{}, nil
}

// RepoCISet chooses where a repository's CI runs. Only the owner reaches it.
func (s *Service) RepoCISet(ctx context.Context, caller contract.Principal, req contract.RepoCISetRequest) (contract.RepoCISetResponse, *contract.Error) {
	if req.Max < 0 || !req.On && req.Max != 0 {
		return contract.RepoCISetResponse{}, invalid("max is a number of runners, for a repository that is on")
	}
	if err := s.store.SetRepoCI(ctx, req.Repo, req.On, req.Max); err != nil {
		if errors.Is(err, store.ErrRepoNotFound) {
			return contract.RepoCISetResponse{}, invalid("unknown repository")
		}
		return contract.RepoCISetResponse{}, failure(err)
	}
	return contract.RepoCISetResponse{}, nil
}

func (s *Service) RoomRepos(ctx context.Context, caller contract.Principal, req contract.RoomReposRequest) (contract.RoomReposResponse, *contract.Error) {
	if e := s.roomAccess(ctx, caller, req.Room); e != nil {
		return contract.RoomReposResponse{}, e
	}
	repos, err := s.store.RoomRepos(ctx, req.Room)
	if errors.Is(err, store.ErrRoomNotFound) {
		return contract.RoomReposResponse{}, invalid("unknown room")
	}
	if err != nil {
		return contract.RoomReposResponse{}, failure(err)
	}
	return contract.RoomReposResponse{Repos: repos}, nil
}
