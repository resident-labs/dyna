package watch

import (
	"context"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/steward"
)

// A failed batch stays frozen until its steward or root chooses recovery. The pause
// is durable before report preparation, so a reporting error cannot start work.
func (s *Service) stopMergeBatch(ctx context.Context, repo string, r Record, b state.MergeTrial) error {
	if b.Phase == "awaiting-root" && b.Notified {
		return nil
	}
	b.Phase, b.Reason = "awaiting-root", "batch trial failed; merging paused for recovery"
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
		assigned := false
		cfg, cfgErr := settings.Load(settings.DefaultPath())
		if cfgErr == nil && cfg.MergeStewardProfile != settings.StewardProfileOff {
			svc := s.mergeSteward()
			assigned = svc.Assign(ctx, member.RoomID, member.Dir, cfg.MergeStewardProfile, trialIdentity(b), b.RootReport) == nil
		}
		if assigned {
			if b.Notifier == "" {
				b.Notifier = "steward"
			}
			continue
		}
		b.Notifier = "root"
		anchor := *member
		anchor.Head = b.Head
		if err := s.postMergeNotice(ctx, anchor, "merge-batch-failed/"+trialIdentity(b), Notice{NeedsRoot: true, Kind: "ci", Text: b.RootReport}); err != nil {
			return err
		}
	}
	b.Notified = true
	return s.saveMergeTrial(repo, b)
}

func (s *Service) mergeSteward() steward.Service {
	sessions := s.StewardSessions
	if sessions == nil && s.Notify != nil && s.Notify.Sessions != nil {
		sessions = steward.Live{Manager: s.Notify.Sessions, Rooms: s.Notify.Rooms}
	}
	return steward.Service{Sessions: sessions, Root: s.Store.Root, Now: s.Now}
}

// StewardProfile uses the same picker as the keeper and steward assignment.
// Off is not a profile: it reports the steward as off without a session manager.
func (s *Service) StewardProfile(pinned string) (string, string, error) {
	if pinned == settings.StewardProfileOff {
		return settings.StewardProfileOff, "", nil
	}
	return s.mergeSteward().Profile(pinned)
}
