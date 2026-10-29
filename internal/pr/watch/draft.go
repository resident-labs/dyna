package watch

import (
	"context"
	"dyna-agent/internal/pr/state"
	"fmt"
	"strings"
)

// SetDraft uses GitHub draft status as the merge hold. It saves no hold state.
func (s *Service) SetDraft(ctx context.Context, key string, draft bool, reason string) error {
	reason = strings.TrimSpace(reason)
	if draft && reason == "" {
		return fmt.Errorf("a hold reason is required")
	}
	r, err := s.Store.Get(key)
	if err != nil {
		return err
	}
	if !r.Watching || r.State != state.PROpen || changeKind(r) != "github" {
		return fmt.Errorf("PR is not open and tracked")
	}
	if s.Notify == nil || s.Notify.Rooms == nil {
		return fmt.Errorf("a PR hold or release needs the room store")
	}
	if _, err := s.Notify.Rooms.Resolve(r.RoomID); err != nil {
		return err
	}
	args := []string{"pr", "ready"}
	text := fmt.Sprintf("%s released. GitHub draft status no longer holds this PR.", changeLabel(r))
	if draft {
		args = append(args, "--undo")
		text = fmt.Sprintf("%s held as a GitHub draft. Reason: %s", changeLabel(r), Sanitize(reason))
	}
	args = append(args, "--", r.URL)
	if _, err := s.github(r.RoomID).command(ctx, "", false, args...); err != nil {
		return err
	}
	if _, err := s.deliver(ctx, r, Notice{Text: text}); err != nil {
		return fmt.Errorf("GitHub draft status changed, but the room post failed: %w", err)
	}
	return nil
}
