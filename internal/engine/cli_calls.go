package engine

import (
	"context"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/websvc"
)

// cliCalls share the caller policy with CLI commands. Global operations keep
// their existing CLI access; room and run services check the target room.
var cliCalls = []string{
	"SetSetting",
	"SetRoomRootProfile",
	"SetRoomCheckIn",
	"SetRoomLimit",
	"AddRoomRepo",
	"RemoveRoomRepo",
	"SaveProfile",
	"UpsertProfile",
	"SetProfileUses",
	"RemoveProfile",
	"SetDefaultProfile",
	"SetProfileDisabled",
	"ImportPrivateConfig",
	"AddHarnessMCP",
	"AddCatalogMCP",
	"AddCatalogSkill",
	"RemoveCatalogEntry",
	"SetCatalogEntryEnabled",
	"AddHarnessSkill",
	"RemoveHarnessConfig",
	"SetHarnessConfigEnabled",
	"SyncPrivateRuntime",
	"SyncCatalogAccount",
	"InstallPrivateRuntime",
	"RemovePrivateRuntime",
	"RestartPrivateRuntimeLogin",
	"BeginPrivateRuntimeLogin",
	"FinishPrivateRuntimeLogin",
	"UsePersonalLogin",
	"FindPersonalLogin",
	"GCPrivateRuntime",
	"UpdateDyna",
	"ReconcileHookInstall",
	"RemoveHook",
	"ConfigCheckProfile",
	"GitExport",
	"StartRun",
	"AccountsList",
	"AccountsUse",
	"AccountsRemove",
	"AccountsImport",
	"ArchiveRun",
	"UnarchiveRun",
	"CancelRun",
	"PauseRun",
	"TransferRun",
	"RemoveRun",
	"ClearRuns",
	"SyncRefresh",
	"Unpin",
}

func protectCLICalls(calls map[string]websvc.Call) {
	protectCalls(calls, cliCalls, "using CLI commands", nil)
}

func (s services) syncRoom(ctx context.Context, roomRef string) (string, error) {
	rec, err := s.rooms.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return "", err
	}
	if _, err := authority.ActorIn(ctx, rec.ID); err != nil {
		return "", err
	}
	return rec.ID, nil
}

func (s services) syncRefresh(ctx context.Context, roomRef, repo string) (watch.SyncStarted, error) {
	roomID, err := s.syncRoom(ctx, roomRef)
	if err != nil {
		return watch.SyncStarted{}, err
	}
	return s.rooms.PRs.Watcher().SyncRefresh(ctx, roomID, repo)
}

func (s services) gitExport(ctx context.Context, roomRef, repo string) (watch.SyncStarted, error) {
	roomID, err := s.syncRoom(ctx, roomRef)
	if err != nil {
		return watch.SyncStarted{}, err
	}
	return s.rooms.PRs.Watcher().GitExport(ctx, roomID, repo)
}
