package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"dyna-agent/internal/localci"
)

const localStatusPrefix = "Dyna local CI on "

func localCommitStatus(s Status) bool { return strings.HasPrefix(s.Description, localStatusPrefix) }
func localStatusNode(s Status) string {
	if !localCommitStatus(s) {
		return ""
	}
	return strings.TrimPrefix(s.Description, localStatusPrefix)
}
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

// aggregateCancelled recognizes a dependency-only aggregate from the
// repository definition. Every dependency must have passed or been cancelled;
// an independent failure or missing provider group keeps hosted CI red.
func aggregateCancelled(definition *localci.Definition, checks []Check, suite int64) (localci.Group, bool) {
	if definition == nil {
		return "", false
	}
	for _, id := range definition.Suites() {
		described, _ := definition.Checks(id)
		for _, check := range described {
			if check.Group == "" {
				continue
			}
			if aggregateGroupCancelled(definition, check.Group, checks, suite) {
				return check.Group, true
			}
		}
	}
	return "", false
}

func aggregateGroupCancelled(definition *localci.Definition, aggregate localci.Group, checks []Check, suite int64) bool {
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
		if expected[group] {
			if check.Status != "completed" || (check.Conclusion != "success" && check.Conclusion != "cancelled") {
				return false
			}
			groups[group] = true
			cancelled = cancelled || check.Conclusion == "cancelled"
			continue
		}
		if check.Status == "completed" && !success(check.Conclusion) && check.Conclusion != "cancelled" {
			return false
		}
	}
	return failedAggregate && cancelled && len(groups) == len(expected)
}

// hostedSuiteClass separates a real failure from an aggregate that only
// repeats cancellations. Both cancellation filters use the same classification.
type hostedSuiteClass struct {
	realFailure            bool
	cancelledOnlyAggregate localci.Group
}

// classifyHostedSuite classifies one hosted suite. The caller disables
// aggregate trust for a run without a suite ID or a replaceable workflow.
func classifyHostedSuite(definition *localci.Definition, checks []Check, suite int64, trustAggregate bool) hostedSuiteClass {
	var aggregate localci.Group
	if trustAggregate {
		aggregate, _ = aggregateCancelled(definition, checks, suite)
	}
	class := hostedSuiteClass{cancelledOnlyAggregate: aggregate}
	for _, check := range checks {
		if check.CheckSuite.ID != suite || check.Status != "completed" || success(check.Conclusion) || check.Conclusion == "cancelled" {
			continue
		}
		if aggregate != "" && check.Name == string(aggregate.Name()) {
			continue
		}
		class.realFailure = true
	}
	return class
}

func withoutLocalHostedCancellations(snap Snapshot, saved localci.HostedRuns, definition *localci.Definition) Snapshot {
	snap.localCIDef = definition
	current := snap.currentSuiteChecks()
	cancelledSuites, ignoredSuites := map[int64]bool{}, map[int64]bool{}
	aggregates := map[int64]localci.Group{}
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
		class := classifyHostedSuite(definition, current, suite, replaceableHostedWorkflow(run))
		aggregates[suite] = class.cancelledOnlyAggregate
		// The saved record proves the cancellation is ours, so here a run that is
		// still active, or one whose aggregate failed, counts too. This is the one
		// rule the general filter below does not share: with no saved record it
		// needs the run's own cancelled conclusion, because only that proves a
		// cancellation rather than a test ended it. Cancellation can leave the
		// run active, cancelled, or failed after its aggregate runs; none is a test
		// failure when only cancelled work caused it.
		if !class.realFailure && (run.Conclusion == "cancelled" || class.cancelledOnlyAggregate != "") {
			ignoredSuites[suite] = true
			continue
		}
		runs = append(runs, run)
	}
	snap.Runs = runs
	var checks []Check
	for _, check := range snap.Checks {
		suite := check.CheckSuite.ID
		if check.App.Slug == "github-actions" && (ignoredSuites[suite] || cancelledSuites[suite] && (check.Conclusion == "cancelled" || (aggregates[suite] != "" && check.Name == string(aggregates[suite].Name())))) {
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
	if r.RepoPath == "" {
		return snap, nil
	}
	repo, err := localci.CanonicalRepo(ctx, r.RepoPath)
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

// commitStatusState is the one conversion from a local CI outcome to the GitHub
// commit status state Dyna posts. An infrastructure error posts no status: the
// run is retried and hosted CI decides instead.
func commitStatusState(outcome localci.Outcome) (state string, publish bool) {
	switch outcome {
	case localci.OutcomePassed:
		return "success", true
	case localci.OutcomeFailed:
		return "failure", true
	}
	return "", false
}

func (s *Service) publishLocalStatuses(ctx context.Context, job registeredCIJob, result localci.Result) error {
	registry := s.ciRegistry()
	saved, err := registry.LoadHostedRuns(ctx, result.Repo, job.head)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	publicationKey := digest(string(payload))
	if saved.PublishedKey == publicationKey {
		return nil
	}
	gh := s.github(job.record.RoomID)
	for _, check := range result.Checks {
		if localInfrastructureError(result) && !registeredTestFailure(result, check) {
			continue
		}
		name := string(check.Name())
		state, publish := commitStatusState(check.Status)
		if !publish {
			continue
		}
		if _, _, err := gh.requestMethod(ctx, refRecord(job.record), fmt.Sprintf("repos/%s/statuses/%s", job.record.Repo, job.head), "POST", "-f", "context="+name, "-f", "state="+state, "-f", "description="+clip(localStatusPrefix+result.Node, 140), "-f", "target_url=https://"+job.record.Host+"/"+job.record.Repo+"/commit/"+job.head); err != nil {
			return err
		}
	}
	saved.Published = true
	saved.PublishedKey = publicationKey
	return registry.SaveHostedRuns(ctx, result.Repo, job.head, saved)
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
		class := classifyHostedSuite(snap.localCIDef, current, suite, suite != 0 && replaceableHostedWorkflow(run))
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
