package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"dyna-agent/internal/localci"
)

type registeredCIObservation struct {
	Status, Key, Logs string
	Reason            string
	BudgetNote        string
	Jobs              []string
	Runs              []Run
	Checks            []CheckSummary
}

func conclusiveCI(status string) bool { return status == "passed" || status == "failed" }
func registeredResultComplete(result localci.Result) bool {
	return result.Node != "" && (result.Status == "error" || len(result.Checks) > 0 && conclusiveCI(result.Status))
}
func localInfrastructureError(result localci.Result) bool {
	if result.Status == "error" {
		return true
	}
	for _, check := range result.Checks {
		if check.Status == "error" {
			return true
		}
	}
	return false
}
func registeredResultStatus(result localci.Result) (string, []string, string) {
	var jobs, logs []string
	for _, check := range result.Checks {
		if check.Status != "failed" || !registeredTestFailure(result, check) {
			continue
		}
		name := check.CheckName
		if name == "" {
			name = check.Suite
		}
		jobs = append(jobs, name)
		logs = append(logs, name+": "+check.Error+"\n"+check.LogTail+"\n"+strings.Join(check.Failures, "\n"))
	}
	if len(jobs) > 0 {
		return "failed", jobs, strings.Join(logs, "\n")
	}
	if !registeredResultComplete(result) || localInfrastructureError(result) {
		return "pending", nil, ""
	}
	for _, check := range result.Checks {
		if check.Status != "passed" {
			return "pending", nil, ""
		}
	}
	return result.Status, nil, ""
}
func registeredCheckSummaries(result localci.Result) []CheckSummary {
	var out []CheckSummary
	for _, check := range result.Checks {
		name := check.CheckName
		if name == "" {
			name = check.Suite
		}
		state, status, conclusion := "pending", "in_progress", ""
		switch check.Status {
		case "passed":
			state, status, conclusion = "success", "completed", "success"
		case "failed", "error":
			state, status, conclusion = "failure", "completed", "failure"
		}
		excerpt := failureExcerpt(check.Error+"\n"+strings.Join(check.Failures, "\n")+"\n"+check.LogTail, true)
		if state != "failure" {
			excerpt = ""
		}
		out = append(out, CheckSummary{Name: name, Node: result.Node, State: state, Status: status, Conclusion: conclusion, DurationMs: check.Duration.Milliseconds(), Completed: result.Started.Add(result.Duration).UTC().Format(time.RFC3339Nano), FailureExcerpt: excerpt, FailureExcerptChecked: true})
	}
	return out
}

func registeredTestFailure(result localci.Result, check localci.SuiteResult) bool {
	if check.Status != "failed" {
		return false
	}
	if check.CheckName != "go-test" || !localInfrastructureError(result) {
		return true
	}
	for _, other := range result.Checks {
		if other.CheckName != "go-test" && other.Status == "failed" {
			return true
		}
	}
	return false
}

