package watch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"dyna-agent/internal/pr/facts"
	"dyna-agent/internal/subagent"
)

// pendingVerdictFrom reports whether a saved submission from id awaits replay.
func pendingVerdictFrom(r *Record, id string) bool {
	for p := r.PendingVerdict; p != nil; p = p.Next {
		if p.Actor == id {
			return true
		}
	}
	return false
}

// deferredReplayReviewer preserves only the saved review's authorization after
// its session closes. New submissions still require an open named reviewer.
func deferredReplayReviewer(r Record, actor, head string) bool {
	if !slices.Contains(r.ReleaseReviewersAfterReplay, actor) {
		return false
	}
	for p := r.PendingVerdict; p != nil; p = p.Next {
		if p.Actor == actor && p.Head == head {
			return true
		}
	}
	return false
}

// releaseReviewer returns id's roles on r to the pool.
func releaseReviewer(r *Record, id string) {
	retireSessionReviewNotices(r, id, "session-closed")
	missingRequired := slices.Contains(facts.MissingRequiredReviewers(*r), id)
	if r.Reviewer == id {
		rememberReviewAssignment(r)
		r.Reviewer, r.ReviewerName, r.ReviewerMode = "", "", ""
		// Keep a completed verdict at its reviewed head.
		if r.Verdict == "" {
			r.RoundHead = ""
			r.RoundStarted = time.Time{}
		}
	}
	if r.ReviewerOverride == id {
		r.ReviewerOverride = ""
	}
	if missingRequired {
		// A missing named review becomes fresh pool work, not a waived gate.
		r.RoundHead, r.Verdict = "", ""
		r.RoundStarted = time.Time{}
	}
	r.ReleaseReviewersAfterReplay = slices.DeleteFunc(r.ReleaseReviewersAfterReplay, func(x string) bool { return x == id })
	r.RequiredReviewers = slices.DeleteFunc(r.RequiredReviewers, func(x string) bool { return x == id })
}

// CloseSession releases reviewer roles and uses the normal author transfer,
// including notice successors and author history. A reviewer with a
// saved verdict keeps its role until the replay applies or rejects it.
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
			if pendingVerdictFrom(r, from.ID) {
				if !slices.Contains(r.ReleaseReviewersAfterReplay, from.ID) {
					r.ReleaseReviewersAfterReplay = append(r.ReleaseReviewersAfterReplay, from.ID)
				}
				summary = append(summary, fmt.Sprintf("PR #%d reviewer release waits for its saved verdict", r.Number))
				continue
			}
			releaseReviewer(r, from.ID)
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
		moved, err := s.transfer(ctx, key, "author", heir, "", transferOptions{expectAuthor: from.ID, inheritUnread: true})
		if err != nil {
			errs = append(errs, fmt.Errorf("PR #%d author handoff: %w", r.Number, err))
		} else if moved {
			summary = append(summary, fmt.Sprintf("PR #%d author → %s", r.Number, heir.Name))
		}
	}
	return summary, errors.Join(errs...)
}

// releaseDeferredReviewers finishes the releases CloseSession deferred, once
// the reviewer's saved verdicts have been applied or rejected.
func (s *Service) releaseDeferredReviewers(ctx context.Context) error {
	st, err := s.readTickState(ctx)
	if err != nil {
		return err
	}
	needed := false
	for _, r := range st.Records {
		needed = needed || len(r.ReleaseReviewersAfterReplay) != 0
	}
	if !needed {
		return nil
	}
	return s.Store.update(func(st *State) error {
		for _, r := range st.Records {
			for _, id := range slices.Clone(r.ReleaseReviewersAfterReplay) {
				if !pendingVerdictFrom(r, id) {
					releaseReviewer(r, id)
				}
			}
		}
		return nil
	})
}
