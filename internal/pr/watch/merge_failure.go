package watch

import (
	"context"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
)

// mergeBatchFailedReason is the merge pause that a failed batch sets.
const mergeBatchFailedReason = "batch trial failed; merging paused for recovery"

// A failed batch stays frozen until root chooses recovery. The pause is durable
// before report preparation, so a reporting error cannot start work. The ci
// read carries the failure evidence; a batch that a restart resumes from carries
// none, and its saved report keeps the evidence the first failure recorded.
func (s *Service) stopMergeBatch(ctx context.Context, repo string, r Record, b state.MergeTrial, ci trialCI) error {
	if b.Phase == "awaiting-root" && b.Notified {
		return nil
	}
	if ci.Status == "failed" {
		b.FailReason, b.FailChecks = ci.Reason, append([]string(nil), ci.Failed...)
	}
	b.Phase, b.Reason = "awaiting-root", mergeBatchFailedReason
	if err := s.Store.update(func(st *State) error {
		if current := st.MergeTrials[repo]; current == nil || trialIdentity(*current) != trialIdentity(b) {
			return nil
		}
		if st.MergePauses == nil {
			st.MergePauses = map[string]string{}
		}
		for _, change := range b.Changes {
			if member := st.Records[change.Key]; member != nil {
				st.MergePauses[member.RoomID] = b.Reason
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
		ev.Reason = mergeBatchFailedReason
		if b.FailReason != "" {
			ev.Reason = b.FailReason
		}
		if err := s.postMergeNotice(ctx, anchor, "merge-batch-failed/"+trialIdentity(b), Notice{NeedsRoot: true, Kind: "ci", Merge: &ev, Text: b.RootReport}); err != nil {
			return err
		}
	}
	b.Notified = true
	return s.saveMergeTrial(repo, b)
}