func applyLocalBudgets(result *localci.Result, policy ChangePolicy) string {
	warn, fail, err := policy.CIBudget()
	if err != nil {
		return ""
	}
	maxTest, allowed, err := policy.TestBudget()
	if err != nil {
		return ""
	}
	var notes []string
	result.Checks = append([]localci.SuiteResult(nil), result.Checks...)
	for i := range result.Checks {
		check := &result.Checks[i]
		if check.Duration > warn && policy.CIBudgetApplies("CI") {
			notes = append(notes, fmt.Sprintf("%s ran for %s", check.CheckName, check.Duration))
			if policy.EnforceCITimeBudget() && check.Status == "passed" && check.Duration > fail {
				check.Status = "failed"
				check.Error = fmt.Sprintf("over CI time budget (%s > %s)", check.Duration, fail)
				result.Status = "failed"
			}
		}
		for _, test := range check.SlowTests {
			if test.Duration <= maxTest || allowed[test.Name] {
				continue
			}
			notes = append(notes, fmt.Sprintf("%s ran for %s; the test limit is %s", test.Name, test.Duration, maxTest))
			if policy.EnforceCITimeBudget() && check.Status == "passed" {
				check.Status = "failed"
				check.Error = "tests over " + maxTest.String()
				result.Status = "failed"
			}
		}
	}
	goFailed := false
	for _, check := range result.Checks {
		if check.Status == "failed" && (check.CheckName == "frontend" || check.CheckName == "frontend-check" || check.CheckName == "go-static" || strings.HasPrefix(check.CheckName, "go-test-shard (")) {
			goFailed = true
		}
	}
	if goFailed {
		for i := range result.Checks {
			if result.Checks[i].CheckName == "go-test" && result.Checks[i].Status == "passed" {
				result.Checks[i].Status = "failed"
				result.Checks[i].Error = "a required local CI check failed"
			}
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return "Local CI on " + result.Node + ": " + clip(strings.Join(notes, "; "), 3000)
}

// withRegisteredCI keeps the hosted and local evidence separate. A provider's
// first conclusive pass releases the wait; either provider's failure vetoes it.
func (s *Service) withRegisteredCI(ctx context.Context, r Record, snap Snapshot, policy ChangePolicy) (Snapshot, error) {
	if changeKind(r) != "github" || !strings.EqualFold(snap.Pull.Head.Repo.FullName, r.Repo) || snap.Pull.Head.SHA != r.Head {
		return snap, nil
	}
	registry := s.ciRegistry()
	mode, err := registry.Mode(ctx, r.RoomID)
	if err != nil {
		return snap, err
	}
	enabled := false
	if mode == localci.ModeLocal || mode == localci.ModeBoth {
		nodes, err := registry.Nodes(ctx)
		if err != nil {
			return snap, err
		}
		for _, node := range nodes {
			enabled = enabled || node.Enabled && node.Slots > 0
		}
	}
	if r.RepoPath == "" {
		return snap, nil
	}
	repo, err := localci.CanonicalRepo(ctx, r.RepoPath)
	if err != nil {
		if !enabled {
			return snap, nil
		}
		return snap, err
	}
	saved, err := registry.LoadHostedRuns(ctx, repo, r.Head)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return snap, err
	}
	if !enabled {
		if len(saved.IDs) == 0 {
			return snap, nil
		}
		job := registeredCIJob{record: r, head: r.Head, branch: r.Branch, trial: strings.HasPrefix(r.Branch, "dyna/merge-trial/")}
		if err := s.resumeHostedCI(ctx, job, repo); err != nil {
			return snap, err
		}
		hosted := withoutLocalHostedCancellations(snap, saved)
		ci, key, jobs, runs := hosted.CIStatus(policy.RequiredChecks...)
		snap.localCI = &registeredCIObservation{Status: ci, Key: key, Jobs: jobs, Runs: runs, Reason: hostedFallbackReason(hosted)}
		return snap, nil
	}
	hosted := withoutLocalHostedCancellations(snap, saved)
	hostedState, hostedKey, hostedJobs, hostedRuns := s.registeredHostedStatus(hosted, r, policy)
	result, err := registry.LoadResult(ctx, repo, r.Head)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return snap, err
	}
	if result.Repo != repo || result.SHA != r.Head {
		result = localci.Result{}
	}
	budgetNote := applyLocalBudgets(&result, policy)
	localState, localJobs, logs := registeredResultStatus(result)
	if result.Status == "passed" && !s.registeredJobResultComplete(ctx, registeredCIJob{record: r, head: r.Head, branch: r.Branch, trial: strings.HasPrefix(r.Branch, "dyna/merge-trial/")}, result) {
		localState = "pending"
	}
	observation := chooseRegisteredCI(r.Head, hostedState, hostedKey, hostedJobs, hostedRuns, localState, localJobs, logs)
	if observation.Status == "pending" {
		observation.Reason = "waiting for local CI"
		if localInfrastructureError(result) {
			observation.Reason = hostedFallbackReason(hosted)
		} else {
			runs := s.nodeRuns()
			runs.Lock()
			if run := runs.runs[registeredJobKey(registeredCIJob{record: r, head: r.Head})]; run != nil {
				observation.Reason = "running local CI on " + run.node
			}
			runs.Unlock()
		}
	}
	if observation.Status == "passed" {
		missing := missingRequiredCIChecks(policy.RequiredChecks, result, hosted)
		if len(missing) > 0 {
			observation.Status = "pending"
			observation.Key = "required:" + r.Head + ":" + digest(strings.Join(missing, "\n"))
			observation.Jobs = missing
			observation.Reason = "waiting for required CI checks: " + strings.Join(missing, ", ")
		}
	}
	observation.Checks = registeredCheckSummaries(result)
	observation.BudgetNote = budgetNote
	snap.localCI = observation
	return snap, nil
}

func (s *Service) mergeLocalCIChecks(ctx context.Context, st State, roomID string, out *MergeControlStatus) error {
	out.LocalCIChecks = map[string][]CheckSummary{}
	for _, repoKey := range controlRepositories(st, roomID) {
		batch := st.MergeTrials[repoKey]
		if len(batch.Changes) == 0 {
			continue
		}
		r := st.Records[batch.Changes[0].Key]
		if r == nil || r.RepoPath == "" {
			continue
		}
		repo, err := localci.CanonicalRepo(ctx, r.RepoPath)
		if err != nil {
			continue
		}
		heads := []string{batch.Head}
		for _, stack := range batch.Stacks {
			heads = append(heads, stack.Head)
		}
		for _, head := range heads {
			if head == "" {
				continue
			}
			result, err := s.ciRegistry().LoadResult(ctx, repo, head)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if result.Node != "" {
				out.LocalCIChecks[head] = registeredCheckSummaries(result)
			}
		}
	}
	return nil
}

