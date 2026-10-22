package watch

import (
	"context"
	"encoding/json"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/pr/state"
)

// budgetResult contains one run's prepared changes. A nil field leaves it alone.
type budgetResult struct {
	queue, problem *string
	reason, text   string
	quiet          bool
	input          string
}

func (b *budgetResult) setError(r Record, token string, err error) {
	reason := ""
	if err != nil {
		reason = "budget error: " + Sanitize(err.Error())
		log.Printf("PR #%d run %s: %s", r.Number, token, reason)
	}
	b.problem = &reason
}

func budgetPolicyKey(p ChangePolicy) string {
	warn, fail, _ := p.CIBudget()
	raw, _ := json.Marshal(struct {
		Enforce    bool
		Warn, Fail string
		Exempt     []string
	}{p.EnforceCITimeBudget(), warn.String(), fail.String(), p.CIBudgetExempt})
	return digest(string(raw))
}

// budgetRecordMatches rechecks the identity and policy used during slow reads.
func budgetRecordMatches(st State, live *Record, r Record, policy ChangePolicy) bool {
	return live != nil && live.Watching && live.State == "open" &&
		live.Head == r.Head && live.RoomID == r.RoomID && live.Author == r.Author &&
		live.Host == r.Host && live.Repo == r.Repo && live.Number == r.Number &&
		live.RepoPath == r.RepoPath && live.Branch == r.Branch && live.BaseRef == r.BaseRef && live.Base == r.Base &&
		live.Polling.CIVersion == r.Polling.CIVersion && live.CIKey == r.CIKey &&
		budgetPolicyKey(EffectivePolicy(st, *live)) == budgetPolicyKey(policy)
}

func (b budgetResult) unchanged(r Record, head, token string) bool {
	if r.CIBudgetHead != head || r.CIBudgetInputs[token] != b.input {
		return false
	}
	if b.queue != nil && r.CIQueueWait[token] != *b.queue {
		return false
	}
	if b.problem != nil && r.CIBudgetErrors[token] != *b.problem {
		return false
	}
	return b.reason == "" || (r.CIBudgetRuns[token] == b.reason && r.CIBudgetSteps[token] &&
		(b.reason != "warned" || r.CIBudgetNotes[token] == b.text))
}

func (s *Service) saveBudgetResult(r Record, snap Snapshot, policy ChangePolicy, token string, b budgetResult) error {
	observed, err := state.ReadSnapshot(s.Store.Root)
	if err != nil {
		return err
	}
	live, ok := observed.Record(r.Key)
	if !ok || !budgetRecordMatches(observed.Metadata(), &live, r, policy) || b.unchanged(live, snap.Pull.Head.SHA, token) {
		return nil
	}
	return s.Store.updateRecords([]string{r.Key}, func(st *State) error {
		live := st.Records[r.Key]
		if !budgetRecordMatches(*st, live, r, policy) || b.unchanged(*live, snap.Pull.Head.SHA, token) {
			return errNoStoreChange
		}
		startBudgetHead(live, snap.Pull.Head.SHA)
		if b.queue != nil {
			if live.CIQueueWait == nil {
				live.CIQueueWait = map[string]string{}
			}
			live.CIQueueWait[token] = *b.queue
		}
		if b.problem != nil {
			if *b.problem == "" {
				delete(live.CIBudgetErrors, token)
			} else {
				if live.CIBudgetErrors == nil {
					live.CIBudgetErrors = map[string]string{}
				}
				live.CIBudgetErrors[token] = *b.problem
			}
		}
		if b.input == "" {
			delete(live.CIBudgetInputs, token)
		} else {
			if live.CIBudgetInputs == nil {
				live.CIBudgetInputs = map[string]string{}
			}
			live.CIBudgetInputs[token] = b.input
		}
		if b.reason != "" {
			applyBudgetVerdict(live, snap, token, b.reason, b.text, b.quiet)
		}
		return nil
	})
}

// budgetOutcomesEqual compares this PR's timing results and check excerpts.
// Unrelated PR commits and target metadata edits do not require reprocessing.
func budgetOutcomesEqual(a, b Record) bool {
	if a.CIBudgetHead != b.CIBudgetHead ||
		!maps.Equal(a.CIBudgetErrors, b.CIBudgetErrors) ||
		!maps.Equal(a.CIBudgetRuns, b.CIBudgetRuns) ||
		!maps.Equal(a.CIBudgetNotes, b.CIBudgetNotes) ||
		!maps.Equal(a.CIBudgetSteps, b.CIBudgetSteps) ||
		!maps.Equal(a.CIBudgetInputs, b.CIBudgetInputs) ||
		!maps.Equal(a.CIQueueWait, b.CIQueueWait) {
		return false
	}
	if a.CITestHead != b.CITestHead || a.CITestPolicy != b.CITestPolicy ||
		a.CITestReportNote != b.CITestReportNote || a.CINoJUnitWarned != b.CINoJUnitWarned ||
		!maps.Equal(a.CITestRuns, b.CITestRuns) ||
		!maps.Equal(a.CITestErrors, b.CITestErrors) ||
		!maps.Equal(a.CITestFailures, b.CITestFailures) ||
		!maps.Equal(a.CITestRetries, b.CITestRetries) {
		return false
	}
	return slices.EqualFunc(a.Checks, b.Checks, func(x, y CheckSummary) bool {
		return x.ID == y.ID && x.Completed == y.Completed &&
			x.FailureExcerpt == y.FailureExcerpt && x.FailureExcerptChecked == y.FailureExcerptChecked
	})
}

