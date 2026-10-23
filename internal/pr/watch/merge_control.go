package watch

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/locallane"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
)

type MergeControlStatus struct {
	LocalCIChecks   map[string][]CheckSummary `json:"localCIChecks,omitempty"`
	ExpeditedGoalID string                    `json:"expeditedGoalId,omitempty"`
	RoomID          string                    `json:"roomId"`
	Paused          bool                      `json:"paused"`
	Reason          string                    `json:"reason,omitempty"`
	Queue           []MergeQueueItem          `json:"queue"`
	Trials          []MergeTrialStatus        `json:"trials"`
}

type mergeControlRoom struct{}

func trialHasRoom(st State, b state.MergeTrial, roomID string) bool {
	for _, c := range b.Changes {
		if r := st.Records[c.Key]; r != nil && r.RoomID == roomID {
			return true
		}
	}
	return false
}

func (s *Service) MergeControlStatus(roomID string) (MergeControlStatus, error) {
	rm, err := s.Notify.Rooms.Resolve(roomID)
	if err != nil {
		return MergeControlStatus{}, err
	}
	st, err := s.Store.Read()
	if err != nil {
		return MergeControlStatus{}, err
	}
	queue, err := s.RoomMergeQueue(rm.ID)
	if err != nil {
		return MergeControlStatus{}, err
	}
	active, _, err := s.expeditedGoals()
	if err != nil {
		return MergeControlStatus{}, err
	}
	pause := st.MergePauses[rm.ID]
	out := MergeControlStatus{ExpeditedGoalID: active[rm.ID], RoomID: rm.ID, Paused: pause.Reason != "", Reason: pause.Reason, Queue: queue, Trials: []MergeTrialStatus{}}
	for _, repo := range controlRepositories(st, rm.ID) {
		out.Trials = append(out.Trials, *trialStatus(st, st.MergeTrials[repo]))
	}
	if err := s.mergeLocalCIChecks(context.Background(), st, rm.ID, &out); err != nil {
		return MergeControlStatus{}, err
	}
	return out, nil
}

func controlRepositories(st State, roomID string) []string {
	var repos []string
	for repo, b := range st.MergeTrials {
		if trialHasRoom(st, *b, roomID) {
			repos = append(repos, repo)
		}
	}
	sort.Strings(repos)
	return repos
}

func controlPR(st State, roomID, ref string) (Record, error) {
	var found *Record
	for _, r := range st.Records {
		if r.RoomID != roomID || changeKind(*r) != "github" || !r.Watching || r.State != "open" {
			continue
		}
		if ref == r.Key || ref == changeID(*r) || ref == r.URL || strings.TrimPrefix(ref, "#") == strconv.Itoa(r.Number) {
			if found != nil {
				return Record{}, fmt.Errorf("PR %q is ambiguous; use its URL", ref)
			}
			found = r
		}
	}
	if found == nil {
		return Record{}, fmt.Errorf("PR %q is not an open watch in this room", ref)
	}
	return *found, nil
}

func (s *Service) authorizeMergeControl(ctx context.Context, roomID, action string) (room.Record, error) {
	actor := authority.Of(ctx)
	if actor.Role == authority.Session {
		var err error
		actor, err = s.Notify.Sessions.AuthorityActor(ctx)
		if err != nil {
			return room.Record{}, err
		}
	} else if actor.Session != "" && actor.Role == authority.Root {
		// The engine resolved this role before the call. The stored session
		// can lose its role, move rooms or close while the publisher runs.
		var err error
		actor, err = s.sessionStore().SessionActor(actor.Session)
		if err != nil {
			return room.Record{}, err
		}
	}
	var rm room.Record
	var resolveErr error
	err := authority.Check("git merge "+action, actor, func(relation authority.Relation) bool {
		if relation != authority.RootRoom {
			return false
		}
		rm, resolveErr = s.Notify.Rooms.Resolve(roomID)
		return resolveErr == nil && (actor.Room == rm.ID || actor.Room == rm.Name)
	})
	if resolveErr != nil {
		return room.Record{}, resolveErr
	}
	if err != nil {
		return room.Record{}, err
	}
	// Root resolves the target in the relation callback. Owner needs no relation.
	if rm.ID == "" {
		rm, err = s.Notify.Rooms.Resolve(roomID)
		if err != nil {
			return room.Record{}, err
		}
	}
	if ghclient.Access(ctx, rm) != ghclient.ReadWrite {
		return room.Record{}, fmt.Errorf("room is read-only")
	}
	return rm, nil
}

