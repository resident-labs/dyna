package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
)

// CIVerdict is the merge gate's reading of a commit's CI: it proves the commit,
// fails it, or has not finished. It is a different vocabulary from a localci
// Outcome, which also reports an infrastructure error that the gate treats as a
// wait. Its values reach stored records and room posts, so their spelling must
// not change.
type CIVerdict string

const (
	VerdictPending CIVerdict = "pending"
	VerdictPassed  CIVerdict = "passed"
	VerdictFailed  CIVerdict = "failed"
	// VerdictAbsent reports a commit with no CI to read.
	VerdictAbsent CIVerdict = "absent"
)

// Conclusive reports a verdict that decides the commit. Only a pass and a
// failure do; a wait never gates a merge.
func (v CIVerdict) Conclusive() bool { return v == VerdictPassed || v == VerdictFailed }

// verdictFromOutcome is the one place where a runner outcome becomes a gate
// verdict. An infrastructure error waits: the run is retried or hosted CI
// decides instead.
func verdictFromOutcome(outcome localci.Outcome) CIVerdict {
	switch outcome {
	case localci.OutcomePassed:
		return VerdictPassed
	case localci.OutcomeFailed:
		return VerdictFailed
	}
	return VerdictPending
}

// verdictFromHosted is the one place where a hosted reading (a state from
// Snapshot.CIStatus) becomes a gate verdict.
func verdictFromHosted(state string) CIVerdict {
	v := CIVerdict(state)
	switch v {
	case VerdictPassed, VerdictFailed, VerdictAbsent:
		return v
	}
	return VerdictPending
}

type registeredCIObservation struct {
	Status     CIVerdict
	Key, Logs  string
	Reason     string
	BudgetNote string
	Jobs       []string
	Runs       []Run
	Checks     []CheckSummary
}

func localInfrastructureError(result localci.Result) bool {
	if result.Status == localci.OutcomeError {
		return true
	}
	for _, check := range result.Checks {
		if check.Status == localci.OutcomeError {
			return true
		}
	}
	return false
}
func registeredResultStatus(result localci.Result) (CIVerdict, []string, string) {
	var jobs, logs []string
	for _, check := range result.Checks {
		if check.Status != localci.OutcomeFailed || !registeredTestFailure(result, check) {
			continue
		}
		name := string(check.Name())
		jobs = append(jobs, name)
		logs = append(logs, name+": "+check.Error+"\n"+check.LogTail+"\n"+strings.Join(check.Failures, "\n"))
	}
	if len(jobs) > 0 {
		return VerdictFailed, jobs, strings.Join(logs, "\n")
	}
	if !result.Complete() || localInfrastructureError(result) {
		return VerdictPending, nil, ""
	}
	for _, check := range result.Checks {
		if check.Status != localci.OutcomePassed {
			return VerdictPending, nil, ""
		}
	}
	return verdictFromOutcome(result.Status), nil, ""
}

// checkSummaryState is the one conversion from a local CI outcome to the GitHub
// check state, status and conclusion Dyna publishes.
func checkSummaryState(outcome localci.Outcome) (state, status, conclusion string) {
	switch outcome {
	case localci.OutcomePassed:
		return "success", "completed", "success"
	case localci.OutcomeFailed:
		return "failure", "completed", "failure"
	case localci.OutcomeError:
		return "skipped", "completed", "neutral"
	}
	return "pending", "in_progress", ""
}
func registeredCheckSummaries(result localci.Result) []CheckSummary {
	var out []CheckSummary
	for _, check := range result.Checks {
		name := string(check.Name())
		state, status, conclusion := checkSummaryState(check.Status)
		excerpt := failureExcerpt(check.Error+"\n"+strings.Join(check.Failures, "\n")+"\n"+check.LogTail, true)
		if state != "failure" {
			excerpt = ""
		}
		out = append(out, CheckSummary{Name: name, Node: result.Node, State: state, Status: status, Conclusion: conclusion, DurationMs: check.Duration.Milliseconds(), Completed: result.Started.Add(result.Duration).UTC().Format(time.RFC3339Nano), FailureExcerpt: excerpt, FailureExcerptChecked: true})
	}
	return out
}

