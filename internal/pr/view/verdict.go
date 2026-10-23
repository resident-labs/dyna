package view

import (
	"context"
	"fmt"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
)

// VerdictResult carries a saved or obsolete verdict notice across the socket.
// These notices are successful command outcomes, not transport errors.
type VerdictResult struct {
	Notice string `json:"notice,omitempty"`
}

// RecordPRVerdict keeps the call name and the request shape that dyna builds
// before the typed review results still send. PR #345 renamed it to
// RecordReviewResult with a typed result, and an engine that self-updated to
// that build left a running reviewer (reviewer-349) unable to record its
// verdict: the CLI of the previous build got "engine has no RecordPRVerdict
// method". This shim maps the old request onto one typed result and records it
// through RecordReviewResult, so the two cannot drift apart.
//
// TODO: legacy/shim remove later - keep for one release after the build that
// introduced RecordReviewResult, then delete it together with the
// "RecordPRVerdict" entry in callspec.LegacyCalls.
func (s *Service) RecordPRVerdict(ctx context.Context, roomRef, key, head, verdict, summary string, reviewID int64, findings []watch.Finding) (VerdictResult, error) {
	result, err := legacyReviewResult(head, verdict, summary, reviewID, findings)
	if err != nil {
		return VerdictResult{}, err
	}
	return s.RecordReviewResult(ctx, roomRef, key, result)
}

// legacyReviewResult maps an old text verdict onto one typed review result.
// The old verdicts said what the reviewer decided, not how Dyna should land
// the change, so nothing here asks for a fast landing: a session that records
// an old verdict gets the normal path.
func legacyReviewResult(head, verdict, summary string, reviewID int64, findings []watch.Finding) (watch.ReviewResult, error) {
	result := watch.ReviewResult{Head: head, Summary: summary, ReviewID: reviewID, Findings: findings}
	switch verdict {
	case "approve":
		result.Verdict = state.VerdictApprove
	case "approve-with-nits":
		// The old engine counted this as an approval with nit findings.
		result.Verdict = state.VerdictApprove
	case "changes":
		result.Verdict = state.VerdictChanges
	case "question":
		// The old engine did not approve a question; it woke the author and
		// kept the head unapproved. A changes result does the same, and is the
		// only typed result that says "the author must act".
		result.Verdict = state.VerdictChanges
	default:
		return watch.ReviewResult{}, fmt.Errorf("verdict must be approve, approve-with-nits, changes, or question")
	}
	return result, nil
}

// RecordReviewResult uses the authenticated session as the reviewer. The
// watcher checks its assignment or room roster and the exact head inside the
// state update. The result is a typed review result; anything else is rejected.
func (s *Service) RecordReviewResult(ctx context.Context, roomRef, key string, result watch.ReviewResult) (VerdictResult, error) {
	p, err := s.prInRoom(roomRef, key)
	if err != nil {
		return VerdictResult{}, err
	}
	room, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return VerdictResult{}, err
	}
	actor, err := authority.ActorIn(ctx, room.ID)
	if err != nil {
		return VerdictResult{}, err
	}
	switch actor.Role {
	case authority.Session, authority.Root:
		if actor.Session == "" {
			return VerdictResult{}, fmt.Errorf("a verdict needs an authenticated reviewer session")
		}
	default:
		return VerdictResult{}, fmt.Errorf("a verdict needs an authenticated reviewer session")
	}
	err = p.RecordReviewResult(ctx, key, actor.Session, result)
	if notice, ok := watch.VerdictNotice(err); ok {
		return VerdictResult{Notice: notice}, nil
	}
	return VerdictResult{}, err
}
