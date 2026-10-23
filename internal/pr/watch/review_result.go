package watch

import (
	"context"
	"fmt"
	"strings"

	"dyna-agent/internal/pr/fastland"
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

// normalizeReviewResult applies Dyna's changed-path rule to an approve-fast
// result. A low-risk change stays fast; anything else records as a plain
// approval with the normal path, and says why. A path read that fails is not
// low risk: a fast landing needs the path rule's facts, not a guess.
func (s *Service) normalizeReviewResult(ctx context.Context, r Record, in ReviewResult) ReviewResult {
	if in.Verdict != state.VerdictApproveFast {
		return in
	}
	// Only a GitHub change has the guarded publisher that a fast landing uses.
	// A local-lane change keeps the normal trial, and says so.
	if changeKind(r) != "github" {
		return normalPath(in)
	}
	paths, err := s.changedPaths(ctx, r)
	if err != nil {
		return assessFastLand(in, nil, false)
	}
	return assessFastLand(in, paths, true)
}

// assessFastLand applies the changed-path rule to a fast approval. A path read
// that failed (ok false) is not low risk: a fast landing needs the rule's facts,
// not a guess.
func assessFastLand(in ReviewResult, paths []string, ok bool) ReviewResult {
	if in.Verdict != state.VerdictApproveFast {
		return in
	}
	if !ok {
		return normalPath(in)
	}
	area, low := fastland.Classify(paths)
	if !low {
		return normalPath(in)
	}
	in.FastLand = &state.FastLand{LowRisk: true, Area: area, Note: "fast landing"}
	return in
}

// normalPath records a fast approval as a plain approval on the normal path.
func normalPath(in ReviewResult) ReviewResult {
	in.Verdict = state.VerdictApprove
	in.FastLand = &state.FastLand{Note: "normal path"}
	return in
}

// changedPaths reads the paths a change touches. The paths come from the change
// itself, so a reviewer cannot claim a low-risk area that the diff does not
// have.
func (s *Service) changedPaths(ctx context.Context, r Record) ([]string, error) {
	if changeKind(r) == "local" {
		base := r.BaseRef
		if base == "" {
			base = r.Base
		}
		if r.RepoPath == "" || r.Head == "" || base == "" {
			return nil, fmt.Errorf("the change has no base and head to diff")
		}
		out, err := gitRead(ctx, r.RepoPath, "diff", "--name-only", "--no-renames", "-z", base+"..."+r.Head)
		if err != nil {
			return nil, err
		}
		var paths []string
		for _, name := range strings.Split(out, "\x00") {
			if name = strings.TrimSpace(name); name != "" {
				paths = append(paths, name)
			}
		}
		return paths, nil
	}
	return s.github(r.RoomID).ChangedPaths(ctx, refRecord(r))
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
		ev.Area = string(in.FastLand.Area)
		ev.Note = in.FastLand.Note
	}
	return ev
}