// MergeControl changes only the existing worker state. Root and the worker
// share the publication lock, current PR facts, and tested-commit publisher.
// clearSettledPause resumes a room whose batch-failure pause has no frozen batch
// left to recover from, such as when root returned the only PR of a failed
// batch. Any other pause reason, and any batch still awaiting root, stays.
func (s *Service) clearSettledPause(roomID string) error {
	return s.Store.update(func(st *State) error {
		if st.MergePauses[roomID].Cause != state.MergePauseCauseBatchFailed {
			return nil
		}
		for _, b := range st.MergeTrials {
			if b == nil || !trialHasRoom(*st, *b, roomID) {
				continue
			}
			if mergePhaseHoldsBatchPause(b.Phase) {
				return nil
			}
		}
		delete(st.MergePauses, roomID)
		return nil
	})
}

func (s *Service) MergeControl(ctx context.Context, roomID, action string, args, without, flaky []string) error {
	if action == "note" {
		return s.mergeNote(ctx, roomID, args, without)
	}
	if len(flaky) > 0 && action != "publish" {
		return fmt.Errorf("--flaky applies only to merge publish")
	}
	rm, err := s.authorizeMergeControl(ctx, roomID, action)
	if err != nil {
		return err
	}
	want := 0
	switch action {
	case "pause", "resume", "cancel", "retry", "bisect":
	case "return":
		want = 2
	case "publish":
		want = 1
	default:
		return fmt.Errorf("unknown merge action %q", action)
	}
	if len(args) != want || (action != "retry" && len(without) != 0) {
		return fmt.Errorf("invalid arguments for merge %s", action)
	}
	if action == "return" && strings.TrimSpace(args[1]) == "" {
		return fmt.Errorf("return requires a reason")
	}
	unlock, err := s.mergeControlLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	// Waiting must not retain authority or room access that changed meanwhile.
	rm, err = s.authorizeMergeControl(ctx, roomID, action)
	if err != nil {
		return err
	}
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	repos := controlRepositories(st, rm.ID)
	setPaused := func(paused bool) error {
		actor := authority.Of(ctx)
		if paused && actor.Role == authority.Session {
			var err error
			actor, err = s.Notify.Sessions.AuthorityActor(ctx)
			if err != nil {
				return err
			}
		}
		return s.Store.update(func(st *State) error {
			if st.MergePauses == nil {
				st.MergePauses = map[string]state.MergePause{}
			}
			if paused {
				st.MergePauses[rm.ID] = state.MergePause{Cause: state.MergePauseCauseManual, Reason: "merging paused by " + string(actor.Role)}
			} else {
				delete(st.MergePauses, rm.ID)
			}
			return nil
		})
	}
	discard := func(repos []string) error {
		for _, repo := range repos {
			b := *st.MergeTrials[repo]
			r := st.Records[b.Changes[0].Key]
			if r == nil {
				return fmt.Errorf("batch lost its repository record")
			}
			if err := s.discardMergeTrial(ctx, repo, *r, b); err != nil {
				return err
			}
		}
		return nil
	}
	switch action {
	case "pause":
		return setPaused(true)
	case "resume":
		return setPaused(false)
	case "cancel":
		if len(repos) == 0 {
			return fmt.Errorf("room has no current batch")
		}
		return discard(repos)
	case "return":
		r, err := controlPR(st, rm.ID, args[0])
		if err != nil {
			return err
		}
		var affected []string
		var trial *state.MergeTrial
		logs := ""
		for _, repo := range repos {
			b := st.MergeTrials[repo]
			for _, c := range b.Changes {
				if c.Key == r.Key {
					affected = append(affected, repo)
					trial = b
					logs = b.RootReport
					if logs == "" {
						logs = fmt.Sprintf("Trial CI evidence: https://%s/%s/commit/%s/checks\n%s", r.Host, r.Repo, b.Head, b.Logs)
					}
					break
				}
			}
		}
		reason := Sanitize(strings.TrimSpace(args[1]))
		if err := s.Store.update(func(st *State) error {
			live := st.Records[r.Key]
			if live == nil || live.Head != r.Head {
				return fmt.Errorf("PR head changed")
			}
			live.MergeTrialFailure = &state.MergeTrialFailure{Change: state.MergeTrialChange{Key: r.Key, Head: r.Head}, Reason: reason, Logs: logs}
			return nil
		}); err != nil {
			return err
		}
		ev := mergeEventOf(r, room.MergeReturned)
		if ev.Base == "" {
			// A local change merges into the integration branch.
			ev.Base = locallane.Integration
		}
		ev.Reason, ev.ReasonKind = reason, room.MergeRootReturn
		ev.Member = r.Number
		if trial != nil {
			ev = mergeTrialEvent(st, *trial, room.MergeReturned)
			ev.Label, ev.Reason, ev.ReasonKind = changeLabel(r), reason, room.MergeRootReturn
			ev.Member = r.Number
		}
		if err := s.postMergeNotice(ctx, r, "merge-root-return/"+digest(reason), Notice{Kind: "ci", Target: r.Author, Merge: &ev, Text: fmt.Sprintf("%s returned by root at %s. %s\n%s", r.URL, r.Head, reason, logs)}); err != nil {
			return err
		}
		if err := discard(affected); err != nil {
			return err
		}
		// A return can leave the room with no failed batch to recover from, and
		// then its batch-failure pause has nothing left to hold.
		return s.clearSettledPause(rm.ID)
	case "retry":
		excluded := map[string]bool{}
		for _, ref := range without {
			r, err := controlPR(st, rm.ID, ref)
			if err != nil {
				return err
			}
			excluded[r.Key] = true
		}
		if err := discard(repos); err != nil {
			return err
		}
		// Bypass the existing pause while preparing the replacement. The
		// batch checkpoint clears it atomically before any build can start.
		ctx = context.WithValue(ctx, mergeControlRoom{}, rm.ID)
		if _, err := s.mergeTickLocked(ctx, rm.ID, excluded); err != nil {
			return err
		}
		current, err := s.Store.Read()
		if err != nil {
			return err
		}
		if len(controlRepositories(current, rm.ID)) == 0 {
			return fmt.Errorf("no eligible PRs for this retry")
		}
		return nil
	case "bisect":
		if len(repos) == 0 {
			return fmt.Errorf("room has no failed batch")
		}
		for _, repo := range repos {
			b := st.MergeTrials[repo]
			if b.Phase != state.MergeTrialPhaseAwaitingRoot {
				return fmt.Errorf("batch %s has not failed", b.Branch)
			}
		}
		return s.Store.update(func(st *State) error {
			for _, repo := range repos {
				b := st.MergeTrials[repo]
				b.Bisect = true
				b.Phase = state.MergeTrialPhaseFailed
				if len(b.Stacks) > 0 {
					b.Phase = state.MergeTrialPhaseStacking
				}
				b.Reason = "root requested prefix trials"
			}
			delete(st.MergePauses, rm.ID)
			return nil
		})
	case "publish":
		tests, err := ParseFlakyTests(flaky)
		if err != nil {
			return err
		}
		return s.publishControlledTrial(ctx, rm.ID, st, repos, args[0], tests)
	}
	return nil
}