// budgetCIUnreconciled also detects a committed result left by an earlier pass.
// Pending and cancelled CI retain applyCI's existing CIKey rules.
func (s *Service) budgetCIUnreconciled(r Record, snap Snapshot, policy ChangePolicy) bool {
	ci, key, _, _ := s.ciStatus(snap, r, policy)
	if ci != r.CI {
		return true
	}
	if ci == "pending" || key == r.CIKey {
		return false
	}
	reason, _ := budgetFailure(r, snap, policy)
	cancelled := ci == "failed" && reason == "" && snap.ciOnlyCancelled(policy.RequiredChecks...)
	if cancelled {
		return r.CIReason != cancelledCIReason
	}
	return true
}

// quietBudgetRecovery uses saved CI evidence only to decide whether the normal
// full poll must finish an interrupted budget reconciliation. Ordinary quiet
// PRs need no cache decoding. Inactive run failures do not schedule more work.
func (s *Service) quietBudgetRecovery(ctx context.Context, r Record, policy ChangePolicy, pull Pull, entries map[string]cachedResponse) bool {
	if !policy.EnforceCITimeBudget() {
		return false
	}
	candidate, reconciled := false, r.CI == "failed"
	for _, outcome := range []struct {
		head, prefix string
		runs         map[string]string
	}{
		{r.CIBudgetHead, "over CI time budget", r.CIBudgetRuns},
		{r.CITestHead, "tests over ", r.CITestRuns},
	} {
		if outcome.head != r.Head {
			continue
		}
		for token, reason := range outcome.runs {
			if !strings.HasPrefix(reason, outcome.prefix) {
				continue
			}
			candidate = true
			// Every candidate must name the applied token and reason. One
			// matching token cannot hide a different unfinished outcome.
			reconciled = reconciled && r.CIKey == "budget:"+r.Head+":"+token && r.CIReason == reason
		}
	}
	// With no current overrun, the applied cancellation reason already
	// accounts for the retained key. Quiet probes need no saved-CI read.
	if !candidate && r.CI == "failed" && r.CIReason == cancelledCIReason {
		return false
	}
	// A replaced overrun can leave its old applied key behind. That key is
	// recovery evidence even when no stored outcome still exceeds the budget.
	staleBudgetKey := strings.HasPrefix(r.CIKey, "budget:"+r.Head+":")
	if (!candidate && !staleBudgetKey) || (candidate && reconciled) {
		return false
	}
	// Reuse the live poll's parser and run selection. Missing or invalid saved
	// evidence needs a full poll, never a passing result from a partial cache.
	gh := GitHub{requests: &requestCache{entries: entries, cacheOnly: true}}
	runs, err := gh.readCommitRuns(ctx, refRecord(r), r.Head)
	if err != nil {
		return true
	}
	snap := Snapshot{Pull: pull, Runs: runs}
	reason, _ := budgetFailure(r, snap, policy)
	if reason == "" && !staleBudgetKey {
		return false
	}
	// A cleared overrun and a test overrun can need the ordinary CI key.
	// Inactive runs without a stale budget key need only the bounded run list.
	if reason == "" || strings.HasPrefix(reason, "tests over ") {
		if err := gh.readCommitChecks(ctx, refRecord(r), r.Head, &snap); err != nil {
			return true
		}
		if err := gh.readCommitStatuses(ctx, refRecord(r), r.Head, &snap); err != nil {
			return true
		}
	}
	return s.budgetCIUnreconciled(r, snap, policy)
}

// completedBudgetJobs excludes incomplete API observations from durable reuse.
func completedBudgetJobs(jobs []budgetJob, now time.Time) bool {
	executed := false
	for _, job := range jobs {
		if job.Status != "completed" {
			return false
		}
		for _, step := range job.Steps {
			if step.Conclusion == "skipped" || runnerOwnedSteps[step.Name] {
				continue
			}
			if step.Status != "completed" {
				return false
			}
			start, startErr := time.Parse(time.RFC3339, step.Started)
			end, endErr := time.Parse(time.RFC3339, step.Completed)
			if startErr != nil || endErr != nil || end.Before(start) || end.After(now) {
				return false
			}
			executed = executed || end.After(start)
		}
	}
	return executed
}

// budgetInputVersion reuses the existing response cache, not a second cache.
// Running jobs and missing evidence always require another measurement.
func budgetInputVersion(root string, gh GitHub, r Record, snap Snapshot, run Run, policy ChangePolicy) string {
	if gh.requests == nil || run.Status != "completed" {
		return ""
	}
	cache := gh.requests
	response := func(path string) (cachedResponse, bool) {
		key := strings.ToLower(r.Host) + "/" + path
		value, ok := cache.used[key]
		if !ok {
			value, ok = cache.entries[key]
		}
		valid := ok && len(value.Body) > 0 && json.Valid(value.Body)
		if valid {
			cache.use(key, value)
		}
		return value, valid
	}
	var versions []string
	seen := map[string]bool{}
	total := 0
	for path := runJobsPath(refRecord(r), run); path != ""; {
		if seen[path] {
			return ""
		}
		seen[path] = true
		value, ok := response(path)
		if !ok {
			return ""
		}
		total += len(value.Body)
		if total > 4*1024*1024 {
			return ""
		}
		versions = append(versions, path, value.ETag, digest(string(value.Body)), value.Next)
		path = value.Next
	}
	if value, ok := response(workflowPath(refRecord(r), run)); ok {
		versions = append(versions, value.ETag, digest(string(value.Body)))
	}
	raw, _ := json.Marshal(struct {
		Root, Key, Room, Author, Head, Base, Policy string
		Run                                         Run
		Responses                                   []string
	}{root, r.Key, r.RoomID, r.Author, snap.Pull.Head.SHA, snap.Pull.Base.Ref, budgetPolicyKey(policy), run, versions})
	return digest(string(raw))
}
