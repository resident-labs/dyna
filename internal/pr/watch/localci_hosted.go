package watch

import (
	"context"
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
		if !hostedRunMatches(job, run) || !queuedHostedRun(run) {
			continue
		}
		// Fetch the current status just before cancel, since a listed job can start.
		var fresh Run
		path := fmt.Sprintf("repos/%s/actions/runs/%d", job.record.Repo, run.ID)
		if err := gh.api(ctx, refRecord(job.record), path, &fresh); err != nil {
			return err
		}
		if fresh.ID != run.ID || !hostedRunMatches(job, fresh) || !queuedHostedRun(fresh) {
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
		if current.Status != "completed" || current.Conclusion != "cancelled" {
			return nil
		}
		if _, _, err := gh.requestMethod(ctx, refRecord(job.record), path+"/rerun", "POST"); err != nil {
			return err
		}
	}
	saved.Resumed = true
	return registry.SaveHostedRuns(ctx, repo, job.head, saved)
}

func withoutLocalHostedCancellations(snap Snapshot, saved localci.HostedRuns) Snapshot {
	snap.Runs = append([]Run(nil), snap.Runs...)
	snap.Checks = append([]Check(nil), snap.Checks...)
	suites := map[int64]bool{}
	var runs []Run
	for _, run := range snap.Runs {
		attempt, ours := saved.Attempts[run.ID]
		if ours && run.Attempt <= attempt && run.Conclusion == "cancelled" {
			suites[run.CheckSuiteID] = run.CheckSuiteID != 0
			continue
		}
		runs = append(runs, run)
	}
	snap.Runs = runs
	var checks []Check
	for _, check := range snap.Checks {
		if suites[check.CheckSuite.ID] {
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
	if saved.Published {
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
	return registry.SaveHostedRuns(ctx, result.Repo, job.head, saved)
}
