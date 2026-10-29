package watch

import (
	"context"
	"dyna-agent/internal/pr/state"
	"fmt"
	"os/user"

	"dyna-agent/internal/authority"

	"dyna-agent/internal/room"
)

// MergeFix permits the owner to publish one approved, CI-green repair PR.
// It bypasses only default-branch CI, while preserving every PR action gate.
func (s *Service) MergeFix(ctx context.Context, key string) error {
	// The engine passes its caller on ctx; the merge is recorded as that caller's.
	caller := authority.Of(ctx)
	if err := authority.Check("git pr merge-fix", caller, nil); err != nil {
		return err
	}
	unlock, err := s.mergeLock()
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.Store.Get(key)
	if err != nil {
		return err
	}
	if changeKind(r) != "github" || !r.Watching || r.State != state.PROpen {
		return fmt.Errorf("repair merge requires an open tracked GitHub PR")
	}
	if s.Notify == nil || s.Notify.Rooms == nil {
		return fmt.Errorf("repair merge requires a room audit log")
	}
	rm, err := s.Notify.Rooms.Resolve(r.RoomID)
	if err != nil {
		return err
	}
	requester := "the room owner"
	if owner, err := user.Current(); err == nil {
		requester += " (" + owner.Username + ")"
	}
	if caller.Role == authority.Root {
		requester = "root session " + caller.Session
	}
	// The engine records the request, so it is a typed event from the room's
	// own service: the owner asked for the merge, and the card says who asked.
	// The merge that follows is the author's own event, and it wakes the author.
	ev := mergeEventOf(r, room.MergeFixRequested)
	ev.Head, ev.Actor = r.Head, requester
	// The owner's request needs the root seat, which reads why main CI was
	// bypassed. Root's own request needs nobody, as it always did.
	needsRoot := caller.Role != authority.Root
	// Every request is its own audit line: a refused attempt keeps its record,
	// and a later attempt with another head or another caller is a second one.
	// A request has no stable identity of its own to dedupe on.
	if _, err := s.Notify.Rooms.PostEventInput(room.PostEventInput{RoomID: rm.ID, FreshID: true, Event: ev, To: []string{rm.Name}, RoomService: true, NeedsRoot: needsRoot}); err != nil {
		return fmt.Errorf("record repair merge requester: %w", err)
	}
	// The reviewer-requirement gate spans the final checks and the publication,
	// so a requirement cannot land between them.
	gate, err := s.requirementLock(ctx)
	if err != nil {
		return err
	}
	defer gate()
	current, children, ready, err := s.prepareMerge(ctx, r)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("repair PR is not eligible after fresh head, approval, CI, policy and pause checks")
	}
	_, err = s.publishMerge(ctx, current, children, nil)
	return err
}
