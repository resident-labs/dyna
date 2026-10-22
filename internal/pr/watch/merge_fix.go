package watch

import (
	"context"
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
	if changeKind(r) != "github" || !r.Watching || r.State != "open" {
		return fmt.Errorf("repair merge requires an open tracked GitHub PR")
	}
	if s.Notify == nil || s.Notify.Rooms == nil {
		return fmt.Errorf("repair merge requires a room audit log")
	}
	rm, err := s.Notify.Rooms.Resolve(r.RoomID)
	if err != nil {
		return err
	}
	owner, err := user.Current()
	if err != nil {
		return fmt.Errorf("identify repair merge owner: %w", err)
	}
	actor := "human"
	post := room.PostInput{From: "owner", To: []string{rm.Name}, Human: true, Wake: true}
	if caller.Role == authority.Root {
		id := caller.Session
		actor = "root session " + id
		post.From = rm.Name + ":" + room.RootAgent
		post.Human, post.Wake = false, false
		post.SessionIDs = map[string]string{post.From: id}
	}
	post.Text = fmt.Sprintf("Room owner %s (%s) requested merge-fix for %s at %s. This single merge bypasses main CI; fresh PR checks and approval remain required.", owner.Username, actor, r.URL, r.Head)
	if _, err := s.Notify.Rooms.Post(r.RoomID, post); err != nil {
		return fmt.Errorf("record repair merge owner: %w", err)
	}
	current, children, ready, err := s.prepareMerge(ctx, r)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("repair PR is not eligible after fresh head, approval, CI, policy and pause checks")
	}
	_, err = s.publishMerge(ctx, current, children)
	return err
}
