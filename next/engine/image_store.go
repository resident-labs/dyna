package engine

import (
	"context"
	"errors"
	"log"
	"slices"

	"github.com/resident-labs/dyna/contract"
	enginegit "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/images"
)

// Images is the store of room images.
func (s *Service) Images() *images.Store { return s.images }

// imageRoom is the room a request acts on: a session's own, or the one the owner names.
func (s *Service) imageRoom(ctx context.Context, caller contract.Principal, room contract.RoomID) (contract.RoomID, *contract.Error) {
	target, problem := s.targetOf(ctx, caller, room, false)
	return target.room, problem
}

func imageFailure(err error) *contract.Error {
	switch {
	case errors.Is(err, images.ErrInvalid):
		return invalid(err.Error())
	case errors.Is(err, images.ErrMissing):
		return &contract.Error{Code: contract.ErrNotFound, Message: err.Error()}
	}
	return failure(err)
}

// ImageSave replaces the room's image of that name. A box takes it at its next start.
func (s *Service) ImageSave(ctx context.Context, caller contract.Principal, req contract.ImageSaveRequest) (contract.ImageSaveResponse, *contract.Error) {
	room, problem := s.imageRoom(ctx, caller, req.Room)
	if problem != nil {
		return contract.ImageSaveResponse{}, problem
	}
	var image contract.RoomImage
	err := s.roomWrite(ctx, room, func() (err error) {
		image, err = s.images.Save(room, req.Name, req.Archive)
		return err
	})
	if err != nil {
		return contract.ImageSaveResponse{}, imageFailure(err)
	}
	return contract.ImageSaveResponse{Image: image}, nil
}

// ImageList lists a room's images. The owner with no room gets every room's.
func (s *Service) ImageList(ctx context.Context, caller contract.Principal, req contract.ImageListRequest) (contract.ImageListResponse, *contract.Error) {
	var rooms []contract.RoomID
	if caller.Kind == contract.PrincipalOwner && req.Room == "" {
		var err error
		if rooms, err = s.store.OpenRooms(ctx); err != nil {
			return contract.ImageListResponse{}, failure(err)
		}
	} else {
		room, problem := s.imageRoom(ctx, caller, req.Room)
		if problem != nil {
			return contract.ImageListResponse{}, problem
		}
		rooms = []contract.RoomID{room}
	}
	out := contract.ImageListResponse{Images: []contract.RoomImage{}}
	for _, room := range rooms {
		held, err := s.images.List(room)
		if err != nil {
			return contract.ImageListResponse{}, failure(err)
		}
		out.Images = append(out.Images, held...)
	}
	return out, nil
}

// ImageShow gives one image's Containerfile and file list.
func (s *Service) ImageShow(ctx context.Context, caller contract.Principal, req contract.ImageShowRequest) (contract.ImageShowResponse, *contract.Error) {
	room, problem := s.imageRoom(ctx, caller, req.Room)
	if problem != nil {
		return contract.ImageShowResponse{}, problem
	}
	image, recipe, files, err := s.images.Show(room, req.Name)
	if err != nil {
		return contract.ImageShowResponse{}, imageFailure(err)
	}
	return contract.ImageShowResponse{Image: image, Containerfile: recipe, Files: files}, nil
}

// ImageRemove deletes a room's image. Boxes already running keep what they have.
func (s *Service) ImageRemove(ctx context.Context, caller contract.Principal, req contract.ImageRemoveRequest) (contract.ImageRemoveResponse, *contract.Error) {
	room, problem := s.imageRoom(ctx, caller, req.Room)
	if problem != nil {
		return contract.ImageRemoveResponse{}, problem
	}
	if err := s.roomWrite(ctx, room, func() error { return s.images.Remove(room, req.Name) }); err != nil {
		return contract.ImageRemoveResponse{}, imageFailure(err)
	}
	return contract.ImageRemoveResponse{}, nil
}

// MigrateImages copies each room's repositories' legacy .dyna/images into the room's store, once per repository and room,
// and logs what it copied and what it left. A repository with no mirror yet waits for a later start. The default repository
// goes first, so its image wins a name two repositories share.
func (s *Service) MigrateImages(ctx context.Context, mirror *enginegit.Mirror) error {
	rooms, err := s.store.OpenRooms(ctx)
	if err != nil {
		return err
	}
	for _, room := range rooms {
		repos, err := s.store.RoomRepos(ctx, room)
		if err != nil {
			return err
		}
		slices.SortStableFunc(repos, func(a, b contract.RoomRepo) int {
			if a.Default == b.Default {
				return 0
			}
			if a.Default {
				return -1
			}
			return 1
		})
		for _, repo := range repos {
			path, err := mirror.Layout.Git(repo.Repo)
			if err != nil || !mirror.Cloned(repo.Repo) {
				log.Printf("room %s: images of %s not migrated yet: no mirror", room, repo.Repo)
				continue
			}
			var copied, left []string
			err = s.roomWrite(ctx, room, func() (err error) {
				copied, left, err = s.images.Migrate(ctx, mirror.Runner, room, repo.Repo, path)
				return err
			})
			if err != nil {
				log.Printf("room %s: migrate images of %s: %v", room, repo.Repo, err)
				continue
			}
			if len(copied)+len(left) > 0 {
				log.Printf("room %s: moved the legacy .dyna/images of %s into the room: copied %v, left %v. Repository images are no longer read; use dyna image save.", room, repo.Repo, copied, left)
			}
		}
	}
	return nil
}
