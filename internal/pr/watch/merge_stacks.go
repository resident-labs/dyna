package watch

import (
	"context"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/trial"
	"dyna-agent/internal/room"
	"fmt"
	"slices"
)

func trialIdentity(b state.MergeTrial) string {
	if b.RootBranch != "" {
		return b.RootBranch
	}
	return b.Branch
}

// Prefix CI runs overlap on GitHub. Only the configured number may be active.
// The failed full batch is already the final prefix, so it never runs twice.
func (s *Service) advanceMergeStacks(ctx context.Context, repo string, r Record, b state.MergeTrial) error {
	if b.Phase == state.MergeTrialPhaseFailed {
		b.RootBranch, b.OriginalHead = b.Branch, b.Head
		b.StackChanges = append([]state.MergeTrialChange(nil), b.Changes...)

		if err := s.Store.update(func(st *State) error {
			for n := 1; n < len(b.Changes); n++ {
				st.NextMergeTrial++
				b.Stacks = append(b.Stacks, state.MergeTrialStack{Branch: fmt.Sprintf("dyna/merge-trial/%d", st.NextMergeTrial), Count: n, Phase: state.MergeTrialPhaseQueued})
			}
			return nil
		}); err != nil {
			return err
		}
		b.Stacks = append(b.Stacks, state.MergeTrialStack{Branch: b.RootBranch, Head: b.Head, Count: len(b.Changes), Phase: state.MergeTrialPhaseFailed, Logs: b.Logs})
		b.Phase, b.Reason = state.MergeTrialPhaseStacking, "testing batch prefixes"
		if err := s.saveMergeTrial(repo, b); err != nil {
			return err
		}
	}
	limit := 0
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	for _, change := range b.Changes {
		settings, err := s.MergeSettings(st.Records[change.Key].RoomID)
		if err != nil {
			return err
		}
		if limit == 0 || settings.MaxParallel < limit {
			limit = settings.MaxParallel
		}
	}
	b.MaxParallel = limit
	active := 0
	for i := range b.Stacks {
		stack := &b.Stacks[i]
		if stack.Phase != state.MergeTrialPhaseRunning {
			continue
		}
		ci, err := s.mergeTrialCI(ctx, r, stack.Branch, stack.Head)
		if err != nil {
			return err
		}
		stack.Reason = ci.Reason
		if ci.Status == "passed" || ci.Status == "failed" {
			stack.Phase = state.MergeTrialPhaseFailed
			if ci.Status == "passed" {
				stack.Phase = state.MergeTrialPhasePassed
			}
			stack.Logs = ci.Logs
			prefix := b
			prefix.Branch, prefix.Changes = stack.Branch, b.Changes[:stack.Count]
			s.noteMergeHistory(st, repo, prefix, "trial", ci.Status, stack.Head, ci.Logs)
		} else {
			active++
		}
	}
	if err := s.saveMergeTrial(repo, b); err != nil {
		return err
	}
	for i := range b.Stacks {
		if b.Stacks[i].Phase != state.MergeTrialPhaseQueued && b.Stacks[i].Phase != state.MergeTrialPhaseBuilding {
			continue
		}
		if active >= limit {
			break
		}
		b.Stacks[i].Phase = state.MergeTrialPhaseBuilding
		if err := s.saveMergeTrial(repo, b); err != nil {
			return err
		}
		prefix := b
		prefix.Branch = b.Stacks[i].Branch
		prefix.Changes = b.Changes[:b.Stacks[i].Count]
		result, err := s.trialClient(r).Build(ctx, trialBatch(r, prefix), func(result trial.Result) error {
			b.Stacks[i].Head = result.Head
			return s.saveMergeTrial(repo, b)
		})
		if err != nil {
			return err
		}
		if len(result.Included) != len(prefix.Changes) || len(result.Conflicts) != 0 {
			return s.discardMergeTrial(ctx, repo, r, b)
		}
		b.Stacks[i].Phase = state.MergeTrialPhaseRunning
		active++
		if err := s.saveMergeTrial(repo, b); err != nil {
			return err
		}
	}
	if active > 0 {
		return nil
	}
	longest := 0
	for _, stack := range b.Stacks {
		if stack.Phase == state.MergeTrialPhaseQueued || stack.Phase == state.MergeTrialPhaseBuilding {
			return nil
		}
		if stack.Phase == state.MergeTrialPhasePassed && stack.Count > longest {
			longest = stack.Count
		}
	}
	for _, stack := range b.Stacks {
		if stack.Count == longest+1 {
			b.Failure = &state.MergeTrialFailure{Change: b.Changes[longest], Reason: "merge trial CI failed; update this PR before it can join a new batch", Logs: stack.Logs}
			break
		}
	}
	if longest == 0 {
		if err := s.rejectTrialFailure(ctx, b); err != nil {
			return err
		}
		return s.discardMergeTrial(ctx, repo, r, b)
	}
	for _, stack := range b.Stacks {
		if stack.Count == longest {
			b.Branch, b.Head = stack.Branch, stack.Head
			break
		}
	}
	b.Changes = b.Changes[:longest]
	b.Phase, b.Reason = state.MergeTrialPhasePublishing, "publishing longest passing prefix"
	if err := s.saveMergeTrial(repo, b); err != nil {
		return err
	}
	return s.advanceMergeTrial(ctx, repo, b)
}

// Persist the rejection before delivering its notice. A retry uses the existing
// notice key, and ordinary PR polling cannot erase the combined-CI evidence.
func (s *Service) rejectTrialFailure(ctx context.Context, b state.MergeTrial) error {
	if b.Failure == nil {
		return nil
	}
	var member Record
	var prs []room.MergePR
	if err := s.Store.update(func(st *State) error {
		r := st.Records[b.Failure.Change.Key]
		if r == nil || r.Head != b.Failure.Change.Head {
			return nil
		}
		r.MergeTrialFailure = b.Failure
		member = *r
		prs = mergeTrialPRs(*st, b)
		return nil
	}); err != nil {
		return err
	}
	if member.Key == "" {
		return nil
	}
	// A published passing prefix no longer holds the member that failed, so the
	// trial's own list leaves it out. The event still names it: the card draws
	// the cause against that PR, and must not fall back to a member that merged.
	// A local change has no URL, so its title identifies it.
	listed := func(p room.MergePR) bool {
		if member.URL != "" {
			return p.URL == member.URL
		}
		return p.Title == member.Title
	}
	if !slices.ContainsFunc(prs, listed) {
		prs = append(prs, mergePR(member))
	}
	text := fmt.Sprintf("%s failed merge trial CI after the preceding tested prefix. %s\n%s", changeLocation(member), b.Failure.Reason, b.Failure.Logs)
	ev := room.MergeEvent{Type: room.MergeFailed, TrialID: trialIdentity(b), Repo: mergeRepo(member), Base: b.DefaultBranch, PRs: prs, Label: changeLabel(member), Member: member.Number, Reason: "failed trial CI after the tested prefix", ReasonKind: room.MergeReasonOther}
	return s.postMergeNotice(ctx, member, "merge-trial-failed", Notice{Target: member.Author, Merge: &ev, Text: text})
}