func registeredTestFailure(result localci.Result, check localci.SuiteResult) bool {
	if check.Status != localci.OutcomeFailed {
		return false
	}
	if !localAggregate(result, check) || !localInfrastructureError(result) {
		return true
	}
	for _, other := range result.Checks {
		if other.Name() != check.Name() && other.Status == localci.OutcomeFailed {
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
			if policy.EnforceCITimeBudget() && check.Status == localci.OutcomePassed && check.Duration > fail {
				check.Status = localci.OutcomeFailed
				check.Error = fmt.Sprintf("over CI time budget (%s > %s)", check.Duration, fail)
				result.Status = localci.OutcomeFailed
			}
		}
		for _, test := range check.SlowTests {
			if test.Duration <= maxTest || allowed[test.Name] {
				continue
			}
			notes = append(notes, fmt.Sprintf("%s ran for %s; the test limit is %s", test.Name, test.Duration, maxTest))
			if policy.EnforceCITimeBudget() && check.Status == localci.OutcomePassed {
				check.Status = localci.OutcomeFailed
				check.Error = "tests over " + maxTest.String()
				result.Status = localci.OutcomeFailed
			}
		}
	}
	failedGroups := map[localci.Group]bool{}
	for _, check := range result.Checks {
		if check.Status == localci.OutcomeFailed && check.Group != "" {
			failedGroups[check.Group] = true
		}
	}
	for i := range result.Checks {
		check := &result.Checks[i]
		if localAggregate(*result, *check) && failedGroups[localci.Group(check.Name())] && check.Status == localci.OutcomePassed {
			check.Status = localci.OutcomeFailed
			check.Error = "a required local CI check failed"
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return "Local CI on " + result.Node + ": " + clip(strings.Join(notes, "; "), 3000)
}

func localAggregate(result localci.Result, check localci.SuiteResult) bool {
	if check.Aggregate {
		return true
	}
	// Results written before group metadata used go-test as both the suite and
	// aggregate identity. Preserve their infrastructure-error classification.
	if check.Suite == localci.SuiteGoTest && check.Name() == localci.CheckName(localci.SuiteGoTest) {
		return true
	}
	for _, other := range result.Checks {
		if other.Group != "" && other.Group.Name() == check.Name() {
			return true
		}
	}
	return false
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
	dir, dirErr := s.checkoutDir(ctx, r)
	if dirErr != nil {
		if !enabled {
			return snap, nil
		}
		return snap, dirErr
	}
	r.RepoPath = dir
	repo, err := localci.CanonicalRepo(ctx, dir)
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
	// A hosted fallback can only decide a head that has a hosted run, or one Dyna
	// cancelled and can start again. Without either, a trial decides alone.
	hostedFallback := snap.hasRealCI(policy.RequiredChecks...) || len(saved.IDs) > 0
	var job registeredCIJob
	if mode == localci.ModeLocal || mode == localci.ModeBoth || len(saved.IDs) > 0 {
		job, err = s.registeredJobForRecord(r)
		if err != nil {
			return snap, err
		}
	}
	var plan localCIPlan
	var result localci.Result
	conclusiveLocal := false
	if mode == localci.ModeLocal || mode == localci.ModeBoth {
		result, err = registry.LoadResult(ctx, repo, r.Head)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return snap, err
		}
		if result.Repo != repo || result.SHA != r.Head {
			result = localci.Result{}
		}
		conclusiveLocal = result.Node != "" && (result.Status == localci.OutcomeFailed || result.Status == localci.OutcomeError)
	}
	if mode == localci.ModeLocal || mode == localci.ModeBoth {
		if !conclusiveLocal {
			plan, err = s.planRegisteredCI(ctx, job)
			if err != nil {
				return snap, err
			}
		}
		if plan.baseMissing {
			snap.localCIConfig = state.LocalCIConfigNotConfigured
			snap = withoutLocalHostedCancellations(snap, saved, nil)
			if len(saved.IDs) > 0 {
				if err := s.resumeHostedCI(ctx, job, repo); err != nil {
					return snap, err
				}
			}
			return snap, nil
		}
		if plan.headMissing {
			snap.localCIConfig = state.LocalCIConfigHeadMissing
		}
	} else if len(saved.IDs) > 0 {
		needsDefinition := false
		for _, run := range snap.Runs {
			attempt, ours := saved.Attempts[run.ID]
			needsDefinition = needsDefinition || ours && run.Attempt <= attempt && run.CheckSuiteID != 0 && run.Conclusion != "cancelled"
		}
		if needsDefinition {
			plan.execution, err = localci.LoadDefinition(ctx, r.RepoPath, r.Head)
			if errors.Is(err, localci.ErrDefinitionNotFound) {
				err = nil
			}
			if err != nil {
				return snap, err
			}
		}
	}
	cancellationDefinition := plan.execution
	if conclusiveLocal {
		// Saved failures stay readable without their old commits. Cancellation
		// classification gets the head graph separately when that graph is available.
		cancellationDefinition, _ = localci.LoadDefinition(ctx, r.RepoPath, r.Head)
	}
	// A run Dyna cancelled for local CI never tested the head. Drop it here,
	// so no later step spends an artifact read or a log excerpt on it.
	snap = withoutLocalHostedCancellations(snap, saved, cancellationDefinition)
	if !enabled {
		// In local mode a saved local result can decide the head without a ready
		// node. A host that is briefly unavailable must not restart the hosted
		// run that local CI replaced: that wait costs a full hosted run and holds
		// merge work the local pass already released. The result is judged by the
		// same gates as a fresh one, so a hosted failure still vetoes it and a
		// partial or unproven suite cannot release a merge. GitHub mode keeps the
		// hosted fallback untouched.
		if mode == localci.ModeLocal || mode == localci.ModeBoth {
			if local, ok := savedLocalResult(ctx, registry, repo, r.Head); ok && s.savedLocalSettles(plan, local, policy) {
				snap.localCI = s.registeredLocalObservation(r, snap, policy, plan, local, job.trial, hostedFallback)
				return snap, nil
			}
		}
		if len(saved.IDs) == 0 {
			return snap, nil
		}
		if err := s.resumeHostedCI(ctx, job, repo); err != nil {
			return snap, err
		}
		ci, key, jobs, runs := snap.CIStatus(policy.RequiredChecks...)
		snap.localCI = &registeredCIObservation{Status: verdictFromHosted(ci), Key: key, Jobs: jobs, Runs: runs, Reason: hostedFallbackReason(snap)}
		return snap, nil
	}
	snap.localCI = s.registeredLocalObservation(r, snap, policy, plan, result, job.trial, hostedFallback)
	return snap, nil
}

// registeredLocalObservation judges a saved local result together with the
// hosted read. Either provider's failure vetoes a pass, a local pass needs its
// whole suite and the required checks, and a pending provider does not block a
// conclusive pass. trial reports a head of a merge trial, the only work local CI
// runs for now, and hostedFallback reports a hosted run that can still decide the
// head.
func (s *Service) registeredLocalObservation(r Record, snap Snapshot, policy ChangePolicy, plan localCIPlan, result localci.Result, trial, hostedFallback bool) *registeredCIObservation {
	hostedState, hostedKey, hostedJobs, hostedRuns := s.registeredHostedStatus(snap, r, policy)
	budgetNote := applyLocalBudgets(&result, policy)
	localState, localJobs, logs := registeredResultStatus(result)
	// Local CI runs merge trials only, so a pull request head has no run of its
	// own: without a saved result the local provider has no evidence to wait
	// for, and waiting would never end.
	if !trial && result.Node == "" {
		localState, localJobs, logs = VerdictAbsent, nil, ""
	}
	if result.Status == localci.OutcomePassed && (!plan.runnable() || !plan.required.CoversSuites(result, plan.suites)) {
		localState = VerdictPending
		if !trial {
			// A manual run on a pull request head may miss the checks the plan
			// names, for example the changes check of a docs-only change. No
			// run for that head is coming to complete it, so it is no evidence.
			localState, localJobs, logs = VerdictAbsent, nil, ""
		}
	}
	// Without a hosted run to fall back to, and with no local retry left, waiting
	// here would never end. The trial fails and names the cause instead. A run
	// Dyna stopped is retried first, and its replacement is the one that decides.
	localFailure := ""
	if trial && !hostedFallback && localInfrastructureError(result) && savedResultActionFor(result) == settleSavedResult {
		localState, localJobs, logs = VerdictFailed, localFailedChecks(result), localFailureNote(result)
		localFailure = localFailureNote(result)
	}
	// A hosted failure that only cancellations caused never tested the head, so
	// it can neither fail nor pass it: the head waits for the local result, which
	// decides once it is complete. Failing here would freeze the batch before the
	// local CI that is still queued or running gets to finish.
	if hostedState == VerdictFailed && hostedCancelledOnly(snap, policy.RequiredChecks) {
		hostedState, hostedKey, hostedJobs, hostedRuns = VerdictPending, "cancelled-only:"+r.Head, nil, nil
	}
	observation := chooseRegisteredCI(r.Head, hostedState, hostedKey, hostedJobs, hostedRuns, localState, localJobs, logs)
	if observation.Status == VerdictPending {
		observation.Reason = "waiting for local CI"
		if localState == VerdictAbsent {
			observation.Reason = "waiting for CI"
		}
		runs := s.nodeRuns()
		runs.Lock()
		live := runs.runs[registeredJobKey(registeredCIJob{record: r, head: r.Head})]
		runs.Unlock()
		switch {
		case live != nil:
			observation.Reason = "running local CI on " + live.node
		case localInfrastructureError(result):
			// No hosted provider is coming in a local-only room: name the retry
			// that really is, instead of a fallback that never arrives.
			observation.Reason = hostedFallbackReason(snap)
			if !hostedFallback {
				// Name the retry that really is coming, not a fallback that is not.
				observation.Reason = "retrying local CI"
			}
		}
	}
	if observation.Status == VerdictPassed || observation.Status == VerdictPending && result.Status == localci.OutcomePassed {
		required := append([]string(nil), policy.RequiredChecks...)
		// Local CI runs merge trials only, so a pull request head has no local
		// run to supply the names the base definition requires. Only a trial head
		// must cover them; the hosted provider decides a pull request head.
		if trial && plan.required != nil {
			for _, name := range plan.required.RequiredChecks(plan.suites) {
				if !slices.Contains(required, string(name)) {
					required = append(required, string(name))
				}
			}
		}
		missing := missingRequiredCIChecks(required, result, snap)
		if len(missing) > 0 {
			observation.Status = VerdictPending
			observation.Key = "required:" + r.Head + ":" + digest(strings.Join(missing, "\n"))
			observation.Jobs = missing
			observation.Reason = "waiting for required CI checks: " + strings.Join(missing, ", ")
		}
	}
	observation.Checks = registeredCheckSummaries(result)
	observation.BudgetNote = budgetNote
	if localFailure != "" {
		observation.Reason = localFailure
	}
	return observation
}

// savedLocalSettles reports whether a saved local result can decide a head with
// no ready node. A conclusive failure always can; a pass must cover the whole
// suite this job runs, so a manual subset run cannot release a merge.
func (s *Service) savedLocalSettles(plan localCIPlan, result localci.Result, policy ChangePolicy) bool {
	budgeted := result
	applyLocalBudgets(&budgeted, policy)
	state, _, _ := registeredResultStatus(budgeted)
	if !state.Conclusive() {
		return false
	}
	return state != VerdictPassed || plan.runnable() && plan.required.CoversSuites(budgeted, plan.suites)
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
func chooseRegisteredCI(head string, hostedState CIVerdict, hostedKey string, hostedJobs []string, hostedRuns []Run, localState CIVerdict, localJobs []string, logs string) *registeredCIObservation {
	o := &registeredCIObservation{Status: VerdictPending, Key: "race-wait:" + head}
	switch {
	case localState == VerdictFailed:
		o.Status, o.Jobs, o.Logs = VerdictFailed, localJobs, logs
		o.Key = "local-failed:" + head
	case hostedState == VerdictFailed:
		o.Status, o.Jobs, o.Runs = VerdictFailed, hostedJobs, hostedRuns
		o.Key = hostedKey
	case localState == VerdictAbsent && hostedState == VerdictAbsent:
		// Neither provider has anything to wait for: this head has no CI to read.
		// A wait here never ends, so the change reports absent and the merge trial
		// decides it instead.
		o.Status, o.Key = VerdictAbsent, "absent:"+head
	case localState == VerdictPassed:
		o.Status, o.Key = VerdictPassed, "local-passed:"+head
	case hostedState == VerdictPassed:
		o.Status, o.Key = VerdictPassed, hostedKey
	}
	return o
}

func (s *Service) registeredJobResultComplete(ctx context.Context, job registeredCIJob, result localci.Result) bool {
	if result.Status == localci.OutcomeError && result.Node != "" && stoppedLocalResult(result) {
		// Dyna stopped this run itself, so it tested nothing. savedResultActionFor
		// admits the head again, and Result.Resumed bounds that to one retry.
		return true
	}
	if result.Status == localci.OutcomeError && result.Node != "" {
		return true
	}
	if result.Status == localci.OutcomeFailed && result.Node != "" {
		return true
	}
	plan, err := s.planRegisteredCI(ctx, job)
	if err != nil || !plan.runnable() {
		return false
	}
	return plan.required.CoversSuites(result, plan.suites)
}

// A finished failing check is conclusive even while another hosted job runs.
func (s *Service) registeredHostedStatus(snap Snapshot, r Record, policy ChangePolicy) (status CIVerdict, key string, jobs []string, runs []Run) {
	var hostedStatuses []Status
	for _, v := range snap.Statuses {
		if !localCommitStatus(v) {
			hostedStatuses = append(hostedStatuses, v)
		}
	}
	snap.Statuses = hostedStatuses
	hosted, key, jobs, runs := s.ciStatus(snap, r, policy)
	status = verdictFromHosted(hosted)
	if status != VerdictPending {
		return
	}
	for _, check := range snap.currentChecks() {
		if ciCheck(check, policy.RequiredChecks) && checkState(check.Status, check.Conclusion) == "failure" {
			status = VerdictFailed
			return
		}
	}
	for _, run := range currentRuns(snap.Runs) {
		if run.Status == "completed" && run.Conclusion != "" && run.Conclusion != "cancelled" && !success(run.Conclusion) {
			status = VerdictFailed
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
			status = VerdictFailed
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
			name := string(check.Name())
			if check.Status == localci.OutcomePassed {
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

// localFailedChecks names the checks a local run could not complete, so a failure
// report says which work did not happen.
func localFailedChecks(result localci.Result) []string {
	var names []string
	for _, check := range result.Checks {
		if check.Aggregate || check.Status == localci.OutcomePassed {
			continue
		}
		names = append(names, string(check.Name()))
	}
	return names
}

// localFailureNote words a local run that could not test the commit, with the
// typed cause that says why. A room with no hosted CI shows this instead of a
// wait that never ends.
func localFailureNote(result localci.Result) string {
	cause := result.Cause
	if cause == "" {
		for _, check := range result.Checks {
			if !check.Aggregate && check.Cause != "" {
				cause = check.Cause
				break
			}
		}
	}
	node := result.Node
	if node == "" {
		node = "the CI machine"
	}
	note := "local CI could not run its checks on " + node
	label := cause.Label()
	switch {
	case label != "" && result.Error != "":
		return note + ": " + label + " (" + result.Error + ")"
	case label != "":
		return note + ": " + label
	case result.Error != "":
		return note + ": " + result.Error
	}
	return note
}

func hostedFallbackReason(snap Snapshot) string {
	for _, run := range currentRuns(snap.Runs) {
		if run.Status == "in_progress" {
			return "running hosted CI fallback"
		}
	}
	return "waiting for hosted CI fallback"
}

// savedLocalResult reads the local CI result of one head for the hosted-fallback
// decision. A missing or unreadable result is no result, so a fallback still
// decides instead of a read error breaking the observation.
func savedLocalResult(ctx context.Context, registry localCIRegistry, repo, head string) (localci.Result, bool) {
	result, err := registry.LoadResult(ctx, repo, head)
	if err != nil || result.Repo != repo || result.SHA != head {
		return localci.Result{}, false
	}
	return result, true
}
