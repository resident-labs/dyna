package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"dyna-agent/internal/localci"
)

// localStatusPrefix marks the commit statuses Dyna posted for a local CI run
// before it stopped posting them. GitHub keeps them on old commits, so the
// prefix still tells a leftover local status apart from a hosted one: a
// leftover status must never become hosted evidence for a merge.
const localStatusPrefix = "Dyna local CI on "

func localCommitStatus(s Status) bool { return strings.HasPrefix(s.Description, localStatusPrefix) }
func replaceableHostedWorkflow(run Run) bool {
	path, _, _ := strings.Cut(run.Path, "@")
	return path == ".github/workflows/ci.yml"
}

func queuedHostedRun(run Run) bool {
	return run.Status == "queued" || run.Status == "waiting"
}
func hostedRunMatches(job registeredCIJob, run Run) bool {
	if run.HeadSHA != job.head || run.HeadBranch != job.branch {
		return false
	}
	if job.trial {
		return run.Event == "push" && strings.HasPrefix(job.branch, "dyna/merge-trial/")
	}
	return strings.EqualFold(job.record.HeadRepo, job.record.Repo) && run.Event == "pull_request"
}

// Cancellation is limited to queued exact-head runs. Started hosted jobs race
// local CI. Main pushes and fork PRs cannot enter this path.
func (s *Service) cancelQueuedHostedCI(ctx context.Context, job registeredCIJob, repo string) error {
	gh := s.github(job.record.RoomID)
	runs, err := gh.readCommitRuns(ctx, refRecord(job.record), job.head)
	if err != nil {
		return err
	}
	registry := s.ciRegistry()
	saved, err := registry.LoadHostedRuns(ctx, repo, job.head)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if saved.Attempts == nil {
		saved.Attempts = map[int64]int{}
	}
	for _, run := range runs {
		if !hostedRunMatches(job, run) || !queuedHostedRun(run) || !replaceableHostedWorkflow(run) {
			continue
		}
		// Fetch the current status just before cancel, since a listed job can start.
		var fresh Run
		path := fmt.Sprintf("repos/%s/actions/runs/%d", job.record.Repo, run.ID)
		if err := gh.api(ctx, refRecord(job.record), path, &fresh); err != nil {
			return err
		}
		if fresh.ID != run.ID || !hostedRunMatches(job, fresh) || !queuedHostedRun(fresh) || !replaceableHostedWorkflow(fresh) {
			continue
		}
		if attempt, exists := saved.Attempts[fresh.ID]; exists && fresh.Attempt <= attempt {
			continue
		}
		// Save intent first, so a restart recognizes the cancellation as ours.
		if _, exists := saved.Attempts[fresh.ID]; !exists {
			saved.IDs = append(saved.IDs, fresh.ID)
		}
		saved.Resumed = false
		saved.Attempts[fresh.ID] = fresh.Attempt
		if err := registry.SaveHostedRuns(ctx, repo, job.head, saved); err != nil {
			return err
		}
		if _, _, err := gh.requestMethod(ctx, refRecord(job.record), path+"/cancel", "POST"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) resumeHostedCI(ctx context.Context, job registeredCIJob, repo string) error {
	registry := s.ciRegistry()
	saved, err := registry.LoadHostedRuns(ctx, repo, job.head)
	if errors.Is(err, os.ErrNotExist) || (err == nil && saved.Resumed) {
		return nil
	}
	if err != nil {
		return err
	}
	gh := s.github(job.record.RoomID)
	for _, id := range saved.IDs {
		path := fmt.Sprintf("repos/%s/actions/runs/%d", job.record.Repo, id)
		var current Run
		if err := gh.api(ctx, refRecord(job.record), path, &current); err != nil {
			return err
		}
		if !hostedRunMatches(job, current) {
			continue
		}
		if current.Attempt > saved.Attempts[id] {
			continue
		}
		if current.Status != "completed" {
			return nil
		}
		if current.Conclusion != "cancelled" {
			if current.Conclusion != "failure" || !replaceableHostedWorkflow(current) {
				return nil
			}
			jobs, err := gh.runJobs(ctx, refRecord(job.record), current)
			if err != nil {
				return err
			}
			var checks []Check
			for _, job := range jobs {
				check := Check{Name: job.Name, Status: job.Status, Conclusion: job.Conclusion}
				check.App.Slug = "github-actions"
				check.CheckSuite.ID = current.CheckSuiteID
				checks = append(checks, check)
			}
			definition, err := localci.LoadDefinition(ctx, job.record.RepoPath, job.head)
			if errors.Is(err, localci.ErrDefinitionNotFound) {
				definition, err = nil, nil
			}
			if err != nil {
				return err
			}
			if _, cancelled := aggregateCancelled(definition, checks, current.CheckSuiteID); !cancelled {
				return nil
			}
		}
		if _, _, err := gh.requestMethod(ctx, refRecord(job.record), path+"/rerun", "POST"); err != nil {
			return err
		}
	}
	saved.Resumed = true
	return registry.SaveHostedRuns(ctx, repo, job.head, saved)
}

// aggregateCancelled recognizes dependency-only aggregates from the
// repository definition. Every dependency must have passed or been cancelled;
// an independent failure or missing provider group keeps hosted CI red.
func aggregateCancelled(definition *localci.Definition, checks []Check, suite int64) (localci.Group, bool) {
	if definition == nil {
		return "", false
	}
	class := classifyHostedSuite(definition, checks, suite, true, false)
	if class.realFailure {
		return "", false
	}
	for _, id := range definition.Suites() {
		described, _ := definition.Checks(id)
		for _, check := range described {
			if class.cancelledOnlyAggregates[check.Group.Name()] {
				return check.Group, true
			}
		}
	}
	return "", false
}

func cancelledAggregates(definition *localci.Definition, checks []Check, suite int64) map[localci.CheckName]bool {
	if definition == nil {
		return nil
	}
	// A provider group is decided first: a hosted job named for it fails when
	// its own checks were cancelled, and the suite aggregate counts that failure
	// as the cancellation it is.
	providers := cancelledProviderAggregates(definition, checks, suite)
	aggregates := map[localci.CheckName]bool{}
	for name := range providers {
		aggregates[name] = true
	}
	for _, id := range definition.Suites() {
		described, _ := definition.Checks(id)
		for _, check := range described {
			if check.Group != "" && aggregateGroupCancelled(definition, check.Group, checks, suite, providers) {
				aggregates[check.Group.Name()] = true
			}
		}
	}
	return aggregates
}

// cancelledProviderAggregates names every provider group whose checks all ran
// cancelled, under a failed job named for the group. Dyna reports one row per
// provider group, and a repository may host that row as its own job, such as a
// frontend job that needs one job per shard.
func cancelledProviderAggregates(definition *localci.Definition, checks []Check, suite int64) map[localci.CheckName]bool {
	cancelled := map[localci.CheckName]bool{}
	for _, id := range definition.Suites() {
		described, _ := definition.Checks(id)
		for _, check := range described {
			group := definition.ProviderGroup(check.Name)
			if group == check.Name || cancelled[group] {
				continue
			}
			if providerAggregateCancelled(definition, group, checks, suite) {
				cancelled[group] = true
			}
		}
	}
	return cancelled
}

// providerAggregateCancelled reports whether a job named for the provider group
// failed only because every check of that group was cancelled.
func providerAggregateCancelled(definition *localci.Definition, aggregate localci.CheckName, checks []Check, suite int64) bool {
	cancelled, failedAggregate, members := false, false, 0
	for _, check := range checks {
		if check.CheckSuite.ID != suite || check.App.Slug != "github-actions" {
			continue
		}
		if localci.CheckName(check.Name) == aggregate {
			failedAggregate = check.Status == "completed" && check.Conclusion == "failure"
			continue
		}
		if definition.ProviderGroup(localci.CheckName(check.Name)) != aggregate {
			continue
		}
		if check.Status != "completed" || (check.Conclusion != "success" && check.Conclusion != "cancelled") {
			return false
		}
		members++
		cancelled = cancelled || check.Conclusion == "cancelled"
	}
	return failedAggregate && cancelled && members > 0
}

func aggregateGroupCancelled(definition *localci.Definition, aggregate localci.Group, checks []Check, suite int64, providers map[localci.CheckName]bool) bool {
	groups := map[localci.CheckName]bool{}
	cancelled, failedAggregate := false, false
	expected := map[localci.CheckName]bool{}
	for _, group := range definition.ProviderGroups(aggregate) {
		expected[group] = true
	}
	if len(expected) == 0 {
		return false
	}
	for _, check := range checks {
		if check.CheckSuite.ID != suite || check.App.Slug != "github-actions" {
			continue
		}
		if check.Name == string(aggregate.Name()) {
			failedAggregate = check.Status == "completed" && check.Conclusion == "failure"
			continue
		}
		group := definition.ProviderGroup(localci.CheckName(check.Name))
		if !expected[group] {
			continue
		}
		if providers[localci.CheckName(check.Name)] {
			// A job named for its provider group only repeats its members.
			groups[group] = true
			cancelled = true
			continue
		}
		if check.Status != "completed" || (check.Conclusion != "success" && check.Conclusion != "cancelled") {
			return false
		}
		groups[group] = true
		cancelled = cancelled || check.Conclusion == "cancelled"
	}
	return failedAggregate && cancelled && len(groups) == len(expected)
}

// hostedSuiteClass separates real failures from every aggregate that only
// repeats cancellations. Both cancellation filters use the same classification.
type hostedSuiteClass struct {
	realFailure             bool
	cancelledOnlyAggregates map[localci.CheckName]bool
}

// hostedAggregateRows names every hosted job the repository definition
// declares aggregate-only: the group a suite's checks feed, and each check
// marked hostedAggregate. Such a job only repeats the outcome of the jobs it
// requires. A provider name alone proves nothing, so any other failure is a
// job that ran, whatever its name.
func hostedAggregateRows(definition *localci.Definition) map[localci.CheckName]bool {
	rows := map[localci.CheckName]bool{}
	for _, id := range definition.Suites() {
		described, _ := definition.Checks(id)
		for _, check := range described {
			if check.Group != "" {
				rows[check.Group.Name()] = true
			}
			if definition.HostedAggregate(check.Name) {
				rows[check.Name] = true
			}
		}
	}
	return rows
}

// classifyHostedSuite classifies one hosted suite. The caller disables
// aggregate trust for a run without a suite ID or a replaceable workflow.
// ownCancellation says a saved record proves Dyna itself cancelled the run,
// and Dyna cancels only a run that is still queued, so no test job inside it
// ever ran: a failure it still reports is then a dependency-only aggregate
// job echoing the cancellation it saw in its needs (D-168). Only a check the
// definition declares an aggregate row earns that reading; a standalone or
// unknown job that failed was started by the race between the queued-status
// read and the cancel, and its failure stays evidence.
func classifyHostedSuite(definition *localci.Definition, checks []Check, suite int64, trustAggregate, ownCancellation bool) hostedSuiteClass {
	class := hostedSuiteClass{}
	if trustAggregate {
		class.cancelledOnlyAggregates = cancelledAggregates(definition, checks, suite)
	}
	var ownRows map[localci.CheckName]bool
	if ownCancellation && definition != nil {
		ownRows = hostedAggregateRows(definition)
	}
	for _, check := range checks {
		if check.CheckSuite.ID != suite || check.Status != "completed" || success(check.Conclusion) || check.Conclusion == "cancelled" {
			continue
		}
		if class.cancelledOnlyAggregates[localci.CheckName(check.Name)] {
			continue
		}
		// The definition naming this check an aggregate row is positive evidence
		// that the job only repeats its members; under Dyna's own cancellation
		// such a failure echoes the cancellation rather than reporting a test.
		if ownRows[localci.CheckName(check.Name)] {
			continue
		}
		class.realFailure = true
	}
	if class.realFailure {
		class.cancelledOnlyAggregates = nil
	}
	return class
}

func withoutLocalHostedCancellations(snap Snapshot, saved localci.HostedRuns, definition *localci.Definition) Snapshot {
	snap.localCIDef = definition
	current := snap.currentSuiteChecks()
	cancelledSuites, ignoredSuites := map[int64]bool{}, map[int64]bool{}
	aggregates := map[int64]map[localci.CheckName]bool{}
	var runs []Run
	for _, run := range snap.Runs {
		attempt, ours := saved.Attempts[run.ID]
		if !ours || run.Attempt > attempt {
			runs = append(runs, run)
			continue
		}
		if run.CheckSuiteID == 0 {
			if run.Conclusion != "cancelled" {
				runs = append(runs, run)
			}
			continue
		}
		suite := run.CheckSuiteID
		cancelledSuites[suite] = true
		class := classifyHostedSuite(definition, current, suite, replaceableHostedWorkflow(run), run.Conclusion == "cancelled")
		aggregates[suite] = class.cancelledOnlyAggregates
		// The saved record proves the cancellation is ours, so here a run that is
		// still active, or one whose aggregate failed, counts too. This is the one
		// rule the general filter below does not share: with no saved record it
		// needs the run's own cancelled conclusion, because only that proves a
		// cancellation rather than a test ended it. Cancellation can leave the
		// run active, cancelled, or failed after its aggregate runs; none is a test
		// failure when only cancelled work caused it.
		if !class.realFailure && (run.Conclusion == "cancelled" || len(class.cancelledOnlyAggregates) > 0) {
			ignoredSuites[suite] = true
			continue
		}
		runs = append(runs, run)
	}
	snap.Runs = runs
	var checks []Check
	for _, check := range snap.Checks {
		suite := check.CheckSuite.ID
		if check.App.Slug == "github-actions" && (ignoredSuites[suite] || cancelledSuites[suite] && (check.Conclusion == "cancelled" || aggregates[suite][localci.CheckName(check.Name)])) {
			continue
		}
		checks = append(checks, check)
	}
	snap.Checks = checks
	return snap
}

// hostedWithoutLocalCancellations gives the budget reads the same snapshot that
// the CI gate uses, so a hosted run Dyna cancelled for local CI supplies no
// failure log excerpt and no artifact inspection.
func (s *Service) hostedWithoutLocalCancellations(ctx context.Context, r Record, snap Snapshot) (Snapshot, error) {
	dir, dirErr := s.checkoutDir(ctx, r)
	if dirErr != nil {
		// No repository means no saved cancellation to find. The CI gate
		// reports the same condition on its own path.
		return snap, nil
	}
	r.RepoPath = dir
	repo, err := localci.CanonicalRepo(ctx, dir)
	if err != nil {
		// No repository means no saved cancellation to find. The CI gate
		// reports the same condition on its own path.
		return snap, nil
	}
	head := snap.Pull.Head.SHA
	if head == "" {
		head = r.Head
	}
	saved, err := s.ciRegistry().LoadHostedRuns(ctx, repo, head)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return snap, nil
		}
		return snap, err
	}
	if len(saved.Attempts) == 0 {
		return snap, nil
	}
	definition, err := localci.LoadDefinition(ctx, r.RepoPath, head)
	if errors.Is(err, localci.ErrDefinitionNotFound) {
		definition, err = nil, nil
	}
	if err != nil {
		return snap, err
	}
	return withoutLocalHostedCancellations(snap, saved, definition), nil
}

