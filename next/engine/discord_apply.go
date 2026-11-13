package engine

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/discord/apply"
	"github.com/resident-labs/dyna/engine/store"
)

const appliedCopiesKept = 20

// discordOwnership is one room's record of what apply owns on one server.
type discordOwnership struct {
	store *store.Store
	room  contract.RoomID
	guild string
}

func (o discordOwnership) Owned(ctx context.Context) ([]apply.Owned, error) {
	return o.store.DiscordOwned(ctx, o.room, o.guild)
}
func (o discordOwnership) Record(ctx context.Context, owned apply.Owned) error {
	return o.store.SetDiscordOwned(ctx, o.room, o.guild, owned)
}
func (o discordOwnership) Forget(ctx context.Context, kind, path string) error {
	return o.store.DeleteDiscordOwned(ctx, o.room, o.guild, kind, path)
}

func (s *Service) DiscordApply(ctx context.Context, caller contract.Principal, req contract.DiscordApplyRequest) (contract.DiscordFolderResponse, *contract.Error) {
	route, path, problem := s.discordFolderRoute(ctx, caller, req.Room, req.Server)
	if problem != nil {
		return contract.DiscordFolderResponse{}, problem
	}
	client, err := s.machineDiscordClient(ctx)
	if err != nil {
		return contract.DiscordFolderResponse{}, failure(err)
	}
	s.discordFolderMu.Lock()
	defer s.discordFolderMu.Unlock()
	report, err := apply.Run(ctx, s.discordApplyTarget(client, route, path), req.Prune)
	if err != nil {
		return contract.DiscordFolderResponse{}, invalid(err.Error())
	}
	if report.Done > 0 && !report.Failed {
		roomPath, err := s.layout.Room(route.Room)
		if err == nil {
			err = keepAppliedCopy(path, filepath.Join(roomPath, "discord-applied", route.Guild))
		}
		if err != nil {
			report.Lines = append(report.Lines, "The applied folder could not be copied: "+err.Error())
		}
	}
	return contract.DiscordFolderResponse{Text: report.String()}, nil
}

// keepAppliedCopy saves the folder as just applied under copies, which sits beside
// the folder on the engine host, and removes all but the newest appliedCopiesKept.
// UNMANAGED subtrees are not ours and are not copied.
func keepAppliedCopy(folderPath, copies string) error {
	target := filepath.Join(copies, time.Now().UTC().Format("20060102T150405.000000Z"))
	err := filepath.WalkDir(folderPath, func(source string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(folderPath, source)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if _, err := os.Stat(filepath.Join(source, "UNMANAGED.md")); err == nil {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(target, relative), 0o700)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(target, relative), data, 0o600)
	})
	if err != nil {
		return err
	}
	kept, err := os.ReadDir(copies)
	if err != nil {
		return err
	}
	for _, old := range kept[:max(0, len(kept)-appliedCopiesKept)] {
		if err := os.RemoveAll(filepath.Join(copies, old.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) discordApplyTarget(client *discord.Client, route store.DiscordRoute, folderPath string) apply.Target {
	return apply.Target{
		Client:        client,
		Guild:         route.Guild,
		Folder:        folderPath,
		Owned:         discordOwnership{s.store, route.Room, route.Guild},
		CurrentPolicy: route.Policy,
		SetPolicy: func(ctx context.Context, text string) error {
			if _, problem := s.setDiscordPolicy(ctx, route, text); problem != nil {
				return errors.New(problem.Message)
			}
			return nil
		},
	}
}
