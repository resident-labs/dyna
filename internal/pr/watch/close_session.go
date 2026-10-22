package watch

import (
	"context"
	"errors"
	"fmt"
	"maps"
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

// dropPendingVerdictsFrom removes id's saved submissions and keeps every other
// reviewer's chain entry.
func dropPendingVerdictsFrom(r *Record, id string) {
	slot := &r.PendingVerdict
	for *slot != nil {
		if (*slot).Actor == id {
			*slot = (*slot).Next
			continue
		}
		slot = &(*slot).Next
	}
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
	dropPendingVerdictsFrom(r, id)
	r.ReleaseReviewerAfterReplay = false
	r.RequiredReviewers = slices.DeleteFunc(r.RequiredReviewers, func(x string) bool { return x == id })
}

// CloseSession releases reviewer roles and uses the normal author transfer,
// including notice successors and author history. An assigned reviewer with a
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
			if r.Reviewer == from.ID && pendingVerdictFrom(r, from.ID) {
				r.ReleaseReviewerAfterReplay = true
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
		if s.beforeCloseTransfer != nil {
			s.beforeCloseTransfer(key)
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
	if !slices.ContainsFunc(slices.Collect(maps.Values(st.Records)), func(r *Record) bool { return r.ReleaseReviewerAfterReplay }) {
		return nil
	}
	return s.Store.update(func(st *State) error {
		for _, r := range st.Records {
			if r.ReleaseReviewerAfterReplay && (r.Reviewer == "" || !pendingVerdictFrom(r, r.Reviewer)) {
				if r.Reviewer == "" {
					r.ReleaseReviewerAfterReplay = false
					continue
				}
				releaseReviewer(r, r.Reviewer)
			}
		}
		return nil
	})
}