// hostedCancelledOnly reports a hosted failure that only cancellations caused:
// ignoring cancelled workflows leaves no failure behind. Such evidence proves
// nothing about the head, so it must not veto a complete local pass, while it
// must not become a pass either.
func hostedCancelledOnly(snap Snapshot, required []string) bool {
	if state, _, _, _ := snap.CIStatus(required...); state != "failed" {
		return false
	}
	cleared, _, _, _ := withoutCancelledHostedWorkflows(snap).CIStatus(required...)
	return cleared != "failed"
}

// withoutCancelledHostedWorkflows drops hosted runs that a cancellation ended
// without a real test failure, and the checks of their suites. Such a workflow
// never tested the head: GitHub ends a run when a newer one replaces it, and
// Dyna's CI time budget ends it too. The cancelled jobs and the aggregate job
// that only reports their cancellations are not evidence, so a conclusive local
// result decides instead. A run with any real failure stays.
func withoutCancelledHostedWorkflows(snap Snapshot) Snapshot {
	current := snap.currentSuiteChecks()
	ignored := map[int64]bool{}
	var runs []Run
	for _, run := range snap.Runs {
		// No saved record says who cancelled this run, so only its own cancelled
		// conclusion proves that a cancellation, not a test, ended it. A run Dyna
		// cancelled for local CI can still be active or failed at this point, so
		// this filter leaves that case to the saved-cancellation filter above.
		if run.Conclusion != "cancelled" {
			runs = append(runs, run)
			continue
		}
		suite := run.CheckSuiteID
		class := classifyHostedSuite(snap.localCIDef, current, suite, suite != 0 && replaceableHostedWorkflow(run), false)
		if class.realFailure {
			runs = append(runs, run)
			continue
		}
		ignored[suite] = true
	}
	snap.Runs = runs
	var checks []Check
	for _, check := range snap.Checks {
		if check.App.Slug == "github-actions" && ignored[check.CheckSuite.ID] {
			continue
		}
		checks = append(checks, check)
	}
	snap.Checks = checks
	return snap
}
