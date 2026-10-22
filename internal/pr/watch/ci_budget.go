package watch

import (
	"cmp"
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"dyna-agent/internal/cihealth"
	"dyna-agent/internal/pr/state"
)

type budgetStep struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Name       string `json:"name"`
	Started    string `json:"started_at"`
	Completed  string `json:"completed_at"`
}
type budgetJob struct {
	ID         int64        `json:"id"`
	Status     string       `json:"status"`
	Conclusion string       `json:"conclusion"`
	Name       string       `json:"name"`
	Created    string       `json:"created_at"`
	Started    string       `json:"started_at"`
	Completed  string       `json:"completed_at"`
	Steps      []budgetStep `json:"steps"`
}

func runToken(run Run) string { return fmt.Sprintf("%d/%d", run.ID, run.Attempt) }

// CancelRun cancels only pull request runs. Push runs, including main, are never eligible.
func (g GitHub) CancelRun(ctx context.Context, ref Ref, run Run) error {
	if run.Event != "pull_request" {
		return fmt.Errorf("refusing to cancel a non-pull_request run")
	}
	_, _, err := g.requestMethod(ctx, ref, fmt.Sprintf("repos/%s/actions/runs/%d/cancel", ref.Repo, run.ID), "POST")
	return err
}

func budgetFailure(r Record, snap Snapshot, policy ChangePolicy) (string, string) {
	if !policy.EnforceCITimeBudget() {
		return "", ""
	}
	// Preserve enforcement failures on every cancellation skipped before
	// replacement evidence. A duplicate cancellation cannot erase a failure.
	selected, skipped := runEvidence(snap.Runs)
	runs := append(skipped, selected...)
	for _, run := range runs {
		if run.Event == "pull_request" && (run.HeadSHA == "" || run.HeadSHA == snap.Pull.Head.SHA) {
			if reason := r.CITestRuns[runToken(run)]; r.CITestHead == snap.Pull.Head.SHA && strings.HasPrefix(reason, "tests over ") {
				return reason, runToken(run)
			}
		}
		if reason := r.CIBudgetRuns[runToken(run)]; r.CIBudgetHead == snap.Pull.Head.SHA && strings.HasPrefix(reason, "over CI time budget") {
			return reason, runToken(run)
		}
	}
	return "", ""
}

