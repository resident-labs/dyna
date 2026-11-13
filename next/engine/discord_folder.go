package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord/folder"
	"github.com/resident-labs/dyna/engine/store"
)

func (s *Service) discordFolderRoute(ctx context.Context, caller contract.Principal, requested contract.RoomID, server string) (store.DiscordRoute, string, *contract.Error) {
	var roomID contract.RoomID
	if caller.Kind == contract.PrincipalOwner {
		if requested == "" {
			return store.DiscordRoute{}, "", invalid("room is required for owner callers")
		}
		resolved, _, err := s.store.ResolveRoom(ctx, string(requested))
		if err != nil {
			return store.DiscordRoute{}, "", invalid("unknown room")
		}
		roomID = resolved
	} else if caller.Kind == contract.PrincipalSession {
		session, err := s.store.Session(ctx, caller.Session)
		if err != nil {
			return store.DiscordRoute{}, "", failure(err)
		}
		tree, err := s.store.RoomTree(ctx, session.Room)
		if err != nil {
			return store.DiscordRoute{}, "", failure(err)
		}
		if tree.Root() != caller.Session {
			return store.DiscordRoute{}, "", forbidden()
		}
		roomID = session.Room
	} else {
		return store.DiscordRoute{}, "", forbidden()
	}
	routes, err := s.store.DiscordRoutes(ctx, roomID)
	if err != nil {
		return store.DiscordRoute{}, "", failure(err)
	}
	var selected store.DiscordRoute
	count := 0
	for _, route := range routes {
		if route.Channel != "" {
			continue
		}
		count++
		if server != "" && route.Guild != server {
			continue
		}
		if selected.Guild != "" {
			return store.DiscordRoute{}, "", invalid("server is required when several servers are mapped")
		}
		selected = route
	}
	if selected.Guild == "" {
		return selected, "", invalid("no whole-server mapping matches")
	}
	roomPath, err := s.layout.Room(roomID)
	if err != nil {
		return selected, "", failure(err)
	}
	path := filepath.Join(roomPath, "discord")
	if count > 1 {
		path = filepath.Join(path, selected.Guild)
	}
	return selected, path, nil
}

func (s *Service) DiscordExport(ctx context.Context, caller contract.Principal, req contract.DiscordExportRequest) (contract.DiscordFolderResponse, *contract.Error) {
	route, path, problem := s.discordFolderRoute(ctx, caller, req.Room, req.Server)
	if problem != nil {
		return contract.DiscordFolderResponse{}, problem
	}
	client, err := s.machineDiscordClient(ctx)
	if err != nil {
		return contract.DiscordFolderResponse{}, failure(err)
	}
	snapshot, err := folder.ReadSnapshot(ctx, client, route.Guild)
	if err != nil {
		return contract.DiscordFolderResponse{}, invalid(err.Error())
	}
	report, err := folder.Export(path, snapshot, req.Force)
	if err != nil {
		return contract.DiscordFolderResponse{}, invalid(err.Error())
	}
	report = append(report, fmt.Sprintf("Exported server %s to %s. No messages or role membership were exported.", route.Guild, path))
	return contract.DiscordFolderResponse{Text: strings.Join(report, "\n")}, nil
}

func (s *Service) DiscordPlan(ctx context.Context, caller contract.Principal, req contract.DiscordPlanRequest) (contract.DiscordFolderResponse, *contract.Error) {
	route, path, problem := s.discordFolderRoute(ctx, caller, req.Room, req.Server)
	if problem != nil {
		return contract.DiscordFolderResponse{}, problem
	}
	desired, err := folder.Load(path)
	if err != nil {
		return contract.DiscordFolderResponse{}, invalid(err.Error())
	}
	client, err := s.machineDiscordClient(ctx)
	if err != nil {
		return contract.DiscordFolderResponse{}, failure(err)
	}
	snapshot, err := folder.ReadSnapshot(ctx, client, route.Guild)
	if err != nil {
		return contract.DiscordFolderResponse{}, invalid(err.Error())
	}
	plan, err := folder.BuildPlan(desired, snapshot)
	if err != nil {
		return contract.DiscordFolderResponse{}, invalid(err.Error())
	}
	return contract.DiscordFolderResponse{Text: plan.String()}, nil
}
