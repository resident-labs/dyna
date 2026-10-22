package watch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"dyna-agent/internal/subagent"
)

// CloseSession releases reviewer roles and uses the normal author transfer,
// including notice successors and author history.
func (s *Service) CloseSession(ctx context.Context, from, heir subagent.Info) ([]string, error) {
	var summary []string
	err := s.Store.update(func(st *State) error {
		for _, r := range st.Records {
			if r.RoomID != from.RoomID || r.State != "open" || !r.Watching {
				continue
			}
			reviewer := r.Reviewer == from.ID || r.ReviewerOverride == from.ID || slices.Contains(r.RequiredReviewers, from.ID)
			if !reviewer {
				continue
			}
			retireSessionReviewNotices(r, from.ID, "session-closed")
			if r.Reviewer == from.ID {
				rememberReviewAssignment(r)
				r.Reviewer, r.ReviewerName, r.ReviewerMode = "", "", ""
				// Keep a completed verdict at its reviewed head.
				if r.Verdict == "" {
					r.RoundHead = ""
					r.RoundStarted = time.Time{}
				}
			}
			if r.ReviewerOverride == from.ID {
				r.ReviewerOverride = ""
			}
			r.RequiredReviewers = slices.DeleteFunc(r.RequiredReviewers, func(id string) bool { return id == from.ID })
			summary = append(summary, fmt.Sprintf("PR #%d reviewer released to the pool", r.Number))
		}
		return nil
	})
	if err != nil {
		return summary, err
	}
	st, err := s.Store.Read()
	if err != nil {
		return summary, err
	}
	var errs []error
	for key, r := range st.Records {
		if r.RoomID != from.RoomID || r.State != "open" || !r.Watching || r.Author != from.ID {
			continue
		}
		if heir.ID == "" {
			errs = append(errs, fmt.Errorf("PR #%d author has no open ancestor or root", r.Number))
			continue
		}
		if err := s.Transfer(ctx, key, "author", heir, ""); err != nil {
			errs = append(errs, fmt.Errorf("PR #%d author handoff: %w", r.Number, err))
		} else {
			summary = append(summary, fmt.Sprintf("PR #%d author → %s", r.Number, heir.Name))
		}
	}
	return summary, errors.Join(errs...)
}