func (s *Service) enforceCIBudget(ctx context.Context, gh GitHub, r Record, snap Snapshot, policy ChangePolicy) error {
	if snap.Pull.State == "closed" {
		return nil
	}
	warn, fail, err := policy.CIBudget()
	if err != nil {
		return err
	}
	for _, run := range currentRuns(snap.Runs) {
		if run.Event != "pull_request" || !policy.CIBudgetApplies(run.Name) {
			continue
		}
		token := runToken(run)
		previous := ""
		if r.CIBudgetHead == snap.Pull.Head.SHA {
			previous = r.CIBudgetRuns[token]
		}
		overBefore := strings.HasPrefix(previous, "over CI time budget")
		input := budgetInputVersion(s.Store.Root, gh, r, snap, run, policy)
		if input != "" && r.CIBudgetHead == snap.Pull.Head.SHA && r.CIBudgetInputs[token] == input &&
			r.CIBudgetErrors[token] == "" && !gh.requests.forceBudgets {
			continue
		}
		outcome := budgetResult{}
		runJobs, err := gh.runJobs(ctx, refRecord(r), run)
		if err != nil {
			outcome.setError(r, token, err)
			if err := s.saveBudgetResult(r, snap, policy, token, outcome); err != nil {
				return err
			}
			continue
		}
		var intervals, queued []busyInterval
		var times []jobTime
		type timing struct {
			name    string
			elapsed time.Duration
			step    bool
		}
		var timings []timing
		// stored keeps the same jobs for the CI health view: one row each,
		// with the steps that actually ran. A queued job is kept too, so a
		// run that waited for a runner shows where the time went.
		var stored []cihealth.Job
		now := s.now()
		for _, job := range runJobs {
			jobRow := cihealth.Job{ID: job.ID, Name: Sanitize(job.Name), Status: job.Status, Conclusion: job.Conclusion}
			if at, err := time.Parse(time.RFC3339, job.Started); err == nil {
				jobRow.Started = at
			}
			if at, err := time.Parse(time.RFC3339, job.Completed); err == nil {
				jobRow.Completed = at
			}
			if job.Status == "queued" || job.Status == "waiting" || job.Status == "pending" || job.Conclusion == "skipped" {
				stored = append(stored, jobRow)
				continue
			}
			if at, err := time.Parse(time.RFC3339, job.Started); err == nil && !at.IsZero() && !at.After(now) {
				if created, err := time.Parse(time.RFC3339, job.Created); err == nil && at.After(created) {
					queued = append(queued, busyInterval{created, at})
				}
			}
			// GitHub can leave a job open long after its runner has finished.
			// Only step timestamps prove execution. Gaps and empty step lists
			// add nothing, even if the job's started_at is hours old.
			var executed []busyInterval
			until := now
			if end, err := time.Parse(time.RFC3339, job.Completed); err == nil && end.Before(until) {
				until = end
			}
			for _, step := range job.Steps {
				if runnerOwnedSteps[step.Name] {
					continue
				}
				if interval, ok := stepExecution(step, until); ok {
					executed = append(executed, interval)
					intervals = append(intervals, interval)
					timings = append(timings, timing{Sanitize(job.Name + " / " + step.Name), interval.end.Sub(interval.start), true})
					jobRow.Steps = append(jobRow.Steps, cihealth.Step{Name: Sanitize(step.Name), Duration: interval.end.Sub(interval.start)})
				}
			}
			elapsed := busyTime(executed)
			if elapsed > 0 {
				times = append(times, jobTime{job.Name, elapsed})
				timings = append(timings, timing{Sanitize(job.Name), elapsed, false})
			}
			jobRow.Busy = elapsed
			if elapsed > 0 || len(jobRow.Steps) > 0 {
				stored = append(stored, jobRow)
			}
		}
		wait := busyTime(queued).Round(time.Second).String()
		outcome.queue = &wait
		// A dependency chain runs one job at a time, so it never exceeds the busy
		// time. Read the workflow only when the cheap bound reaches the warning.
		// If the jobs cannot be mapped to the workflow, keep the busy time and note why.
		busy := busyTime(intervals)
		measured := true
		if busy >= warn {
			graph, err := gh.workflowNeeds(ctx, refRecord(r), run)
			if err != nil {
				// Busy time only bounds the chain from above. Without the
				// workflow, the verdict is unknown and nothing is enforced.
				outcome.setError(r, token, fmt.Errorf("reading the workflow: %w", err))
				outcome.reason, outcome.quiet = "budget unknown", true
				if err := s.saveBudgetResult(r, snap, policy, token, outcome); err != nil {
					return err
				}
				continue
			}
			path, err := criticalPath(times, graph)
			if err != nil {
				measured = false
				outcome.setError(r, token, fmt.Errorf("measuring busy time instead of the critical path: %w", err))
			} else {
				busy = path
			}
		}
		if measured {
			outcome.setError(r, token, nil)
			if completedBudgetJobs(runJobs, now) {
				outcome.input = budgetInputVersion(s.Store.Root, gh, r, snap, run, policy)
			}
		}
		s.saveCIRun(ctx, r, run, warn, fail, busy, busyTime(queued), stored)
		// A run judged over budget by the old measure now fits: clear that verdict.
		if busy < warn && (overBefore || previous == "warned") {
			outcome.reason = "within budget"
		}
		if busy < warn {
			if err := s.saveBudgetResult(r, snap, policy, token, outcome); err != nil {
				return err
			}
			continue
		}
		sort.SliceStable(timings, func(i, j int) bool { return timings[i].elapsed > timings[j].elapsed })
		var slow []string
		jobs, steps := 0, 0
		for _, item := range timings {
			if item.step {
				if steps == 3 {
					continue
				}
				steps++
			} else {
				if jobs == 3 {
					continue
				}
				jobs++
			}
			slow = append(slow, fmt.Sprintf("%s (%s)", item.name, budgetWords(item.elapsed.Round(time.Second))))
		}
		reason, text := "warned", fmt.Sprintf("CI for %s has run %s; the budget is %s. Slowest jobs/steps so far: %s", r.URL, budgetWords(warn), budgetWords(fail), strings.Join(slow, "; "))
		if busy >= fail && policy.EnforceCITimeBudget() {
			reason = fmt.Sprintf("over CI time budget (%s)", compactDuration(fail))
			action := "was cancelled"
			if overBefore && r.CIBudgetSteps[token] {
				// Keep the durable failure and its notice after cancellation.
				reason = previous
			} else if run.Status == "completed" {
				reason = "over CI time budget (finished)"
				action = "finished over budget"
			} else {
				observed, err := state.ReadSnapshot(s.Store.Root)
				if err != nil {
					return err
				}
				live, ok := observed.Record(r.Key)
				if !ok || !budgetRecordMatches(observed.Metadata(), &live, r, policy) {
					continue
				}
				if err := gh.CancelRun(ctx, refRecord(r), run); err != nil {
					outcome.setError(r, token, err)
					outcome.input = ""
					reason = "over CI time budget (cancel failed)"
					action = "exceeded its budget; cancellation failed"
				}
			}
			text = fmt.Sprintf("CI for %s %s: %s. Slowest jobs/steps: %s", r.URL, action, reason, strings.Join(slow, "; "))
		} else if previous == "warned" {
			if err := s.saveBudgetResult(r, snap, policy, token, outcome); err != nil {
				return err
			}
			continue
		}
		outcome.reason, outcome.text, outcome.quiet = reason, text, !policy.EnforceCITimeBudget()
		if err := s.saveBudgetResult(r, snap, policy, token, outcome); err != nil {
			return err
		}
	}
	return nil
}

