package engine

import (
	"context"
	"log"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/websvc"
)

// createRoomRecord creates or reuses the room that `dyna room create`
// resolved (requirement 2). The CLI resolves the name and repo, which only
// reads; the engine writes the record. The record names its creator from the
// caller (requirement 3).
func (s services) createRoomRecord(ctx context.Context, in room.CreateInput) (room.Record, error) {
	caller, local := websvc.CallerFromContext(ctx)
	by, err := createdBy(caller, local, authority.Of(ctx), in.CreatedBy, enforceCallers, log.Printf)
	if err != nil {
		return room.Record{}, err
	}
	in.CreatedBy = by
	return s.rooms.CreateRoomRecord(ctx, in)
}

// createRoom is the app's New room call. Its record names the creator by the
// same rule as createRoomRecord, never from the engine's own environment.
// The app claims no creator.
func (s services) createRoom(ctx context.Context, in roomsvc.CreateRoomInput) (roomsvc.CreatedRoom, error) {
	caller, local := websvc.CallerFromContext(ctx)
	by, err := createdBy(caller, local, authority.Of(ctx), "", enforceCallers, log.Printf)
	if err != nil {
		return roomsvc.CreatedRoom{}, err
	}
	in.CreatedBy = by
	return s.rooms.CreateRoom(ctx, in)
}

// createdBy is who a room record names as its creator: a session's ID, "run
// <id> agent <n>" for a workflow worker, or "unverified" (see
// recordedCaller). The owner names its own harness session, if it has one
// (runstore.OwnerFromEnv), so claimed is kept for the owner only. It is not
// checked: the owner is trusted, and the creator is a record label, not a
// permission.
func createdBy(caller websvc.Caller, local bool, actor authority.Actor, claimed string, enforce bool, logf func(string, ...any)) (string, error) {
	as, ok, err := recordedCaller(caller, local, actor, enforce, logf, "creating a room")
	switch {
	case err != nil:
		return "", err
	case !ok:
		return unverified, nil
	case as.Run != "":
		return workerName(as), nil
	case as.Session != "":
		return as.Session, nil
	}
	return claimed, nil
}
