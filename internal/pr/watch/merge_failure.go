package watch

import (
	"context"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
)

// mergePhaseHoldsBatchPause defines the phases that can own a batch-failure pause.
func mergePhaseHoldsBatchPause(phase state.MergeTrialPhase) bool {
	switch phase {
	case state.MergeTrialPhaseAwaitingRoot, state.MergeTrialPhaseFailed, state.MergeTrialPhaseStacking:
		return true
	default:
		return false
	}
}

// mergeFailCause classifies a failed trial's cause for the card. A failure that
// names its failing checks is the checks-failed kind: the checks list is the
// evidence, so the event carries no reason text that would repeat it. Any other
// cause keeps the steward's or root's own words.
func mergeFailCause(checks *room.MergeCheckSummary, reason string) (room.MergeReasonKind, string) {
	if checks != nil && len(checks.Failed) > 0 {
		return room.MergeChecksFailed, ""
	}
	if reason == "" {
		return "", ""
	}
	return room.MergeReasonOther, reason
}

// A failed batch stays frozen until root chooses recovery. The pause is durable
// before report preparation, so a reporting error cannot start work. The ci
// read carries the failure evidence; a batch that a restart resumes from carries
// none, and its saved report keeps the evidence the first failure recorded.
func (s *Service) stopMergeBatch(ctx context.Context, repo string, r Record, b state.MergeTrial, ci trialCI) error {
	if b.Phase == state.MergeTrialPhaseAwaitingRoot && b.Notified {
		return nil
	}
	if ci.Status == VerdictFailed {
		b.FailReason, b.FailChecks = ci.Reason, append([]string(nil), ci.Failed...)
	}
	b.Phase, b.Reason = state.MergeTrialPhaseAwaitingRoot, state.MergeBatchFailedReason
	if err := s.Store.update(func(st *State) error {
		if current := st.MergeTrials[repo]; current == nil || trialIdentity(*current) != trialIdentity(b) {
			return nil
		}
		if st.MergePauses == nil {
			st.MergePauses = map[string]state.MergePause{}
		}
		for _, change := range b.Changes {
			if member := st.Records[change.Key]; member != nil {
				st.MergePauses[member.RoomID] = state.MergePause{Cause: state.MergePauseCauseBatchFailed, Reason: b.Reason}
			}
		}
		st.MergeTrials[repo] = &b
		return nil
	}); err != nil {
		return err
	}
	if b.Notified {
		return nil
	}
	if b.RootReport == "" {
		text, err := s.mergeFailureReport(ctx, r, b, ci)
		if err != nil {
			return err
		}
		b.RootReport = text
		if err := s.saveMergeTrial(repo, b); err != nil {
			return err
		}
	}
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, change := range b.Changes {
		member := st.Records[change.Key]
		if member == nil || seen[member.RoomID] {
			continue
		}
		seen[member.RoomID] = true
		anchor := *member
		anchor.Head = b.Head
		ev := mergeTrialEvent(st, b, room.MergeFailed)
		ev.Repo, ev.Checks = mergeRepo(anchor), mergeChecksOf(ci)
		// A retry after a delivery error has no CI read of its own, so the typed
		// facts saved with the pause keep the cause and the failed checks.
		if ev.Checks == nil && len(b.FailChecks) > 0 {
			ev.Checks = &room.MergeCheckSummary{Failed: b.FailChecks}
		}
		reason := b.FailReason
		if reason == "" {
			reason = state.MergeBatchFailedReason
		}
		ev.ReasonKind, ev.Reason = mergeFailCause(ev.Checks, reason)
		if err := s.postMergeNotice(ctx, anchor, "merge-batch-failed/"+trialIdentity(b), Notice{NeedsRoot: true, Kind: "ci", Merge: &ev, Text: b.RootReport}); err != nil {
			return err
		}
	}
	b.Notified = true
	return s.saveMergeTrial(repo, b)
}
