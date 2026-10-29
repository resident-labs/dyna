package watch

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Close closes an open tracked PR on GitHub, then ends its Dyna tracking.
// The room's GitHub credential stays behind the engine's command path.
func (s *Service) Close(ctx context.Context, key, reason string) error {
	unlock, err := s.publicationGate(ctx, key)
	if err != nil {
		return err
	}
	defer unlock()
	reason = strings.TrimSpace(reason)
	r, err := s.Store.Get(key)
	if err != nil {
		return err
	}
	if !r.Watching || r.State != "open" || changeKind(r) != "github" {
		return fmt.Errorf("PR is not open and tracked")
	}
	if s.Notify == nil || s.Notify.Rooms == nil {
		return fmt.Errorf("a PR close needs the room store")
	}
	if _, err := s.Notify.Rooms.Resolve(r.RoomID); err != nil {
		return err
	}
	if _, err := s.github(r.RoomID).command(ctx, "", false, "pr", "close", "--", r.URL); err != nil {
		return err
	}
	snap := Snapshot{Pull: Pull{State: "closed"}}
	var out processEffects
	if err := s.Store.update(func(st *State) error {
		current := st.Records[key]
		if current == nil {
			return fmt.Errorf("PR is not tracked")
		}
		if err := sameAuthor(current, r.Author); err != nil {
			return err
		}
		s.closeRecord(current, snap, newPollNotices(current), &out)
		if reason != "" {
			id := "msg_" + digest(current.Key + "/outcome/closed")[:16]
			for i := range current.Notices {
				n := &current.Notices[i]
				if n.ID == id {
					n.Summary = Sanitize(reason)
					n.Text += "\n\nReason: " + n.Summary
				}
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("PR closed on GitHub, but saving its outcome failed: %w", err)
	}
	var postErr error
	if err := s.flush(ctx, key); err != nil {
		postErr = fmt.Errorf("PR closed on GitHub, but the room post failed: %w", err)
	}
	return errors.Join(postErr, s.finishProcess(ctx, key, snap, out))
}
