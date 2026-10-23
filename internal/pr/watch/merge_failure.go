package watch

import (
	"context"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
)

// mergeBatchFailedReason is the merge pause that a failed batch sets.
const mergeBatchFailedReason = "batch trial failed; merging paused for recovery"

// A failed batch stays frozen until root chooses recovery. The pause is durable
// before report preparation, so a reporting error cannot start work.
func (s *Service) stopMergeBatch(ctx context.Context, repo string, r Record, b state.MergeTrial, checks *room.MergeCheckSummary) error {
	if b.Phase == "awaiting-root" && b.Notified {
		return nil
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
		text, err := s.mergeFailureReport(ctx, r, b)
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
		ev.Repo, ev.Reason, ev.Checks = mergeRepo(anchor), mergeBatchFailedReason, checks
		if err := s.postMergeNotice(ctx, anchor, "merge-batch-failed/"+trialIdentity(b), Notice{NeedsRoot: true, Kind: "ci", Merge: &ev, Text: b.RootReport}); err != nil {
			return err
		}
	}
	b.Notified = true
	return s.saveMergeTrial(repo, b)
}
