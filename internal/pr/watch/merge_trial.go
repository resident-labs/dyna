package watch

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/gitexec"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/locallane"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/trial"
	"dyna-agent/internal/room"
)

// MergeTick uses the existing PR facts and publication lock. Every automatic
// publication now advances main to a CI-tested batch commit. It returns when
// it is next due: one poll later while a merge trial runs or the merge queue
// has a PR, or zero. A PR change wakes it by its store event.
func (s *Service) MergeTick(ctx context.Context) (time.Time, error) {
	unlock, err := s.mergeLock()
	if err != nil {
		return s.now().Add(pollEvery), err
	}
	defer unlock()
	return s.mergeTickLocked(locallane.WithCommonDirs(ctx), "", nil)
}

// Root retries use the worker's queue and batch builder under the same lock.
// It returns its next due time, as MergeTick does.
func (s *Service) mergeTickLocked(ctx context.Context, roomID string, without map[string]bool) (time.Time, error) {
	retry := func(err error) (time.Time, error) { return s.now().Add(pollEvery), err }
	probes, refreshErr := s.refreshMergeFacts(ctx)
	st, err := s.Store.Read()
	if err != nil {
		return retry(err)
	}
	failures := []error{refreshErr}
	if err := s.scheduleRegisteredCI(ctx, st); err != nil {
		failures = append(failures, err)
	}
	if _, err := s.refreshRepositoryFacts(ctx, st); err != nil {
		failures = append(failures, err)
	}
	st, err = s.Store.Read()
	if err != nil {
		return retry(err)
	}
	// An explicit retry refreshes this room's full PR facts before using the
	// same queue builder. The background worker keeps its normal poll schedule.
	if roomID != "" {
		for key, probe := range probes {
			r := st.Records[key]
			if r == nil || r.RoomID != roomID || without[key] || probe.Err != nil || !currentTrialMember(r, state.MergeTrialChange{Key: key, Head: probe.Pull.Head.SHA}) {
				continue
			}
			snap, err := s.github(r.RoomID).snapshotFromPull(ctx, refRecord(*r), probe.Pull, nil)
			if err != nil {
				return retry(err)
			}
			snap.ciProbe = probe.CI
			snap, err = s.withRegisteredCI(ctx, *r, snap, EffectivePolicy(st, *r))
			if err != nil {
				return retry(err)
			}
			ci, key, jobs, _ := s.ciStatus(snap, *r, EffectivePolicy(st, *r))
			if err := s.process(ctx, r.Key, snap, ci, key, jobs, nil, false); err != nil {
				return retry(err)
			}
		}
		st, err = s.Store.Read()
		if err != nil {
			return retry(err)
		}
	}
	seen := map[string]bool{}
	var repos []string
	for repo := range st.MergeTrials {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	// The pass is due again while a merge trial runs or the queue has a PR.
	var next time.Time
	for _, repo := range repos {
		next = s.now().Add(pollEvery)
		if roomID != "" && !trialHasRoom(st, *st.MergeTrials[repo], roomID) {
			continue
		}
		seen[repo] = true
		if err := s.confirmTrialProbes(st, probes, *st.MergeTrials[repo]); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := s.advanceMergeTrial(ctx, repo, *st.MergeTrials[repo]); err != nil {
			failures = append(failures, err)
		}
	}
	st, err = s.Store.Read()
	if err != nil {
		return retry(errors.Join(append(failures, err)...))
	}
	groups := map[string][]Record{}
	// A fast landing publishes its PR on this tick, so the repository takes no
	// trial until the next tick reads the new default-branch head.
	fastLanded := map[string]bool{}
	repos = nil
	// An explicit retry may select a replacement while the durable pause
	// remains in place. Only saving that replacement clears the pause.
	if roomID != "" {
		delete(st.MergePauses, roomID)
	}
	for _, item := range mergeQueue(st) {
		next = s.now().Add(pollEvery)
		if item.State != "ready" {
			continue
		}
		r := *recordByID(&st, item.ChangeID)
		if (roomID != "" && r.RoomID != roomID) || without[r.Key] {
			continue
		}
		repo := defaultRepo(r)
		if seen[repo] {
			continue
		}
		if why, err := s.trialRoomHold(ctx, r); err != nil {
			failures = append(failures, err)
			continue
		} else if why != "" {
			continue
		}
		if !st.RepositoryFacts[repo].MergeTrialCI {
			continue
		}
		current, ready, err := s.mergeReady(ctx, r)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !ready {
			continue
		}
		if fastLandable(current) {
			if err := s.fastLand(ctx, current); err != nil {
				failures = append(failures, err)
			} else {
				fastLanded[repo] = true
			}
			continue
		}
		if len(groups[repo]) == 0 {
			repos = append(repos, repo)
		}
		groups[repo] = append(groups[repo], current)
	}
	active, _, err := s.expeditedGoals()
	if err != nil {
		return retry(err)
	}
	for _, repo := range repos {
		if fastLanded[repo] {
			continue
		}
		groups[repo] = chooseMergeBatch(groups[repo], active, s.expediteStreak("merge:"+repo))
		limit := 0
		for _, member := range groups[repo] {
			settings, err := s.MergeSettings(member.RoomID)
			if err != nil {
				return retry(err)
			}
			if limit == 0 || settings.MaxParallel < limit {
				limit = settings.MaxParallel
			}
		}
		var batch state.MergeTrial
		if err := s.Store.update(func(st *State) error {
			if st.MergeTrials == nil {
				st.MergeTrials = map[string]*state.MergeTrial{}
			}
			if st.MergeTrials[repo] != nil {
				return errors.New("another trial already owns this repository")
			}
			base := st.RepositoryFacts[repo]
			if base.Head == "" || base.DefaultBranch == "" || !base.MergeTrialCI {
				return errors.New("merge-trial push CI or default branch could not be confirmed")
			}
			st.NextMergeTrial++
			batch = state.MergeTrial{Branch: "dyna/merge-trial/" + strconv.FormatUint(st.NextMergeTrial, 10), Base: base.Head, DefaultBranch: base.DefaultBranch, Phase: "building", Created: s.now(), MaxParallel: limit}
			for _, r := range groups[repo] {
				batch.Changes = append(batch.Changes, trialChange(r))
			}
			st.MergeTrials[repo] = &batch
			if roomID != "" {
				delete(st.MergePauses, roomID)
			}
			return nil
		}); err != nil {
			failures = append(failures, err)
			continue
		}
		s.expediteGranted("merge:"+repo, expeditedRecord(groups[repo][0], active))
		s.noteTrialEvent(ctx, st, batch, mergeTrialEvent(st, batch, room.MergeQueued))
		if err := s.advanceMergeTrial(ctx, repo, batch); err != nil {
			failures = append(failures, err)
		}
	}
	if err := errors.Join(failures...); err != nil {
		return retry(err)
	}
	return next, nil
}

func (s *Service) trialRoomHold(ctx context.Context, r Record) (string, error) {
	if changeKind(r) == "github" {
		local, err := recordLocalLane(ctx, r)
		if err != nil {
			return "", err
		}
		if local {
			return "repository uses the local merge lane", nil
		}
	}

	if changeKind(r) == "local" {
		enabled, err := locallane.Enabled(ctx, r.RepoPath)
		if err != nil {
			return "", err
		}
		if !enabled {
			return "repository is not in the local lane", nil
		}
	}
	st, err := s.Store.Read()
	if err != nil {
		return "", err
	}
	if why := st.MergePauses[r.RoomID]; why != "" && ctx.Value(mergeControlRoom{}) != r.RoomID {
		return why, nil
	}
	if paused, err := s.RoomPaused(r.RoomID); err != nil {
		return "", err
	} else if paused {
		return "room sessions are paused", nil
	}
	if s.Notify == nil || s.Notify.Rooms == nil {
		return "", errors.New("merge trials require the room store")
	}
	rm, err := s.Notify.Rooms.Resolve(r.RoomID)
	if err != nil {
		return "", err
	}
	if ghclient.Access(ctx, rm) != ghclient.ReadWrite {
		return "room is read-only", nil
	}
	return "", nil
}

func (s *Service) trialClient(r Record) trial.Client {
	if changeKind(r) == "local" {
		return trial.Client{Scratch: filepath.Join(s.Store.Root, "trials"), Run: func(ctx context.Context, cmd *exec.Cmd) error {
			clean := gitexec.Command(ctx, cmd.Dir, cmd.Args[1:]...)
			// Preserve the trial builder's fixed identity, dates, and isolated Git config.
			for _, value := range cmd.Env {
				if strings.HasPrefix(value, "GIT_AUTHOR_") || strings.HasPrefix(value, "GIT_COMMITTER_") || strings.HasPrefix(value, "GIT_CONFIG_") {
					clean.Env = append(clean.Env, value)
				}
			}
			clean.Stdin, clean.Stdout, clean.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr
			return clean.Run()
		}}
	}
	g := s.github(r.RoomID)
	return trial.Client{Scratch: filepath.Join(s.Store.Root, "trials"), Run: func(ctx context.Context, cmd *exec.Cmd) error {
		ctx = ghclient.WithRepository(ghclient.WithRoom(ghclient.WithRooms(ctx, g.Rooms), r.RoomID), r.Host, r.Repo)
		if g.run != nil {
			return g.run(ctx, cmd)
		}
		return ghclient.RunGit(ctx, g.Executable, cmd)
	}}
}

func trialBatch(r Record, b state.MergeTrial) trial.Batch {
	batch := trial.Batch{Remote: "https://" + r.Host + "/" + r.Repo + ".git", DefaultBranch: b.DefaultBranch, Base: b.Base, Branch: b.Branch, Created: b.Created}
	if changeKind(r) == "local" {
		batch.Remote = r.RepoPath
	}
	for _, change := range b.Changes {
		batch.Heads = append(batch.Heads, change.Head)
		if change.Message != "" {
			if batch.Messages == nil {
				batch.Messages = map[string]string{}
			}
			batch.Messages[change.Head] = change.Message
		}
	}
	return batch
}

// The message is frozen with the head, so a rebuilt trial gets the same commit.
func trialChange(r Record) state.MergeTrialChange {
	return state.MergeTrialChange{Key: r.Key, Head: r.Head, Message: trial.MergeMessage(r.Number, r.Title, r.Head)}
}

func (s *Service) saveMergeTrial(repo string, b state.MergeTrial) error {
	return s.Store.update(func(st *State) error {
		if current := st.MergeTrials[repo]; current == nil || trialIdentity(*current) != trialIdentity(b) {
			return errors.New("merge trial changed during reconciliation")
		}
		st.MergeTrials[repo] = &b
		return nil
	})
}

func (s *Service) discardMergeTrial(ctx context.Context, repo string, r Record, b state.MergeTrial) error {
	if err := s.clearLegacyMergeReview(ctx, &b); err != nil {
		return err
	}

	refs := map[string]string{b.Branch: b.Head}
	if b.RootBranch != "" {
		refs[b.RootBranch] = b.OriginalHead
	}
	for _, stack := range b.Stacks {
		refs[stack.Branch] = stack.Head
	}
	for branch, head := range refs {
		if head == "" {
			continue
		}
		cleanup := b
		cleanup.Branch = branch
		if err := s.trialClient(r).Delete(ctx, trialBatch(r, cleanup), head); err != nil {
			return err
		}
	}
	return s.Store.update(func(st *State) error {
		if current := st.MergeTrials[repo]; current != nil && trialIdentity(*current) == trialIdentity(b) {
			delete(st.MergeTrials, repo)
		}
		return nil
	})
}

func (s *Service) advanceMergeTrial(ctx context.Context, repo string, b state.MergeTrial) error {
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	if len(b.Changes) == 0 {
		return errors.New("merge trial has no frozen PR heads")
	}
	r := st.Records[b.Changes[0].Key]
	if r == nil {
		return errors.New("merge trial lost its tracked repository")
	}
	// TODO: legacy/shim remove later — resume batches saved by the advisory-review engine; remove after all pre-upgrade batches are gone.
	if b.Phase == "reviewing" || b.LegacyReview != nil {
		if err := s.clearLegacyMergeReview(ctx, &b); err != nil {
			return err
		}
		if err := s.saveMergeTrial(repo, b); err != nil {
			return err
		}
	}
	if b.Phase == "awaiting-root" || ((b.Phase == "failed" || b.Phase == "stacking") && !b.Bisect) {
		return s.stopMergeBatch(ctx, repo, *r, b, trialCI{})
	}
	// The access and pause checks also apply to a resumed publishing attempt.
	for _, change := range b.Changes {
		member := st.Records[change.Key]
		if member == nil {
			return errors.New("merge trial lost a tracked PR")
		}
		if why, err := s.trialRoomHold(ctx, *member); err != nil {
			return err
		} else if why != "" {
			return nil
		}
	}
	if b.Phase != "publishing" && b.Phase != "published" {
		if st.RepositoryFacts[repo].Head != b.Base {
			return s.discardMergeTrial(ctx, repo, *r, b)
		}
		for _, change := range b.Changes {
			member := st.Records[change.Key]
			if !currentTrialMember(member, change) {
				return s.discardMergeTrial(ctx, repo, *r, b)
			}
		}
	}
	client := s.trialClient(*r)
	batch := trialBatch(*r, b)
	switch b.Phase {
	case "building":
		result, err := client.Build(ctx, batch, func(result trial.Result) error { b.Head = result.Head; return s.saveMergeTrial(repo, b) })
		if err != nil {
			b.Reason = err.Error()
			return errors.Join(err, s.saveMergeTrial(repo, b))
		}
		var included []state.MergeTrialChange
		for _, change := range b.Changes {
			if slices.Contains(result.Included, change.Head) {
				included = append(included, change)
				continue
			}
			if slices.Contains(result.Conflicts, change.Head) {
				member := *st.Records[change.Key]
				text := fmt.Sprintf("%s conflicts with another PR in merge trial %s. Inspect the overlapping changes before this PR can join a batch.", changeLocation(member), b.Branch)
				ev := mergeTrialEvent(st, b, room.MergeConflict)
				ev.Label, ev.Reason = changeLabel(member), "conflicts with another PR in the trial"
				if err := s.postMergeNotice(ctx, member, "merge-trial-conflict", Notice{Target: member.Author, Merge: &ev, Text: text}); err != nil {
					return err
				}
			}
		}
		if len(included) == 0 {
			return s.discardMergeTrial(ctx, repo, *r, b)
		}
		b.Changes, b.Head, b.Phase, b.Reason = included, result.Head, "running", ""
		if err := s.saveMergeTrial(repo, b); err != nil {
			return err
		}
		s.noteTrialEvent(ctx, st, b, mergeTrialEvent(st, b, room.MergeTrial))
		return nil
	case "running":
		ci, err := s.mergeTrialCI(ctx, *r, b.Branch, b.Head)
		if err != nil {
			return err
		}
		if ci.Status == "failed" {
			b.Phase, b.Reason, b.Logs = "failed", "batch trial failed; waiting for root", ci.Logs
			s.noteMergeHistory(st, repo, b, "trial", "failed", b.Head, ci.Logs)
			return s.stopMergeBatch(ctx, repo, *r, b, ci)
		}
		if ci.Status != "passed" {
			if b.Reason != ci.Reason {
				b.Reason = ci.Reason
				if err := s.saveMergeTrial(repo, b); err != nil {
					return err
				}
				// A new count is one progress step of the trial's card. This is a
				// fact the CI read already gave, so it costs no extra call.
				if ci.Total > 0 {
					s.noteTrialEvent(ctx, st, b, mergeChecksEvent(st, b, ci))
				}
			}
			return nil
		}
		s.noteMergeHistory(st, repo, b, "trial", "passed", b.Head, "")
		b.Reason = ""
		for _, change := range b.Changes {
			member := *st.Records[change.Key]
			if member.Head != change.Head {
				return s.discardMergeTrial(ctx, repo, *r, b)
			}
			if _, ready, err := s.mergeReady(ctx, member); err != nil {
				return err
			} else if !ready {
				return s.discardMergeTrial(ctx, repo, *r, b)
			}
		}
		b.Phase = "publishing"
		if err := s.saveMergeTrial(repo, b); err != nil {
			return err
		}
		return s.advanceMergeTrial(ctx, repo, b)
	case "publishing":
		if st.RepositoryFacts[repo].Head != b.Head {
			ci, err := s.mergeTrialCI(ctx, *r, b.Branch, b.Head)
			if err != nil {
				return err
			}
			logs := ci.Logs
			flaky := flakyPublicationAllowed(ctx, logs)
			if ci.Status == "failed" && !flaky {
				// Record the prefix before it is reset to the full batch.
				s.noteMergeHistory(st, repo, b, "trial", "failed", b.Head, logs)
				if len(b.StackChanges) > 0 {
					for i := range b.Stacks {
						if b.Stacks[i].Branch == b.Branch {
							b.Stacks[i].Phase, b.Stacks[i].Logs = "failed", logs
						}
					}
					b.Changes, b.Branch, b.Head = b.StackChanges, b.RootBranch, b.OriginalHead
					b.Phase, b.Reason = "stacking", "selected prefix CI changed; choosing a passing prefix"
					b.Failure = nil
				} else {
					b.Phase, b.Reason, b.Logs = "failed", "batch trial failed; waiting for root", logs
				}
				if !b.Bisect {
					return s.stopMergeBatch(ctx, repo, *r, b, ci)
				}
				return s.saveMergeTrial(repo, b)
			}
			if ci.Status != "passed" && !flaky {
				b.Reason = ci.Reason
				return s.saveMergeTrial(repo, b)
			}

			for _, change := range b.Changes {
				member := *st.Records[change.Key]
				if member.Head != change.Head {
					return s.discardMergeTrial(ctx, repo, *r, b)
				}
				if _, ready, err := s.mergeReady(ctx, member); err != nil {
					return err
				} else if !ready {
					return s.discardMergeTrial(ctx, repo, *r, b)
				}
			}
		}
		if err := client.Publish(ctx, batch, b.Head); err != nil {
			if errors.Is(err, trial.ErrMainMoved) {
				return s.discardMergeTrial(ctx, repo, *r, b)
			}
			b.Reason = err.Error()
			return errors.Join(err, s.saveMergeTrial(repo, b))
		}
		s.noteMergeHistory(st, repo, b, "publish", "published", b.Head, "")
		landed := mergeTrialEvent(st, b, room.MergeLanded)
		landed.Commit = b.Head
		s.noteTrialEvent(ctx, st, b, landed)
		b.Phase = "published"
		if err := s.saveMergeTrial(repo, b); err != nil {
			return err
		}
		return s.advanceMergeTrial(ctx, repo, b)
	case "published":
		if err := s.rejectTrialFailure(ctx, b); err != nil {
			return err
		}
		var followups []error
		for _, change := range b.Changes {
			member := *st.Records[change.Key]
			if changeKind(member) == "local" {
				if err := s.publishLocalChange(ctx, member, change.Head, trialIdentity(b)); err != nil {
					return err
				}
				continue
			}
			pull, err := s.github(member.RoomID).Pull(ctx, refRecord(member))
			if err != nil {
				return err
			}
			if pull.Head.SHA != change.Head {
				continue
			}
			if !pull.Merged {
				return nil
			}
			processErr := s.process(ctx, member.Key, Snapshot{Pull: pull, headConfirmed: true}, "", "", nil, nil, false)
			recorded, readErr := s.mergeRecorded(member)
			if readErr != nil || !recorded {
				return errors.Join(processErr, readErr)
			}
			if processErr != nil {
				followups = append(followups, processErr)
			}
		}
		if changeKind(*r) == "local" {
			if err := hooks.New().Emit(ctx, hooks.Event{Event: hooks.PRMerged, Repo: r.RepoPath, Worktree: r.RepoPath, Branch: locallane.Integration, SHA: b.Head, Room: r.RoomID}); err != nil {
				return err
			}
		}
		return errors.Join(append(followups, s.discardMergeTrial(ctx, repo, *r, b))...)
	case "failed", "stacking":
		return s.advanceMergeStacks(ctx, repo, *r, b)
	default:
		return fmt.Errorf("unknown merge trial phase %q", b.Phase)
	}
}

// trialCI is what the steward observed about a merge trial's CI: its status,
// the logs of a failure, the reason it shows while it waits, and the check
// counts it already read. It costs no extra GitHub call.
type trialCI struct {
	Status string
	Logs   string
	Reason string
	// Done counts the checks that finished, and Total counts them all. Both
	// are zero while the trial branch has no started run.
	Done   int
	Total  int
	Failed []string
	// Runs counts the failed completed runs of a failed read. A run can fail
	// without a named check, so the count is evidence of its own.
	Runs int
	// Cancelled reports a cancelled check or run. That CI never tested the
	// commit, so its failure is an infrastructure cause, not a test failure.
	Cancelled bool
}

// mergeTrialCI accepts only push evidence for the frozen trial branch and head.
// Missing or queued runs do not prove that the trial suite ran.
func (s *Service) mergeTrialCI(ctx context.Context, r Record, branch, head string) (trialCI, error) {
	if changeKind(r) == "local" {
		return s.localTrialCI(ctx, r, head)
	}
	var snap Snapshot
	if err := s.github(r.RoomID).readCommitCI(ctx, refRecord(r), head, &snap); err != nil {
		return trialCI{}, err
	}
	var pushes []Run
	started := false
	for _, run := range snap.Runs {
		if run.Event != "push" || run.HeadSHA != head || run.HeadBranch != branch {
			continue
		}
		pushes = append(pushes, run)
		if run.Status == "in_progress" || run.Status == "completed" {
			started = true
		}
	}
	snap.Runs = pushes
	snap.Pull.Head.SHA = head
	snap.Pull.Head.Repo.FullName = r.Repo
	trialRecord := r
	trialRecord.Head = head
	trialRecord.Branch = branch
	st, err := s.Store.Read()
	if err != nil {
		return trialCI{}, err
	}
	policy := EffectivePolicy(st, r)
	snap, err = s.withRegisteredCI(ctx, trialRecord, snap, policy)
	if err != nil {
		return trialCI{}, err
	}
	if snap.localCI != nil {
		if !conclusiveCI(snap.localCI.Status) {
			return trialCI{Status: "pending", Reason: snap.localCI.Reason}, nil
		}
		// The failed jobs of a hosted read are the evidence a failure records.
		return withTrialEvidence(r, head, trialCI{
			Status:    snap.localCI.Status,
			Logs:      snap.localCI.Logs,
			Failed:    snap.localCI.Jobs,
			Runs:      len(snap.localCI.Runs),
			Cancelled: snap.ciCancelled(policy.RequiredChecks...),
		}), nil
	}
	out := trialCI{Status: "pending"}
	var failed []Run
	if started {
		out.Status, _, out.Failed, failed = snap.CIStatus()
	}
	if out.Status == "pending" {
		total, done, active := 0, 0, false
		for _, run := range currentRuns(pushes) {
			jobs, err := s.github(r.RoomID).runJobs(ctx, refRecord(r), run)
			if err != nil {
				return trialCI{}, err
			}
			total += len(jobs)
			for _, job := range jobs {
				if job.Status == "completed" {
					done++
				}
				if job.Status == "completed" || job.Status == "in_progress" {
					active = true
				}
			}
		}
		out.Done, out.Total = done, total
		out.Reason = "waiting for push CI to start"
		if active {
			out.Reason = fmt.Sprintf("running (%d of %d jobs done)", done, total)
		}
		return out, nil
	}
	if out.Status != "failed" {
		out.Failed = nil
	}
	if out.Status == "failed" {
		out.Runs, out.Cancelled = len(failed), snap.ciCancelled(policy.RequiredChecks...)
		for _, run := range failed {
			out.Logs += s.github(r.RoomID).Logs(ctx, refRecord(r), run) + "\n"
		}
		out = withTrialEvidence(r, head, out)
	}
	return out, nil
}

// withTrialEvidence makes every failed read name what it observed, so a batch
// never fails without a cause. A read that observed nothing keeps an unknown
// cause instead of guessing one: a failed run without a named check is a test
// failure, and only an observed cancellation is an infrastructure cause.
func withTrialEvidence(r Record, head string, ci trialCI) trialCI {
	if ci.Status != "failed" {
		return ci
	}
	if strings.TrimSpace(ci.Logs) == "" {
		where := fmt.Sprintf("https://%s/%s/commit/%s/checks", r.Host, r.Repo, head)
		switch {
		case len(ci.Failed) > 0:
			ci.Logs = fmt.Sprintf("Failed checks at %s: %s\n", where, strings.Join(ci.Failed, ", "))
		case ci.Runs > 0:
			ci.Logs = fmt.Sprintf("%d failed CI run(s) at %s, with no run log.\n", ci.Runs, where)
		case ci.Cancelled:
			ci.Logs = fmt.Sprintf("CI for %s was cancelled at %s before it tested the commit.\n", head, where)
		default:
			ci.Logs = fmt.Sprintf("CI for %s failed at %s with no failed check, failed run or cancellation.\n", head, where)
		}
	}
	switch {
	case len(ci.Failed) > 0:
		ci.Reason = "checks failed: " + strings.Join(ci.Failed, ", ")
	case ci.Runs > 0:
		ci.Reason = "a CI run failed without a named check; read its logs for the failing test"
	case ci.Cancelled:
		ci.Reason = "CI was cancelled before it tested the commit, so this is an infrastructure cause, not a test failure"
	}
	return ci
}

// confirmTrialProbes scopes a failed observation to trials that contain it.
func (s *Service) confirmTrialProbes(st State, probes map[string]headProbe, b state.MergeTrial) error {
	// Publication owns its scoped revalidation and already-published recovery.
	if b.Phase == "publishing" || b.Phase == "published" || b.Phase == "awaiting-root" || ((b.Phase == "failed" || b.Phase == "stacking") && !b.Bisect) {
		return nil
	}
	// Let the existing state transition discard locally invalid membership
	// before asking for remote evidence that stopped watches cannot supply.
	for _, member := range b.Changes {
		if !currentTrialMember(st.Records[member.Key], member) {
			return nil
		}
	}
	for _, member := range b.Changes {
		if changeKind(*st.Records[member.Key]) == "local" {
			continue
		}
		probe, ok := probes[member.Key]
		if !ok {
			// Paused rooms intentionally have no probes. The trial's room
			// guard keeps their work held without reporting a GitHub failure.
			if r := st.Records[member.Key]; r != nil {
				paused, err := s.RoomPaused(r.RoomID)
				if err != nil {
					return err
				}
				if paused {
					continue
				}
			}
			return fmt.Errorf("%s: GitHub head could not be confirmed; wait for a successful refresh", member.Key)
		}
		if probe.Err != nil {
			return fmt.Errorf("%s: GitHub head could not be confirmed: %w", member.Key, probe.Err)
		}
	}
	return nil
}

func currentTrialMember(r *Record, frozen state.MergeTrialChange) bool {
	return r != nil && r.Watching && r.State == "open" && r.Head == frozen.Head
}
