package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
)

// trialLocalCIStartWait is how long a merge trial head waits for a required
// local CI run before the merge queue retries scheduling it once, and how long
// it waits after that retry before it alerts root when nothing started.
const trialLocalCIStartWait = 3 * time.Minute

// watchTrialLocalCIStart bounds a trial head that waits for local CI no machine
// has started. Such a wait once had no end: hosted CI can pass a trial branch
// without the local suites a room requires, and the trial then waits for
// evidence that never comes (D-407). The watcher records the wait when it first
// sees it, retries scheduling once after trialLocalCIStartWait, and posts one
// typed root alert after another trialLocalCIStartWait if the retry also
// started nothing. A run in flight or a complete saved result clears the
// record, because local CI has started or already decided the head.
func (s *Service) watchTrialLocalCIStart(ctx context.Context, repo string, r Record, b *state.MergeTrial) error {
	if changeKind(r) == "github" {
		mode, err := s.ciRegistry().Mode(ctx, r.RoomID)
		if err != nil {
			return err
		}
		// A room whose CI GitHub decides has no local run to wait for.
		if mode != localci.ModeLocal && mode != localci.ModeBoth {
			return nil
		}
	}
	trial := r
	trial.Head, trial.Branch = b.Head, b.Branch
	job, err := s.registeredJobForRecord(trial)
	if err != nil {
		// The gate reports a missing base SHA on its own read.
		return nil
	}
	if dir, err := s.checkoutDir(ctx, job.record); err == nil {
		job.record.RepoPath = dir
	} else {
		return err
	}
	if s.liveLocalCIRun(job.record) != nil {
		return s.clearTrialLocalCIWait(repo, b)
	}
	ciRepo, err := localci.CanonicalRepo(ctx, job.record.RepoPath)
	if err != nil {
		return err
	}
	result, err := s.ciRegistry().LoadResult(ctx, ciRepo, b.Head)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// A complete result whose action is still a retry has not decided the
	// head: its replacement is the run this wait is for, so the wait survives
	// until that replacement starts, decides, or the alert names it.
	if err == nil && savedResultActionFor(result) == settleSavedResult && s.registeredJobResultComplete(ctx, job, result) {
		return s.clearTrialLocalCIWait(repo, b)
	}
	plan, err := s.planRegisteredCI(ctx, job)
	if err != nil || !plan.runnable() {
		// No local definition gates this head, so hosted CI decides it.
		return nil
	}
	now := s.now()
	if b.LocalCIWait == nil {
		b.LocalCIWait = &state.MergeTrialLocalCIWait{Since: now}
		return s.saveMergeTrial(repo, *b)
	}
	waited := b.LocalCIWait
	switch {
	case !waited.Retried && !now.Before(waited.Since.Add(trialLocalCIStartWait)):
		waited.Retried, waited.Since = true, now
		if err := s.saveMergeTrial(repo, *b); err != nil {
			return err
		}
		// One bounded retry through the same admission a tick uses, so a
		// transient scheduling failure does not hold the trial until a human
		// runs its checks by hand. A suite no machine allows is a wait, not an
		// error: the alert names it when the wait outlives the retry.
		if _, err := s.startRegisteredCI(ctx, job); err != nil {
			var blocked localci.SuiteBlockedError
			if !errors.As(err, &blocked) {
				return err
			}
		}
		return nil
	case waited.Retried && !waited.Alerted && !now.Before(waited.Since.Add(trialLocalCIStartWait)):
		// Deliver before the flag is saved, so a failed delivery or a stop
		// between them cannot spend the one alert: the next tick delivers
		// again, and postMergeNotice keeps a delivered alert single.
		if err := s.alertTrialLocalCIUnstarted(ctx, b, plan); err != nil {
			return err
		}
		waited.Alerted = true
		return s.saveMergeTrial(repo, *b)
	}
	return nil
}

// clearTrialLocalCIWait drops the record of an unstarted wait: local CI has
// started, or a complete result already decided the head.
func (s *Service) clearTrialLocalCIWait(repo string, b *state.MergeTrial) error {
	if b.LocalCIWait == nil {
		return nil
	}
	b.LocalCIWait = nil
	return s.saveMergeTrial(repo, *b)
}

// alertTrialLocalCIUnstarted posts the one typed root alert of a trial head
// whose required local CI never started. The alert names the trial, its head,
// the checks nothing ran, and the command that starts them.
func (s *Service) alertTrialLocalCIUnstarted(ctx context.Context, b *state.MergeTrial, plan localCIPlan) error {
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	member := st.Records[b.Changes[0].Key]
	if member == nil {
		return nil
	}
	var names []string
	for _, name := range plan.required.RequiredChecks(plan.suites) {
		names = append(names, string(name))
	}
	anchor := *member
	anchor.Head = b.Head
	ev := mergeTrialEvent(st, *b, room.MergeChecks)
	ev.ReasonKind, ev.Reason, ev.Key = room.MergeReasonOther, "local CI has not started", "local-ci-unstarted"
	ev.Summary = fmt.Sprintf("required local checks never started: %s", strings.Join(names, ", "))
	text := fmt.Sprintf("Merge trial %s (head %s) has waited more than %s for local CI that no machine started, after one retry. Required local checks: %s. Start them with 'dyna git ci run %s', or inspect the CI machines with 'dyna machine list'.",
		b.Branch, b.Head, 2*trialLocalCIStartWait, strings.Join(names, ", "), b.Head)
	return s.postMergeNotice(ctx, anchor, "merge-trial-local-ci-unstarted/"+trialIdentity(*b), Notice{NeedsRoot: true, Kind: "ci", Merge: &ev, Text: text})
}