// storeBudget saves a verdict through the same guarded writer as measurements.
func (s *Service) storeBudget(r Record, snap Snapshot, token, reason, text string, quiet bool) error {
	return s.Store.updateRecords([]string{r.Key}, func(st *State) error {
		live := st.Records[r.Key]
		if live == nil || live.Head != r.Head {
			return errNoStoreChange
		}
		applyBudgetVerdict(live, snap, token, reason, text, quiet)
		return nil
	})
}

// applyBudgetVerdict is shared by direct verdicts and prepared measurements.
func applyBudgetVerdict(live *Record, snap Snapshot, token, reason, text string, quiet bool) {
	startBudgetHead(live, snap.Pull.Head.SHA)
	if live.CIBudgetRuns == nil {
		live.CIBudgetRuns = map[string]string{}
	}
	live.CIBudgetRuns[token] = reason
	if live.CIBudgetSteps == nil {
		live.CIBudgetSteps = map[string]bool{}
	}
	live.CIBudgetSteps[token] = true
	if reason != "warned" {
		delete(live.CIBudgetNotes, token)
	}
	if reason == "warned" {
		if live.CIBudgetNotes == nil {
			live.CIBudgetNotes = map[string]string{}
		}
		live.CIBudgetNotes[token] = text
		if quiet {
			view := *live
			view.Head = snap.Pull.Head.SHA
			notice(&view, live.Author, text, "informational:ci-budget:"+snap.Pull.Head.SHA+":"+token)
			live.Notices = view.Notices
		}
		return
	}
	if !strings.HasPrefix(reason, "over CI time budget") {
		return
	}
	// The snapshot can observe a new head before process resets review state.
	// Format this notice for that head without changing the old review head.
	view := *live
	view.Head = snap.Pull.Head.SHA
	notice(&view, live.Author, text, "ci-budget:"+snap.Pull.Head.SHA+":"+token+":"+reason)
	live.Notices = view.Notices
}

