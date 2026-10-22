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
	repos = nil
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
		if len(groups[repo]) == 0 {
			repos = append(repos, repo)
		}
		groups[repo] = append(groups[repo], current)
	}
	for _, repo := range repos {
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
			return nil
		}); err != nil {
			failures = append(failures, err)
			continue
		}
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
		return s.stopMergeBatch(ctx, repo, *r, b)
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
				if err := s.postMergeNotice(ctx, member, "merge-trial-conflict", Notice{Target: member.Author, Text: text}); err != nil {
					return err
				}
			}
		}
		if len(included) == 0 {
			return s.discardMergeTrial(ctx, repo, *r, b)
		}
		b.Changes, b.Head, b.Phase, b.Reason = included, result.Head, "running", ""
		return s.saveMergeTrial(repo, b)
	case "running":
		ci, logs, reason, err := s.mergeTrialCI(ctx, *r, b.Branch, b.Head)
		if err != nil {
			return err
		}
		if ci == "failed" {
			b.Phase, b.Reason, b.Logs = "failed", "batch trial failed; waiting for root", logs
			s.noteMergeHistory(st, repo, b, "trial", "failed", b.Head, logs)
			return s.stopMergeBatch(ctx, repo, *r, b)
		}
		if ci != "passed" {
			if b.Reason != reason {
				b.Reason = reason
				return s.saveMergeTrial(repo, b)
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
			ci, logs, reason, err := s.mergeTrialCI(ctx, *r, b.Branch, b.Head)
			if err != nil {
				return err
			}
			if ci == "failed" {
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
					return s.stopMergeBatch(ctx, repo, *r, b)
				}
				return s.saveMergeTrial(repo, b)
			}
			if ci != "passed" {
				b.Reason = reason
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
				if err := s.publishLocalChange(ctx, member, change.Head); err != nil {
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

// mergeTrialCI accepts only push evidence for the frozen trial branch and head.
// Missing or queued runs do not prove that the trial suite ran.
func (s *Service) mergeTrialCI(ctx context.Context, r Record, branch, head string) (string, string, string, error) {
	if changeKind(r) == "local" {
		return s.localTrialCI(ctx, r, head)
	}
	var snap Snapshot
	if err := s.github(r.RoomID).readCommitCI(ctx, refRecord(r), head, &snap); err != nil {
		return "", "", "", err
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
		return "", "", "", err
	}
	snap, err = s.withRegisteredCI(ctx, trialRecord, snap, EffectivePolicy(st, r))
	if err != nil {
		return "", "", "", err
	}
	if snap.localCI != nil && conclusiveCI(snap.localCI.Status) {
		return snap.localCI.Status, snap.localCI.Logs, "", nil
	}
	ci := "pending"
	var failed []Run
	if started {
		ci, _, _, failed = snap.CIStatus()
	}
	if ci == "pending" {
		total, done, active := 0, 0, false
		for _, run := range currentRuns(pushes) {
			jobs, err := s.github(r.RoomID).runJobs(ctx, refRecord(r), run)
			if err != nil {
				return "", "", "", err
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
		reason := "waiting for push CI to start"
		if active {
			reason = fmt.Sprintf("running (%d of %d jobs done)", done, total)
		}
		return ci, "", reason, nil
	}
	logs := ""
	if ci == "failed" {
		for _, run := range failed {
			logs += s.github(r.RoomID).Logs(ctx, refRecord(r), run) + "\n"
		}
	}
	return ci, logs, "", nil
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