// Either provider can finish first. A failed current attempt vetoes a pass.
func chooseRegisteredCI(head, hostedState, hostedKey string, hostedJobs []string, hostedRuns []Run, localState string, localJobs []string, logs string) *registeredCIObservation {
	o := &registeredCIObservation{Status: "pending", Key: "race-wait:" + head}
	switch {
	case localState == "failed":
		o.Status, o.Jobs, o.Logs = "failed", localJobs, logs
		o.Key = "local-failed:" + head
	case hostedState == "failed":
		o.Status, o.Jobs, o.Runs = "failed", hostedJobs, hostedRuns
		o.Key = hostedKey
	case localState == "passed":
		o.Status, o.Key = "passed", "local-passed:"+head
	case hostedState == "passed":
		o.Status, o.Key = "passed", hostedKey
	}
	return o
}

func requiredRegisteredChecks(suites []string) []string {
	var checks []string
	for _, suite := range suites {
		if suite != "go" {
			checks = append(checks, suite)
			continue
		}
		checks = append(checks, "frontend", "frontend-check", "go-static", "go-test")
		for n := 0; n < 8; n++ {
			checks = append(checks, fmt.Sprintf("go-test-shard (%d)", n))
		}
	}
	if len(checks) == 0 {
		checks = []string{"changes"}
	}
	return checks
}
func coversRegisteredChecks(result localci.Result, required []string) bool {
	if !registeredResultComplete(result) {
		return false
	}
	if result.Status != "passed" {
		return true
	}
	seen := map[string]bool{}
	for _, check := range result.Checks {
		seen[check.CheckName] = check.Status == "passed"
	}
	for _, name := range required {
		if !seen[name] {
			return false
		}
	}
	return true
}
func (s *Service) registeredJobResultComplete(ctx context.Context, job registeredCIJob, result localci.Result) bool {
	if result.Status == "error" && result.Node != "" {
		return true
	}
	if result.Status == "failed" && result.Node != "" {
		return true
	}
	var suites []string
	if job.trial {
		suites = []string{"relay", "go", "cross-build", "computer-smoke", "android"}
	} else {
		if !headSHAPattern.MatchString(job.record.Base) {
			return false
		}
		files, err := gitRead(ctx, job.record.RepoPath, "diff", "--name-only", job.record.Base+"..."+job.head)
		if err != nil {
			return false
		}
		suites = registeredSuites(strings.Split(files, "\n"))
	}
	return coversRegisteredChecks(result, requiredRegisteredChecks(suites))
}

// A finished failing check is conclusive even while another hosted job runs.
func (s *Service) registeredHostedStatus(snap Snapshot, r Record, policy ChangePolicy) (status, key string, jobs []string, runs []Run) {
	var hostedStatuses []Status
	for _, v := range snap.Statuses {
		if !localCommitStatus(v) {
			hostedStatuses = append(hostedStatuses, v)
		}
	}
	snap.Statuses = hostedStatuses
	status, key, jobs, runs = s.ciStatus(snap, r, policy)
	if status != "pending" {
		return
	}
	for _, check := range snap.currentChecks() {
		if ciCheck(check, policy.RequiredChecks) && checkState(check.Status, check.Conclusion) == "failure" {
			status = "failed"
			return
		}
	}
	for _, run := range currentRuns(snap.Runs) {
		if run.Status == "completed" && run.Conclusion != "" && run.Conclusion != "cancelled" && !success(run.Conclusion) {
			status = "failed"
			return
		}
	}
	latest := map[string]Status{}
	for _, v := range snap.Statuses {
		if old, ok := latest[v.Context]; !ok || v.ID > old.ID {
			latest[v.Context] = v
		}
	}
	for _, v := range latest {
		if !localCommitStatus(v) && requiredCheck(v.Context, policy.RequiredChecks) && (v.State == "failure" || v.State == "error") {
			status = "failed"
			return
		}
	}
	return
}

// A provider's pass cannot replace an extra required check that never passed.
// Posted local statuses stay separate from current hosted check/status evidence.
func missingRequiredCIChecks(required []string, result localci.Result, hosted Snapshot) []string {
	passed := map[string]bool{}
	if result.Node != "" {
		for _, check := range result.Checks {
			name := check.CheckName
			if name == "" {
				name = check.Suite
			}
			if check.Status == "passed" {
				passed[name] = true
			}
		}
	}
	for _, check := range hosted.currentChecks() {
		if checkState(check.Status, check.Conclusion) == "success" {
			passed[check.Name] = true
		}
	}
	latest := map[string]Status{}
	for _, v := range hosted.Statuses {
		if localCommitStatus(v) {
			continue
		}
		if old, ok := latest[v.Context]; !ok || v.ID > old.ID {
			latest[v.Context] = v
		}
	}
	for name, status := range latest {
		if status.State == "success" {
			passed[name] = true
		}
	}
	var missing []string
	for _, name := range required {
		if !passed[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

func hostedFallbackReason(snap Snapshot) string {
	for _, run := range currentRuns(snap.Runs) {
		if run.Status == "in_progress" {
			return "running hosted CI fallback"
		}
	}
	return "waiting for hosted CI fallback"
}