// startBudgetHead clears the budget state when the pull request has a new head.
func startBudgetHead(live *Record, sha string) {
	if live.CIBudgetHead == sha {
		return
	}
	live.CIBudgetHead = sha
	live.CIBudgetRuns = map[string]string{}
	live.CIBudgetNotes = map[string]string{}
	live.CIBudgetErrors = map[string]string{}
	live.CIQueueWait = map[string]string{}
	live.CIBudgetSteps = map[string]bool{}
	live.CIBudgetInputs = map[string]string{}
}
func compactDuration(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}
func budgetWords(d time.Duration) string {
	if d == time.Minute {
		return "1 minute"
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d minutes", d/time.Minute)
	}
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + " seconds"
}

// runnerOwnedSteps are added by the runner, not the workflow. Their time follows
// runner load, so it is not the pull request's cost. "Set up runner" and
// "Complete runner" run the host's job hooks, where a job waits for a go-test
// slot (scripts/ci-runner-slot.sh).
var runnerOwnedSteps = map[string]bool{"Set up job": true, "Set up runner": true, "Complete runner": true, "Complete job": true}

// stepExecution counts an open interval only when GitHub says the step is running.
// A completed or pending step without an end is not evidence of current execution.
func stepExecution(step budgetStep, now time.Time) (busyInterval, bool) {
	if step.Status == "queued" || step.Status == "waiting" || step.Status == "pending" || step.Conclusion == "skipped" {
		return busyInterval{}, false
	}
	start, err := time.Parse(time.RFC3339, step.Started)
	if err != nil || start.IsZero() || !start.Before(now) {
		return busyInterval{}, false
	}
	end, err := time.Parse(time.RFC3339, step.Completed)
	if err != nil {
		if step.Completed != "" || step.Status != "in_progress" {
			return busyInterval{}, false
		}
		end = now
	}
	if end.After(now) {
		end = now
	}
	return busyInterval{start, end}, end.After(start)
}

// currentRuns first keeps the latest attempt of each run. A cancelled duplicate
// can yield to another run of the same workflow, head, and event. A cancelled
// rerun attempt remains pending instead of reviving an earlier attempt.
func currentRuns(runs []Run) []Run {
	selected, _ := runEvidence(runs)
	return selected
}

// runEvidence keeps the skipped cancellation chain for budget enforcement.
// Selection stops at the first non-cancelled run or cancelled rerun attempt.
func runEvidence(runs []Run) (selected, skipped []Run) {
	attempts := map[int64]Run{}
	for _, r := range runs {
		if old, ok := attempts[r.ID]; !ok || r.Attempt > old.Attempt {
			attempts[r.ID] = r
		}
	}
	type workflowKey struct {
		id                int64
		name, head, event string
	}
	groups := map[workflowKey][]Run{}
	for _, r := range attempts {
		name := ""
		if r.WorkflowID == 0 {
			name = r.Name
		}
		key := workflowKey{r.WorkflowID, name, r.HeadSHA, r.Event}
		groups[key] = append(groups[key], r)
	}
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool { return group[i].ID > group[j].ID })
		i := 0
		for i < len(group) && group[i].Conclusion == "cancelled" && group[i].Attempt <= 1 {
			i++
		}
		if i == len(group) {
			// With no replacement evidence, retain the newest cancellation.
			selected = append(selected, group[0])
			skipped = append(skipped, group[1:]...)
		} else {
			selected = append(selected, group[i])
			skipped = append(skipped, group[:i]...)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].ID < skipped[j].ID })
	return selected, skipped
}

type jobTime struct {
	name    string
	elapsed time.Duration
}

// workflowJob is one job of the workflow file: a pattern for its API names and the job IDs it needs.
type workflowJob struct {
	name  *regexp.Regexp
	needs []string
}

var expression = regexp.MustCompile(`\$\{\{.*?\}\}`)

// namePattern matches the API names of a job: its display name, with each ${{ }}
// expression as a wildcard, and an optional matrix suffix " (...)".
func namePattern(display string) *regexp.Regexp {
	parts := expression.Split(display, -1)
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".+?") + `(?: \(.*\))?$`)
}