func (s *Service) publishControlledTrial(ctx context.Context, roomID string, st State, repos []string, name string, flaky []FlakyTest) error {
	if paused, err := s.RoomPaused(roomID); err != nil {
		return err
	} else if paused {
		return fmt.Errorf("room sessions are paused")
	}
	for _, repo := range repos {
		original := *st.MergeTrials[repo]
		b := original
		matched := b.Branch == name || strings.TrimPrefix(b.Branch, "dyna/merge-trial/") == name
		if !matched {
			allChanges := b.Changes
			if len(b.StackChanges) > 0 {
				allChanges = b.StackChanges
			}
			for _, stack := range b.Stacks {
				if stack.Branch == name || strings.TrimPrefix(stack.Branch, "dyna/merge-trial/") == name {
					b.Branch, b.Head = stack.Branch, stack.Head
					if stack.Count <= 0 || stack.Count > len(allChanges) {
						return fmt.Errorf("invalid saved prefix size")
					}
					b.Changes = allChanges[:stack.Count]
					matched = true
					break
				}
			}
		}
		if !matched {
			continue
		}
		if original.Phase == state.MergeTrialPhasePublished && original.Branch == b.Branch {
			return fmt.Errorf("trial %s already published", b.Branch)
		}
		if b.Head == "" {
			return fmt.Errorf("trial has no tested commit")
		}
		r := st.Records[b.Changes[0].Key]
		if r == nil {
			return fmt.Errorf("trial lost its repository record")
		}
		if len(flaky) > 0 {
			// Root names every failing test before a failed trial may merge.
			if b.Phase != state.MergeTrialPhaseFailed && b.Phase != state.MergeTrialPhaseAwaitingRoot {
				return fmt.Errorf("trial %s has not failed; publish it without --flaky", b.Branch)
			}
			if err := flakyCoverage(b.Logs, flaky); err != nil {
				return err
			}
		} else {
			ci, err := s.mergeTrialCI(ctx, *r, b.Branch, b.Head)
			if err != nil {
				return err
			}
			if ci.Status != "passed" {
				return fmt.Errorf("trial push CI has not passed")
			}
		}
		probes, err := s.observeHeadBatch(ctx, st, []*Record{r}, nil)
		if err != nil {
			return err
		}
		probe, ok := probes[r.Key]
		if !ok || probe.Err != nil || probe.Default.Head == "" {
			return fmt.Errorf("current main could not be confirmed")
		}
		if probe.Default.Name != b.DefaultBranch {
			return fmt.Errorf("default branch changed; retry the batch")
		}
		b.Base = probe.Default.Head
		if b.RootBranch == "" {
			b.RootBranch, b.OriginalHead = original.Branch, original.Head
		}
		if len(b.StackChanges) == 0 {
			b.StackChanges = original.Changes
		}
		b.Failure = nil
		b.Phase, b.Reason = state.MergeTrialPhasePublishing, "root requested publication of passed trial"
		if len(flaky) > 0 {
			b.Reason = "root published a failed trial for known flaky tests"
		}
		if err := s.saveMergeTrial(repo, b); err != nil {
			return err
		}
		// A normal publication preserves the room pause and still enforces room
		// access, session pause, fresh PR gates, ancestry and the main lease. A
		// flaky publication clears the pause only after it merges.
		publishCtx := context.WithValue(ctx, mergeControlRoom{}, roomID)
		if len(flaky) > 0 {
			publishCtx = withFlakyPublish(publishCtx, flaky)
		}
		if err := s.advanceMergeTrial(publishCtx, repo, b); err != nil {
			return err
		}
		if len(flaky) > 0 {
			return s.finishFlakyPublish(ctx, roomID, repo, b, flaky)
		}
		return nil
	}
	return fmt.Errorf("trial %q is not saved for this room", name)
}
