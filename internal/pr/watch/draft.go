package watch

import (
	"context"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/state"
	"fmt"
	"strings"
	"time"
)

func (s *Service) setHold(key string, hold *state.Hold) error {
	return s.Store.updateRecordOrAll(key, func(*State) bool { return false }, func(st *State) error {
		st.Records[key].Hold = hold
		return nil
	})
}

// SetDraft holds a PR in Dyna's state and as a GitHub draft. Release clears both.
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
	// The hold is saved first, so a failed GitHub call still leaves the PR held.
	// Dyna keeps the hold until release, whatever GitHub's draft state says.
	if draft {
		actor := authority.Of(ctx)
		hold := &state.Hold{Reason: reason, By: actor.Session, At: time.Now().UTC()}
		if err := s.setHold(key, hold); err != nil {
			return err
		}
	}
	if _, err := s.github(r.RoomID).command(ctx, "", false, args...); err != nil {
		return err
	}
	if !draft {
		if err := s.setHold(key, nil); err != nil {
			return err
		}
	}
	if _, err := s.deliver(ctx, r, Notice{Text: text}); err != nil {
		return fmt.Errorf("GitHub draft status changed, but the room post failed: %w", err)
	}
	return nil
}