// criticalPath is the longest chain of job run times through the needs graph.
// Run times contain only runner step execution, without dispatch or reporting gaps.
// Each API job must map to exactly one workflow job. Parallel instances of that job,
// such as matrix entries, count once, at the longest. A name that maps to none or
// several is an error: guessing would invent or sum chains.
func criticalPath(times []jobTime, jobs map[string]workflowJob) (time.Duration, error) {
	elapsed := map[string]time.Duration{}
	for _, t := range times {
		var keys []string
		for key, job := range jobs {
			if job.name.MatchString(t.name) {
				keys = append(keys, key)
			}
		}
		if len(keys) != 1 {
			return 0, fmt.Errorf("cannot map job %q to one workflow job (%d matches)", t.name, len(keys))
		}
		elapsed[keys[0]] = max(elapsed[keys[0]], t.elapsed)
	}
	done := map[string]time.Duration{}
	var finish func(key string, depth int) time.Duration
	finish = func(key string, depth int) time.Duration {
		if d, ok := done[key]; ok || depth > len(jobs) {
			return d
		}
		var before time.Duration
		for _, need := range jobs[key].needs {
			before = max(before, finish(need, depth+1))
		}
		done[key] = before + elapsed[key]
		return done[key]
	}
	var longest time.Duration
	for key := range elapsed {
		longest = max(longest, finish(key, 0))
	}
	return longest, nil
}

// workflowNeeds reads the jobs and their needs from the workflow file at the run's commit.
// The API does not return them.
func (g GitHub) workflowNeeds(ctx context.Context, ref Ref, run Run) (map[string]workflowJob, error) {
	var file struct {
		Content string `json:"content"`
	}
	if err := g.api(ctx, ref, workflowPath(ref, run), &file); err != nil {
		return nil, err
	}
	body, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil {
		return nil, err
	}
	return parseWorkflowJobs(body)
}

// parseWorkflowJobs maps each job key of a workflow file to its name pattern and needs.
func parseWorkflowJobs(body []byte) (map[string]workflowJob, error) {
	var workflow struct {
		Jobs map[string]struct {
			Name  string `yaml:"name"`
			Needs any    `yaml:"needs"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		return nil, err
	}
	jobs := map[string]workflowJob{}
	for key, job := range workflow.Jobs {
		w := workflowJob{name: namePattern(cmp.Or(job.Name, key))}
		switch v := job.Needs.(type) {
		case string:
			w.needs = []string{v}
		case []any:
			for _, need := range v {
				if dep, ok := need.(string); ok {
					w.needs = append(w.needs, dep)
				}
			}
		}
		jobs[key] = w
	}
	return jobs, nil
}

// busyTime counts overlapping intervals once and excludes gaps without execution.
type busyInterval struct{ start, end time.Time }

func busyTime(intervals []busyInterval) time.Duration {
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start.Before(intervals[j].start) })
	var total time.Duration
	var end time.Time
	for _, interval := range intervals {
		start := interval.start
		if start.Before(end) {
			start = end
		}
		if interval.end.After(start) {
			total += interval.end.Sub(start)
		}
		if interval.end.After(end) {
			end = interval.end
		}
	}
	return total
}

// runJobs reads the current attempt and all pages for one workflow run.
func (g GitHub) runJobs(ctx context.Context, ref Ref, run Run) ([]budgetJob, error) {
	var pages []struct {
		Jobs []budgetJob `json:"jobs"`
	}
	if err := g.pages(ctx, ref, runJobsPath(ref, run), &pages); err != nil {
		return nil, err
	}
	var jobs []budgetJob
	for _, page := range pages {
		jobs = append(jobs, page.Jobs...)
	}
	return jobs, nil
}

// runJobsPath is shared by the reader and completed-input comparison.
func runJobsPath(ref Ref, run Run) string {
	if run.Attempt > 0 {
		return fmt.Sprintf("repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100", ref.Repo, run.ID, run.Attempt)
	}
	return fmt.Sprintf("repos/%s/actions/runs/%d/jobs?per_page=100&filter=latest", ref.Repo, run.ID)
}

func workflowPath(ref Ref, run Run) string {
	path, _, _ := strings.Cut(run.Path, "@")
	return fmt.Sprintf("repos/%s/contents/%s?ref=%s", ref.Repo, path, run.HeadSHA)
}
