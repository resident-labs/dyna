package roomsvc

import (
	"context"
	"fmt"
	"strings"

	"dyna-agent/internal/agentslice"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/shells"
)

func (s *Service) ArchiveRoom(ref string) error        { return s.rooms().SetArchived(ref, true) }
func (s *Service) UnarchiveRoom(ref string) error      { return s.rooms().SetArchived(ref, false) }
func (s *Service) RenameRoom(ref, name string) error   { return s.rooms().Rename(ref, name) }
func (s *Service) PinRoom(ref string, order int) error { return s.rooms().SetPinned(ref, true, order) }
func (s *Service) UnpinRoom(ref string) error          { return s.rooms().SetPinned(ref, false, 0) }
func (s *Service) SetRoomRootProfile(ctx context.Context, ref, name string) error {
	if err := authority.Check("room edit", authority.Of(ctx), nil); err != nil {
		return err
	}
	rec, err := s.rooms().Resolve(ref)
	if err != nil {
		return err
	}
	if _, err := authority.ActorIn(ctx, rec.ID); err != nil {
		return err
	}
	return s.rooms().SetRootProfile(ref, name)
}

// SetRoomCheckIn stores a room's parent check-in cadence: a duration such as
// 30m, off to stop this room's check-ins, or default to use the parent
// check-ins hook's global interval. Only the owner and the room's root may
// change it, like every room setting.
func (s *Service) SetRoomCheckIn(ctx context.Context, ref, value string) error {
	if err := s.authorizeRoomEdit(ctx, ref); err != nil {
		return err
	}
	setting, err := room.ParseCheckIn(value)
	if err != nil {
		return err
	}
	_, err = s.rooms().SetCheckIn(ctx, ref, setting)
	return err
}

// SetRoomUpstream names the parent repository of a room's fork checkout, so
// `pr track` accepts a PR opened on that parent. "off" clears it. Only the
// owner and the room's root may change it.
func (s *Service) SetRoomUpstream(ctx context.Context, ref, value string) error {
	if err := s.authorizeRoomEdit(ctx, ref); err != nil {
		return err
	}
	upstream, err := room.ParseUpstream(value)
	if err != nil {
		return err
	}
	return s.rooms().SetUpstream(ref, upstream)
}

// SetRoomLimitResult is the answer to a saved limit: ApplyWarning names why
// the live slice did not pick the saved limit up, empty when it did. The
// saved value is the source of truth: later launches and applies use it even
// when the live move failed, so a failed apply is not an error.
type SetRoomLimitResult struct {
	ApplyWarning string `json:"applyWarning,omitempty"`
}

// SetRoomLimit stores one of a room's resource limits: a size such as 2G for
// memory, a count such as 512 for tasks, off to remove that limit, or default
// to use the machine default. Only the owner and the room's root may change
// it, like every room setting. The saved limit is what later launches use;
// this call also applies it live and reports a warning when that failed.
func (s *Service) SetRoomLimit(ctx context.Context, ref, field, value string) (SetRoomLimitResult, error) {
	if err := s.authorizeRoomEdit(ctx, ref); err != nil {
		return SetRoomLimitResult{}, err
	}
	f, err := room.ParseLimitField(field)
	if err != nil {
		return SetRoomLimitResult{}, err
	}
	limit, err := f.Parse(value)
	if err != nil {
		return SetRoomLimitResult{}, err
	}
	rec, err := s.rooms().SetLimit(ctx, ref, f, limit)
	if err != nil {
		return SetRoomLimitResult{}, err
	}
	if err := agentslice.ApplyRoomLimits(rec.ID); err != nil {
		return SetRoomLimitResult{ApplyWarning: "the room keeps the limit; the live slice did not pick it up: " + err.Error()}, nil
	}
	return SetRoomLimitResult{}, nil
}

// AddRoomRepo registers another repository whose checkouts the room accepts
// for local changes. A non-empty upstream names that repository's parent, so
// `pr track` accepts PRs opened on it. Only the owner or the room's root may.
func (s *Service) AddRoomRepo(ctx context.Context, ref, path, upstream string) error {
	if err := s.authorizeRoomEdit(ctx, ref); err != nil {
		return err
	}
	parent, err := room.ParseUpstream(upstream)
	if err != nil {
		return err
	}
	return s.rooms().AddRepo(ref, path, parent)
}

// RemoveRoomRepo unregisters a repository added with AddRoomRepo.
func (s *Service) RemoveRoomRepo(ctx context.Context, ref, path string) error {
	if err := s.authorizeRoomEdit(ctx, ref); err != nil {
		return err
	}
	return s.rooms().RemoveRepo(ref, path)
}

func (s *Service) authorizeRoomEdit(ctx context.Context, ref string) error {
	if err := authority.Check("room edit", authority.Of(ctx), nil); err != nil {
		return err
	}
	rec, err := s.rooms().Resolve(ref)
	if err != nil {
		return err
	}
	_, err = authority.ActorIn(ctx, rec.ID)
	return err
}

// DeleteRoom refuses deletion while open sessions or live work pin the room.
// Closed session records remain readable after the room is deleted.
func (s *Service) DeleteRoom(ctx context.Context, ref string) error {
	if err := authority.Check("room delete", authority.Of(ctx), nil); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.rooms().Delete(ref, s.deleteBlockers)
	return err
}

// deleteBlockers lists the open sessions and live work that pin a room.
func (s *Service) deleteBlockers(rec room.Record) error {
	var blockers []string
	// Read metadata only: Info/List can report repaired turns while Delete
	// holds the room root lock, which a report may need again.
	sessions, err := s.sessions().ListOpenMeta()
	if err != nil {
		return err
	}
	for _, meta := range sessions {
		if meta.RoomID == rec.ID {
			blockers = append(blockers, fmt.Sprintf("session %s (open)", meta.Address()))
		}
	}
	runs, err := runstore.List()
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.RoomID == rec.ID && r.Status == "running" && runstore.Alive(r) {
			blockers = append(blockers, fmt.Sprintf("run %s (running)", r.ID))
		}
	}
	shellList, err := shells.DefaultStore().List(false)
	if err != nil {
		return err
	}
	for _, sh := range shellList {
		if sh.RoomID == rec.ID {
			blockers = append(blockers, fmt.Sprintf("shell %s (%s)", sh.ID, sh.Status))
		}
	}
	if len(blockers) == 0 && s.Notify.ActiveRoomIDs()[rec.ID] {
		blockers = append(blockers, "live work")
	}
	if len(blockers) == 0 {
		return nil
	}
	return fmt.Errorf("room %s is in use; close or stop these first:\n  %s", rec.Name, strings.Join(blockers, "\n  "))
}
