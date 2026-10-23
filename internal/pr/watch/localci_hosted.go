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
			if !goAggregateCancelled(checks, current.CheckSuiteID) {
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

// goAggregateCancelled recognizes the repository's dependency-only go-test job.
// Every observed dependency must have passed or been cancelled; an independent
// failure, missing dependency group, or unknown failing job keeps hosted CI red.
func goAggregateCancelled(checks []Check, suite int64) bool {
	groups := map[string]bool{}
	cancelled, aggregate := false, false
	for _, check := range checks {
		if check.CheckSuite.ID != suite || check.App.Slug != "github-actions" {
			continue
		}
		if check.Name == "go-test" {
			aggregate = check.Status == "completed" && check.Conclusion == "failure"
			continue
		}
		group := check.Name
		if strings.HasPrefix(group, "go-test-shard (") {
			group = "go-test-shard"
		}
		switch group {
		case "go-test-shard", "go-static", "frontend", "frontend-check":
			if check.Status != "completed" || (check.Conclusion != "success" && check.Conclusion != "cancelled") {
				return false
			}
			groups[group] = true
			cancelled = cancelled || check.Conclusion == "cancelled"
		default:
			if check.Status == "completed" && !success(check.Conclusion) && check.Conclusion != "cancelled" {
				return false
			}
		}
	}
	return aggregate && cancelled && len(groups) == 4
}

func withoutLocalHostedCancellations(snap Snapshot, saved localci.HostedRuns) Snapshot {
	current := snap.currentSuiteChecks()
	cancelledSuites, aggregates, ignoredSuites := map[int64]bool{}, map[int64]bool{}, map[int64]bool{}
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
		aggregate := replaceableHostedWorkflow(run) && goAggregateCancelled(current, suite)
		aggregates[suite] = aggregate
		realFailure := false
		for _, check := range current {
			if check.CheckSuite.ID == suite && check.Status == "completed" && !success(check.Conclusion) && check.Conclusion != "cancelled" && !(aggregate && check.Name == "go-test") {
				realFailure = true
			}
		}
		// Cancellation can leave the run active, cancelled, or failed after its
		// aggregate runs. None is a test failure when only cancelled work caused it.
		if !realFailure && (run.Conclusion == "cancelled" || aggregate) {
			ignoredSuites[suite] = true
			continue
		}
		runs = append(runs, run)
	}
	snap.Runs = runs
	var checks []Check
	for _, check := range snap.Checks {
		suite := check.CheckSuite.ID
		if check.App.Slug == "github-actions" && (ignoredSuites[suite] || cancelledSuites[suite] && (check.Conclusion == "cancelled" || (aggregates[suite] && check.Name == "go-test"))) {
			continue
		}
		checks = append(checks, check)
	}
	snap.Checks = checks
	return snap
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
		name := check.CheckName
		if name == "" {
			name = check.Suite
		}
		state := "pending"
		switch check.Status {
		case "passed":
			state = "success"
		case "failed":
			state = "failure"
		case "error":
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
