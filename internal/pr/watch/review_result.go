package watch

import (
	"fmt"
	"strings"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
)

// ReviewResult is one reviewer turn's typed result. The engine records exactly
// one result per reviewer turn; anything else is rejected.
type ReviewResult = state.ReviewResult

// prepareReviewResult checks the typed result before anything is saved.
func prepareReviewResult(in ReviewResult) error {
	switch in.Verdict {
	case state.VerdictApprove, state.VerdictChanges, state.VerdictApproveFast, state.VerdictStopEarly:
	default:
		return fmt.Errorf("review result must be approve, changes, approve-fast, or stop-early")
	}
	if in.ReviewID < 0 {
		return fmt.Errorf("review ID cannot be negative")
	}
	if in.Verdict == state.VerdictStopEarly {
		switch in.StopReason {
		case state.StopSuperseded, state.StopDuplicate, state.StopEmpty, state.StopTooLarge:
		default:
			return fmt.Errorf("a stop-early result needs its reason: superseded, duplicate, empty, or too-large")
		}
		if len(in.Findings) > 0 {
			return fmt.Errorf("a stop-early result carries no findings")
		}
	} else {
		if in.StopReason != "" {
			return fmt.Errorf("only a stop-early result carries a stop reason")
		}
		if strings.TrimSpace(in.Summary) == "" {
			return fmt.Errorf("summary is required")
		}
	}
	if len(in.Summary) > MaxReviewBytes {
		return fmt.Errorf("review exceeds %s; shorten it before recording (nothing was saved)", settings.FormatBytes(int64(MaxReviewBytes)))
	}
	return nil
}

// normalizeReviewResult preserves a reviewer's approve-fast decision for a
// GitHub PR. Only a local-lane change stays on the normal trial path.
func (s *Service) normalizeReviewResult(r Record, in ReviewResult) ReviewResult {
	if in.Verdict != state.VerdictApproveFast {
		return in
	}
	if changeKind(r) != "github" {
		return normalPath(in)
	}
	in.FastLand = &state.FastLand{LowRisk: true}
	return in
}

// normalPath records a fast approval as a plain approval on the normal path.
func normalPath(in ReviewResult) ReviewResult {
	in.Verdict = state.VerdictApprove
	in.FastLand = nil
	return in
}

// reviewEventOf builds the typed review result event. The findings and counts
// come from the recorded result; the Markdown body is the agent's detail and no
// card parses it.
func reviewEventOf(r Record, in ReviewResult, detail string) room.ReviewEvent {
	ev := room.ReviewEvent{
		Kind:       room.ReviewKind(in.Verdict),
		ChangeID:   changeID(r),
		Number:     r.Number,
		Label:      changeLabel(r),
		Location:   changeLocation(r),
		Head:       in.Head,
		Summary:    reviewSummary(in.Summary),
		Detail:     detail,
		StopReason: room.ReviewStopReason(in.StopReason),
	}
	for _, f := range in.Findings {
		ev.Findings = append(ev.Findings, room.ReviewFinding{ID: f.ID, Severity: room.ReviewSeverity(f.Severity), File: f.File, Line: f.Line, Why: f.Why, Fix: f.Fix, Patch: f.Patch})
		switch f.Severity {
		case state.FindingBlocking:
			ev.Blocking++
		case state.FindingShouldFix:
			ev.ShouldFix++
		default:
			ev.Nits++
		}
	}
	if in.FastLand != nil {
		ev.FastLand = in.FastLand.LowRisk
	}
	return ev
}
