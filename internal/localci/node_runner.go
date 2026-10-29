package localci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/ciexcerpt"
	"dyna-agent/internal/jobs"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/systemdver"
)

// The two steps of a finished or cancelled run's teardown, on a machine that may
// answer slowly. An engine stop does not run them: checks are detached, so the
// engine only stops polling and a restarted engine reattaches.
const (
	// containerCleanupTimeout bounds the node command that stops a run's
	// containers, scopes and worktrees.
	containerCleanupTimeout = time.Minute
	// nodeCleanupAttempts is how often the cleanup after a run is tried.
	nodeCleanupAttempts = 2
	// runRemovalTimeout bounds the node command that stops the run's keeper,
	// which then removes the run directory and releases its locks.
	runRemovalTimeout = 2 * time.Minute
	// StopWait is how long an engine stop waits for local CI runs to stop
	// polling their machines. The checks keep running there.
	StopWait = 10 * time.Second
)

type nodeCheck struct {
	id       Suite
	name     CheckName
	group    Group
	inner    string
	job      *JobRef
	image    string
	host     bool
	hostUser bool
	setup    string
	env      map[string]string
	caches   []CacheDefinition
	reports  ReportMode
	priority int
	// cpus and memoryMiB are what the repository asked for, and resources is
	// what the node granted. Heavy marks an IO-heavy check, which a node runs
	// only a few of.
	cpus      int
	memoryMiB int64
	heavy     bool
	resources checkResources
	// event is the GitHub event a job check runs, and branch the ref a push
	// event names. A cache-warming job is scoped to the default branch.
	event  string
	branch string
}

// sccachePortBase is the first port a check's own sccache server may use. A
// shared server would be killed with the check that started it, while other
// checks still use it, so each check gets a server of its own.
const sccachePortBase = 4226

// ErrHostChecksDisabled means a repository asked for host execution on a
// machine whose owner has not allowed it. The repository cannot enable it.
var ErrHostChecksDisabled = errors.New("host CI checks are disabled")

// nodeChecks expands repository-defined suites into checks. A check shared by
// suites runs once and keeps the aggregate membership from its grouped suite.
func nodeChecks(definition *Definition, suites []Suite, allowHost bool) ([]nodeCheck, bool, error) {
	if len(suites) == 0 {
		suites = definition.WorkflowSuites()
	}
	var checks []nodeCheck
	seen := map[Suite]int{}
	aggregate := false
	for _, suite := range suites {
		expanded, ok := definition.ExecutableChecks(suite)
		if !ok {
			return nil, false, fmt.Errorf("unknown CI suite %q", suite)
		}
		for _, command := range expanded {
			check := command.Check
			aggregate = aggregate || check.Group != ""
			if index, exists := seen[check.ID]; exists {
				if checks[index].group == "" {
					checks[index].group = check.Group
				}
				continue
			}
			// A workflow job runs containers the workflow chooses, including their
			// options and volumes, so it needs the machine's host-check grant just
			// as a host command does. act also keeps the host's container socket
			// out of those containers, but that alone cannot bound what a workflow
			// asks act to start.
			if (command.Host || command.Job != nil) && !allowHost {
				return nil, false, fmt.Errorf("%w: check %q", ErrHostChecksDisabled, check.ID)
			}
			seen[check.ID] = len(checks)
			checks = append(checks, nodeCheck{
				id: check.ID, name: check.Name, group: check.Group,
				inner: command.Command, job: command.Job, image: command.Image,
				host: command.Host, hostUser: command.HostUser, setup: command.Setup,
				env: cloneStrings(command.Env), caches: append([]CacheDefinition(nil), command.Caches...),
				reports: command.Reports, priority: command.Priority,
				cpus: command.CPUs, memoryMiB: command.MemoryMiB, heavy: command.Heavy,
			})
		}
	}
	return checks, aggregate, nil
}

func nodeCacheNames(checks []nodeCheck) []string {
	var names []string
	for _, check := range checks {
		for _, cache := range check.caches {
			if !slices.Contains(names, cache.Name) {
				names = append(names, cache.Name)
			}
		}
	}
	return names
}

// systemdNeedsNoExpandFlag reports whether the node's systemd-run must be told
// not to expand environment variables in a command's arguments. The script Dyna
// passes holds $ signs of its own, and a systemd that expands them rewrites it,
// so a version that knows systemdver.NoExpandFlag gets it. An older systemd-run
// rejects that argument and expands nothing of its own, so it runs the scope
// without it: host checks failed on Debian 12 that way, with "unrecognized
// option '--expand-environment=no'". A version this cannot read gets the
// argument, because a loud failure on an old systemd is better than a script
// that was quietly rewritten.
func (r Runner) systemdNeedsNoExpandFlag(ctx context.Context) bool {
	name, args := r.exec().NodeProgram("systemd-run", []string{"--version"})
	fn := r.Command
	if fn == nil {
		fn = jobs.RunCommand
	}
	var out bytes.Buffer
	if err := fn(ctx, "", nil, &out, io.Discard, name, args...); err != nil {
		return true
	}
	major, ok := systemdver.Major(out.String())
	return !ok || systemdver.NeedsNoExpand(major)
}

func (r Runner) runNode(ctx context.Context, req Request) (result Result, err error) {
	node := *r.Node
	if err := node.Validate(); err != nil {
		return result, err
	}
	if !node.Enabled {
		return result, fmt.Errorf("CI node is disabled")
	}
	if req.Timeout <= 0 {
		req.Timeout = 30 * time.Minute
	}
	if req.MaxTest <= 0 {
		req.MaxTest = 10 * time.Second
	}
	var checks []nodeCheck
	var aggregate bool
	prepareChecks := func() error {
		var prepareErr error
		checks, aggregate, prepareErr = nodeChecks(req.Definition, req.Suites, node.HostChecksAllowed(req.RoomID))
		if req.Event == "push" {
			// A warm run sends a push event, so the workflow's cache-warming job
			// sees the default branch it is scoped to.
			for i := range checks {
				checks[i].event, checks[i].branch = "push", req.Branch
			}
		}
		if errors.Is(prepareErr, ErrHostChecksDisabled) {
			return fmt.Errorf("CI node %q: %w", node.Name, prepareErr)
		}
		if prepareErr != nil {
			return prepareErr
		}
		// Every check is bounded by this node before any of them starts, so the
		// report and the command agree on what the run granted.
		for i := range checks {
			checks[i].resources = clampCheckResources(checks[i], node)
		}
		return nil
	}
	if req.Definition != nil {
		if err := prepareChecks(); err != nil {
			return result, err
		}
	}
	repo, err := filepath.Abs(req.Repo)
	if err != nil {
		return result, err
	}
	common, err := r.commonDir(ctx, repo)
	if err != nil {
		return result, err
	}
	ref := req.SHA
	if ref == "" {
		ref = "HEAD"
	}
	var output bytes.Buffer
	if err := r.run(ctx, repo, &output, "git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}"); err != nil {
		return result, fmt.Errorf("resolve commit: %w: %s", err, &output)
	}
	sha := strings.TrimSpace(output.String())
	if !commitSHA.MatchString(sha) {
		return result, fmt.Errorf("git returned invalid commit SHA")
	}
	if req.Definition == nil {
		req.Definition, err = LoadDefinition(ctx, repo, sha)
		if err != nil {
			return result, err
		}
	}
	if req.Definition.revision != "" && req.Definition.revision != sha {
		return result, fmt.Errorf("local CI definition revision does not match commit %s", sha)
	}
	if checks == nil {
		if err := prepareChecks(); err != nil {
			return result, err
		}
	}
	result = Result{Repo: common, RoomID: req.RoomID, Node: node.Name, SHA: sha, Started: time.Now().UTC(), Status: "error"}
	defer func() {
		result.Duration = time.Since(result.Started)
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
		if err != nil {
			result.Status, result.Error = OutcomeError, err.Error()
			if result.Cause == "" {
				result.Cause = runCause(node, ctx, err)
			}
		}
	}()
	base := cacheBase(node.Name)
	cache := path.Join(base, repoKey(common)+".git")
	// A restarted engine follows the checks an earlier engine left running on this
	// machine; it starts a new run only when there are none.
	localStore := Store{DataDir: r.root()}
	attachKey, err := canonicalKey(common)
	if err != nil {
		return result, err
	}
	// The report folder comes first: a run that fails here has claimed no record.
	dir := r.resultDir(common, sha)
	if err := os.MkdirAll(dir, 0700); err != nil { // not-state: report blobs live outside SQLite.
		return result, err
	}
	reportRoot, err := os.MkdirTemp(dir, "run-") // not-state: report blobs live outside SQLite.
	if err != nil {
		return result, err
	}
	// The run takes over only a record whose run has no live process, so it never
	// touches the record of a run that another engine or command is executing.
	resume, err := r.resumeRun(ctx, localStore, attachKey, sha, cache, checks)
	if err != nil {
		_ = os.Remove(reportRoot) // not-state: the folder is still empty.
		return result, err
	}
	runID := fmt.Sprintf("%s-%d", sha[:12], time.Now().UnixNano())
	if resume != nil {
		runID = resume.RunID
	}
	runDir := path.Join(base, "runs", runID)
	// A run holds a lock on this file while it runs. A cleanup takes the lock to
	// learn that the run died, so a crashed run's directory never stays forever.
	active := path.Join(runDir, ".active")
	reports := path.Join(runDir, "reports")
	control, cancel := context.WithTimeout(ctx, time.Duration(len(checks))*(req.Timeout+admissionWait)+2*time.Minute)
	defer cancel()
	// A run removes its own directory on the node, whatever ended it: a finished
	// run left about half a gigabyte behind, and the machine filled its disk.
	// It is a separate call because the cleanup below stops at its first failed
	// container check (exit 69) and would then keep the directory. It runs on its
	// own context, so a cancelled run still removes it, and it runs last, after
	// the reports were copied back.
	defer func() {
		if EngineStopping() {
			// An engine stop leaves the run on its machine: its checks keep running
			// and a restarted engine reattaches.
			return
		}
		remove, stop := context.WithTimeout(context.WithoutCancel(ctx), runRemovalTimeout)
		defer stop()
		var log tailBuffer
		if e := r.nodeCommand(remove, &log, &log, removeRunDir(base, active)); e != nil {
			err = errors.Join(err, fmt.Errorf("remove run directory: %w: %s", e, log.String()))
		}
	}()
	output.Reset()
	// The record of started checks lasts until the run ends for good. An engine
	// stop keeps it, so the next engine reattaches.
	attached := attachment{RunID: runID}
	if resume != nil {
		attached = *resume
	}
	defer func() {
		background := context.WithoutCancel(ctx)
		if EngineStopping() {
			// The checks stay on the machine, and the record passes to whoever runs
			// the commit next.
			if e := localStore.releaseAttachment(background, attachKey, sha, node.Name, runID); e != nil {
				log.Printf("local CI %s: hand over started checks: %v", node.Name, e)
			}
			return
		}
		if e := localStore.deleteAttachment(background, attachKey, sha, node.Name, runID); e != nil {
			log.Printf("local CI %s: forget started checks: %v", node.Name, e)
		}
	}()
	if resume == nil {
		// A machine that is close to a full disk cleans Dyna's own caches first, so
		// the checks do not die on a full disk halfway through.
		if e := r.cleanBeforeRun(control, node); e != nil {
			log.Printf("local CI %s: free space check: %v", node.Name, e)
		}
		setup := nodeSetupScript(base, reports, active, nodeCacheNames(checks))
		setupErr := r.nodeCommand(control, &output, &output, setup)
		// A repair is a fact whether or not the run then succeeded, so report it
		// before the setup error returns.
		if kinds := parseCacheRepairs(output.String()); len(kinds) > 0 && r.OnCacheRepair != nil {
			r.OnCacheRepair(control, CacheRepair{Node: node.Name, Kinds: kinds})
		}
		if setupErr != nil {
			return result, fmt.Errorf("initialize node: %w: %s", setupErr, &output)
		}
		output.Reset()
		if err := r.exec().PushCommit(control, repo, cache, sha); err != nil {
			// No check has started, so the git error would otherwise be an empty
			// result and the trial would report an unknown job.
			cause := runCause(node, control, err)
			if cause == CauseUnclassified {
				cause = CauseGit
			}
			result.Cause = cause
			job := gitTransportJob(node.Name, gitPushCheck, cause, err)
			// The copy from suites to checks is registered later, so a return here
			// has to set both. The trial reads checks.
			result.Suites, result.Checks = []SuiteResult{job}, []SuiteResult{job}
			return result, err
		}
	}
	// Job checks each run act on a copy of the workflow whose name carries the
	// run and the check, so parallel checks never share one container name.
	// The engine tags the copy from the commit's own bytes before any check
	// starts; a workflow it cannot parse fails the run as a clear error
	// instead of a check that dies inside act.
	taggedWorkflows := map[string]string{}
	for _, check := range checks {
		if check.job == nil {
			continue
		}
		body, err := repoid.GitRaw(control, repo, "cat-file", "blob", sha+":"+check.job.Workflow)
		if err != nil {
			return result, fmt.Errorf("read workflow %s: %w", check.job.Workflow, err)
		}
		tagged, err := tagWorkflowBody([]byte(body), "CI-dyna-"+runID+"-"+string(check.id))
		if err != nil {
			return result, fmt.Errorf("tag workflow %s for %s: %w", check.job.Workflow, check.id, err)
		}
		taggedWorkflows[string(check.id)] = string(tagged)
	}
	defer func() {
		if EngineStopping() {
			return
		}
		script := teardownScript(node, cache, runDir, runID, checks)
		// The checks decided the commit. A cleanup that fails or times out is
		// tried once more, because a timeout is often a busy node, and the run's
		// directory and record are removed right after this, so a survivor would
		// have no second chance. A cleanup that still fails is a warning on the
		// run, never an error that replaces a passed result.
		var failure error
		var lastOutput string
		for attempt := 0; attempt < nodeCleanupAttempts; attempt++ {
			cleanup, stop := context.WithTimeout(context.Background(), containerCleanupTimeout)
			var tail tailBuffer
			failure = r.nodeCommand(cleanup, &tail, &tail, script)
			stop()
			if failure == nil {
				// Teardown confirmed that every check stopped. Any reservation
				// retained after an uncertain poll can now be released.
				for _, check := range checks {
					if err := r.queue().Release(context.Background(), node.Name, runID+"-"+string(check.id)); err != nil {
						log.Printf("local CI %s: release check reservation: %v", node.Name, err)
					}
				}
				return
			}
			lastOutput = tail.String()
		}
		log.Printf("local CI %s: cleanup node: %v: %s", node.Name, failure, lastOutput)
		result.Warnings = append(result.Warnings, NodeCleanupUnfinished)
	}()
	if req.OnStart != nil {
		if err := req.OnStart(control); err != nil {
			return result, err
		}
	}
	result.Suites = make([]SuiteResult, len(checks))
	// Each check log stays open until the report classification has run, so a
	// cause the reports prove can still be appended to the check's own file.
	logs := make([]*checkLog, len(checks))
	defer func() {
		result.Checks = append([]SuiteResult(nil), result.Suites...)
		if !aggregate {
			return
		}
		var groups []Group
		for _, check := range checks {
			if check.group != "" && !slices.Contains(groups, check.group) {
				groups = append(groups, check.group)
			}
		}
		for _, group := range groups {
			status := OutcomePassed
			for _, check := range result.Suites {
				if check.Group == group {
					status = combinedStatus(status, check.Status)
				}
			}
			result.Checks = append(result.Checks, SuiteResult{Suite: Suite(group), CheckName: group.Name(), Node: node.Name, Status: status, Aggregate: true})
		}
	}()
	// One poller follows every check of this run with one call at a time.
	poller, stopPoller := r.exec().NewPoller(control)
	defer stopPoller()
	// Renew a live check at most once per minute. The expiry is much longer
	// than this interval, including when a poll takes time to return.
	renewed := map[string]time.Time{}
	poller.Polled = func(spec jobs.RunSpec) {
		if time.Since(renewed[spec.ID]) < time.Minute {
			return
		}
		if err := r.queue().Renew(control, node.Name, runID+"-"+spec.ID); err != nil {
			log.Printf("local CI %s: renew check reservation: %v", node.Name, err)
			return
		}
		renewed[spec.ID] = time.Now()
	}
	var attachedMu sync.Mutex
	// Record each check before admission. If the engine stops between
	// admission and launch, recovery can inspect the node and release its
	// reservation when no detached check was started.
	recordCheck := func(id Suite) error {
		attachedMu.Lock()
		defer attachedMu.Unlock()
		if !slices.Contains(attached.Checks, id) {
			attached.Checks = append(attached.Checks, id)
		}
		return localStore.saveAttachment(control, attachKey, sha, node.Name, attached)
	}
	resumed := func(id Suite) bool { return resume != nil && resume.has(id) }
	noExpand := r.systemdNeedsNoExpandFlag(control)
	queue := make(chan int, len(checks))
	order := make([]int, len(checks))
	for i := range checks {
		order[i] = i
	}
	// Checks that are already running come first. Higher-priority checks start
	// first; equal priorities keep definition order.
	sort.SliceStable(order, func(i, j int) bool {
		if a, b := resumed(checks[order[i]].id), resumed(checks[order[j]].id); a != b {
			return a
		}
		return checks[order[i]].priority > checks[order[j]].priority
	})
	for _, index := range order {
		queue <- index
	}
	close(queue)
	var wg sync.WaitGroup
	// The node's admission queue bounds what runs at once, so every check of
	// the run gets its own worker and holds its own place in the queue. A
	// repository's parallelism limit still bounds its own run.
	workers := len(checks)
	if req.Parallelism > 0 {
		workers = min(workers, req.Parallelism)
	}
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				check := checks[i]
				sr := SuiteResult{Suite: check.id, CheckName: check.name, Group: check.group, Node: node.Name, Status: "passed", Note: check.resources.Note}
				checkCtx, stop := context.WithTimeout(control, admissionWait+req.Timeout+time.Minute)
				var output tailBuffer
				writer := io.Writer(&output)
				// The check's own output goes to a local blob file first, so the
				// evidence outlives a report archive, a cleanup or a transport that
				// fails afterwards.
				blob, blobErr := newCheckLog(filepath.Join(reportRoot, "checks"), check)
				if blobErr != nil {
					log.Printf("local CI %s: keep check log: %v", node.Name, blobErr)
				} else {
					writer = io.MultiWriter(&output, blob)
				}
				spec := jobs.RunSpec{
					ID:     string(check.id),
					Dir:    detachedDir(runDir, check.id),
					Unit:   checkUnit(runID, check.id),
					Attach: resumed(check.id),
				}
				// Every check joins the node's admission queue: the one queue jobs
				// and checks of every room share. A fresh check waits for room, and
				// each wait reason the queue records lands in its log, so the wait
				// is visible. A resumed check already runs on the node, so it only
				// takes its place back.
				queue := r.queue()
				source := req.Source
				source.Head = sha[:7]
				demand := jobs.Demand{Node: node.Name, ID: runID + "-" + string(check.id), Room: req.RoomID, Heavy: check.heavy, CPUs: check.resources.CPUs, MemoryMiB: check.resources.MemoryMiB, Timeout: req.Timeout, Ahead: req.Ahead, Check: string(check.name), Source: source}
				runErr := recordCheck(check.id)
				port := 0
				if runErr != nil {
					runErr = fmt.Errorf("record check before admission: %w", runErr)
				} else if spec.Attach {
					held, holdErr := queue.Reattach(checkCtx, demand)
					// Only an exit record proves that a missing reservation is safe to
					// ignore. A live check keeps the error, so existing cleanup stops it.
					if errors.Is(holdErr, jobs.ErrNoReservation) {
						frames, pollErr := r.exec().Poll(checkCtx, []jobs.RunSpec{spec}, nil)
						switch {
						case pollErr != nil:
							runErr = fmt.Errorf("poll the detached check %s: %w", check.id, pollErr)
						case frames[spec.Dir].Unit != jobs.UnitExited:
							runErr = fmt.Errorf("record the running check %s in the queue: %w", check.id, holdErr)
						}
					} else if holdErr != nil {
						runErr = fmt.Errorf("record the running check %s in the queue: %w", check.id, holdErr)
					} else {
						port = held
					}
				} else {
					port, runErr = queue.Acquire(checkCtx, demand, r.wait, func(reason string) {
						fmt.Fprintf(writer, "local CI: %s\n", reason)
					})
				}
				if runErr == nil && !spec.Attach {
					spec.Script = nodeCheckScript(node, base, cache, runDir, runID, sha, req.Base, check, req.Timeout, port, noExpand, req.Definition.ActImage(), node.ContainerSocket, taggedWorkflows[string(check.id)])
				}
				if !check.host {
					// A host check runs its command in its own scope from inside the
					// script, so the unit that holds the script stays unbounded.
					limits := check.resources.Scope(false)
					spec.Limits = &limits
				}
				if runErr == nil {
					runErr = poller.Run(checkCtx, spec, writer)
				}
				// An exit record proves the check ended. A transport failure or
				// failed stop does not; retain its reservation until teardown
				// confirms the stop or the reservation expires.
				var exited *jobs.DetachedExit
				if (!r.Engine || !EngineStopping()) && (runErr == nil || errors.As(runErr, &exited)) {
					releaseCtx, releaseStop := context.WithTimeout(context.WithoutCancel(checkCtx), 10*time.Second)
					_ = queue.Release(releaseCtx, node.Name, demand.ID)
					releaseStop()
				}
				sr.LogTail = output.String()
				markers := checkMarker.FindAllStringSubmatch(sr.LogTail, -1)
				if runErr != nil || len(markers) == 0 {
					sr.Status, sr.Error = OutcomeError, "check command did not complete"
					if runErr != nil {
						sr.Error = runErr.Error()
					}
					sr.Cause = checkCause(node, checkCtx, runErr, markers, check.image != "")
					if sr.Cause == CauseTestFailure {
						sr.Status = OutcomeFailed
					}
				}
				// A container that never started proves nothing about the commit: report
				// the machine, not a failing test, so the run can move to another one.
				if sr.Status == OutcomeFailed && check.image != "" && !commandStarted(sr.LogTail) {
					sr.Status, sr.Error, sr.Cause = OutcomeError, "the container did not start the check's command on "+node.Name, CauseContainerFailure
				}
				// A full disk on the machine is not a failing test: report it as an
				// infrastructure error, so the run cleans up and retries, and hosted
				// CI resumes when it stays broken.
				if sr.Status == OutcomeFailed && diskFull(sr.LogTail+"\n"+sr.Error) {
					sr.Status, sr.Error, sr.Cause = OutcomeError, "check ran out of disk space on the CI node", CauseDiskFull
				}
				// A checkout that failed before the check ran keeps the git error as
				// this job's evidence. The exit status alone would hide that text.
				if text, ok := gitErrorText(sr.LogTail); ok && len(markers) == 0 {
					sr.Status, sr.Error = OutcomeError, text
					if sr.Cause == "" || sr.Cause == CauseContainerFailure || sr.Cause == CauseUnclassified {
						sr.Cause = CauseGit
					}
				}
				if blobErr == nil {
					logs[i] = blob
					blob.exitCode = checkExitCode(runErr, markers)
					// The output reaches the log before the report copy, and the cause the
					// command proves is recorded now. The reports can still change it.
					if recordErr := blob.record(checkExit{Cause: sr.Cause, Status: sr.Status, ExitCode: blob.exitCode}); recordErr != nil {
						log.Printf("local CI %s: record check cause: %v", node.Name, recordErr)
					}
					sr.Reports = append(sr.Reports, blob.path)
				}
				stop()
				result.Suites[i] = sr
			}
		}()
	}
	wg.Wait()
	// A cancelled or expired run still reports what each check proved. A failed
	// report copy stays an error on the result, but it never hides the checks'
	// own causes or their local logs.
	var copyErr error
	stopped := control.Err()
	if stopped == nil {
		copyErr = r.fetchReports(control, reportRoot, reports)
		// The deadline can end the copy itself, and then the reports are gone.
		stopped = control.Err()
	}
	result.Status = OutcomePassed
	for i := range result.Suites {
		sr := &result.Suites[i]
		switch {
		case stopped != nil:
			// The reports were never read, so a check that reported success is
			// not a pass: only its log is proven. A check that already failed
			// keeps its own outcome.
			if sr.Status == OutcomePassed {
				sr.Status, sr.Error, sr.Cause = OutcomeError, "check stopped before its reports were read", runCause(node, control, stopped)
			}
		case copyErr != nil:
			// The command exited 0, but the gate needs its reports, so this is
			// not a pass. It is not a machine failure either: the command's own
			// result reached the engine, and only the reports are missing.
			if sr.Status == OutcomePassed {
				sr.Status, sr.Error, sr.Cause = OutcomeError, "reports unavailable: copy reports: "+copyErr.Error(), CauseReports
			}
		default:
			// The reports say what the exit status cannot: the execution timing,
			// the structured failures, the slow tests and the diagnostics links.
			// Read them for every check, including one whose command failed; the
			// command's own outcome and cause stay the check's.
			readReports(sr, checks[i].reports, reportRoot, req.MaxTest, req.Allowed)
		}
		result.Status = combinedStatus(result.Status, sr.Status)
	}
	// The report classification can assign a cause to a check that passed its
	// command, or replace the one the command proved. Each log ends with the
	// cause the run finally reported.
	for i, blob := range logs {
		if blob == nil {
			continue
		}
		sr := result.Suites[i]
		if err := blob.record(checkExit{Cause: sr.Cause, Status: sr.Status, ExitCode: blob.exitCode}); err != nil {
			log.Printf("local CI %s: record check cause: %v", node.Name, err)
		}
		if sr.Status != OutcomePassed {
			result.Suites[i].FailureExcerpt = checkFailureExcerpt(blob.path)
		}
		if err := blob.Close(); err != nil {
			log.Printf("local CI %s: finish check log: %v", node.Name, err)
		}
	}
	if stopped != nil {
		// A run that reached its own deadline, or that its caller cancelled, is
		// not a pass whatever a check proved before the end: the reports were
		// never read. The checks keep their logs and the run keeps a typed cause.
		result.Cause = runCause(node, control, stopped)
		return result, stopped
	}
	if copyErr != nil {
		// A machine that lost the connection during its checks keeps that cause
		// on the run, so the scheduler still retries it on another machine. A run
		// whose checks proved their own results only lost its reports.
		result.Cause = CauseReports
		if reportCopyCause(node, control, copyErr) == CauseSSHDrop && slices.ContainsFunc(result.Suites, func(sr SuiteResult) bool { return sr.Cause == CauseSSHDrop }) {
			result.Cause = CauseSSHDrop
		}
		return result, fmt.Errorf("copy reports: %w", copyErr)
	}
	return result, nil
}

// reportCopyCause names a failed copy as an SSH drop when its client lost the
// connection, and as unavailable reports otherwise (D-170).
func reportCopyCause(node Node, ctx context.Context, err error) FailureCause {
	if runCause(node, ctx, err) == CauseSSHDrop {
		return CauseSSHDrop
	}
	return CauseReports
}

// fetchReports copies the node's report files into the local report blob folder.
// The caller treats a failure as evidence about a check and not as a reason to
// drop the run: the checks keep their logs either way. It only reads the node,
// so it follows the same retry rule as every other node call, and each attempt
// starts a new archive.
func (r Runner) fetchReports(ctx context.Context, reportRoot, reports string) error {
	script := "cd " + shellquote.Quote(reports) + " && find . -mindepth 2 -maxdepth 2 -type f \\( -name '*.xml' -o -name '*.json' -o -name 'ci-diagnostics.jsonl' \\) -print0 | tar -C " + shellquote.Quote(reports) + " --null -T - -cf -"
	return r.retry(ctx, longCallTimeout, func(ctx context.Context) error {
		archive, err := os.CreateTemp(reportRoot, "reports-*.tar") // not-state: temporary report transport.
		if err != nil {
			return &jobs.PermanentError{Err: err}
		}
		defer os.Remove(archive.Name())
		defer archive.Close()
		var stderr tailBuffer
		if err := r.exec().NodeExec(ctx, nil, &boundedWriter{writer: archive, remaining: 70 << 20}, &stderr, script); err != nil {
			err = fmt.Errorf("%w: %s", err, stderr.String())
			if errors.Is(err, errReportsTooLarge) {
				return &jobs.PermanentError{Err: err}
			}
			return err
		}
		if _, err := archive.Seek(0, io.SeekStart); err != nil {
			return &jobs.PermanentError{Err: err}
		}
		if err := readArchive(archive, reportRoot); err != nil {
			return &jobs.PermanentError{Err: err}
		}
		return nil
	})
}

// readReports fills one check's duration, reports and test failures from the
// copied report files, whatever the command already proved. A check that cannot
// show a result of its own is an infrastructure error and never a passing one.
func readReports(sr *SuiteResult, mode ReportMode, reportRoot string, max time.Duration, allowed map[string]bool) {
	var timing struct {
		DurationNanos time.Duration `json:"durationNanos"`
	}
	timingPath := filepath.Join(reportRoot, "reports", string(sr.Suite), "execution.json")
	data, timingErr := os.ReadFile(timingPath)
	if timingErr == nil {
		timingErr = json.Unmarshal(data, &timing)
	}
	if timingErr == nil && timing.DurationNanos >= 0 {
		sr.Duration = timing.DurationNanos
	} else if sr.Status == OutcomePassed {
		sr.Status, sr.Error, sr.Cause = OutcomeError, "check produced no valid execution timing", CauseReports
	}
	if e := checkReports(sr, mode, reportRoot, max, allowed); e != nil {
		// A check that already proved how it failed keeps its status, its cause
		// and its error: a report problem is not better evidence than the
		// command, and it is added to what the command said instead.
		switch {
		case sr.Status == OutcomePassed:
			sr.Status, sr.Error, sr.Cause = OutcomeError, e.Error(), CauseReports
		case sr.Error == "":
			sr.Error = e.Error()
		default:
			sr.Error += "; reports: " + e.Error()
		}
	}
	if sr.Status == OutcomeFailed && sr.Cause == "" {
		sr.Cause = CauseTestFailure
	}
	diagnostics := filepath.Join(reportRoot, "reports", string(sr.Suite), "ci-diagnostics.jsonl")
	if info, e := os.Stat(diagnostics); e == nil && info.Mode().IsRegular() {
		sr.Reports = append(sr.Reports, diagnostics)
	}
}

// cancelled reports a caller that stopped the run on purpose. A deadline is the
// run's own end, not a caller's decision, so it does not count.
func cancelled(ctx context.Context) bool {
	return ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// gitPushCheck is the job a failed push records. No suite check has started,
// so this name is the evidence the trial shows instead of an unknown job.
const gitPushCheck CheckName = "git-push"

// gitTransportJob is one push or checkout failure, with the git error text.
func gitTransportJob(node string, name CheckName, cause FailureCause, err error) SuiteResult {
	text := ""
	if err != nil {
		text = err.Error()
	}
	return SuiteResult{CheckName: name, Node: node, Status: OutcomeError, Cause: cause, Error: text, LogTail: text}
}

// gitNodeStep runs one git command on the node. Smudge is skipped, so a
// checkout leaves Git LFS pointer files. A failure prints the git error as a
// marker the runner stores on the job.
func gitNodeStep(command string) string {
	return `git_err=$(GIT_LFS_SKIP_SMUDGE=1 ` + command + ` 2>&1) || { printf 'DYNA_CI_GIT_ERROR=%s\n' "$(printf '%s' "$git_err" | tr '\n' ' ' | head -c 2000)"; exit 69; }; `
}

var gitErrorMarker = regexp.MustCompile(`(?m)^DYNA_CI_GIT_ERROR=(.*)$`)

// gitErrorText is the git error a node step printed, when the log has one.
func gitErrorText(log string) (string, bool) {
	matches := gitErrorMarker.FindAllStringSubmatch(log, -1)
	if len(matches) == 0 {
		return "", false
	}
	text := strings.TrimSpace(matches[len(matches)-1][1])
	if text == "" {
		return "", false
	}
	return text, true
}

// checkMarker is the exit status the node writes after a check's command ran.
// Without it the check never reached its command or never came back.
var checkMarker = regexp.MustCompile(`(?m)^DYNA_CI_CHECK_EXIT=([0-9]+)$`)

// commandStartedFile is the sentinel a check's command writes before it runs.
// Its name matches no report filter, so it never leaves the node.
const commandStartedFile = ".command-started"

// commandMarker is what the node writes about that sentinel: 1 when the
// container started the command, 0 when it never got that far.
var commandMarker = regexp.MustCompile(`(?m)^DYNA_CI_COMMAND_STARTED=([01])$`)

// commandStarted reports what the node's marker proves. A log without the marker
// proves nothing either way, so it counts as started.
func commandStarted(logTail string) bool {
	marks := commandMarker.FindAllStringSubmatch(logTail, -1)
	return len(marks) == 0 || marks[len(marks)-1][1] == "1"
}

// checkCause reads why a check stopped from what its own output proves: the
// deadline, the exit status the node recorded, and the marker it printed. It
// never calls an unmarked local error an SSH drop: only an SSH client that lost
// the connection returns 255 without the node's marker. Exit status 137 proves a
// SIGKILL, which is not a timeout: a container that was killed ran out of
// resources, and any other process was killed from outside.
func checkCause(node Node, ctx context.Context, err error, markers [][]string, container bool) FailureCause {
	cause := endCause(node, ctx, err, markers, container)
	// An engine stop ends every check it owns, whatever wrapper reported first:
	// the run context, a TERM to the check's timeout, or a container removed by
	// the cleanup. None of those proves anything about the commit, so they all
	// carry the engine-stop cause and the run is repeated.
	switch cause {
	case CauseCancelled, CauseContainerFailure, CauseUnclassified:
		if EngineStopping() {
			return CauseEngineStop
		}
	}
	return cause
}

// endCause names why one check ended, from its own evidence.
func endCause(node Node, ctx context.Context, err error, markers [][]string, container bool) FailureCause {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return CauseTimeout
		}
		return CauseCancelled
	}
	// A node that stayed unreachable lost the check, whatever the check's own
	// marker says: a result that never crossed the wire cannot be trusted, so this
	// is a machine cause and the run is retried.
	if unreachable(node, err) {
		return CauseSSHDrop
	}
	// A cancellation the error itself carries proves a cancelled run even when
	// this context has not seen the cancel yet: one cancel reaches the poller's
	// context and the check's in one wave, and a poller that reports the run's
	// end can carry it first, so the error's identity decides as well.
	if errors.Is(err, context.Canceled) {
		return CauseCancelled
	}
	code, hasExit := jobs.CommandExitStatus(err)
	if len(markers) == 0 {
		if hasExit {
			switch {
			case code == 69:
				return CauseContainerFailure
			case code == 124:
				return CauseTimeout
			case code == 137:
				return killedCause(container)
			}
		}
		return CauseUnclassified
	}
	code, convErr := strconv.Atoi(markers[len(markers)-1][1])
	if convErr != nil {
		return CauseUnclassified
	}
	switch code {
	case 69, 125, 126, 127:
		return CauseContainerFailure
	case 75, 124:
		return CauseTimeout
	case 137:
		return killedCause(container)
	case 143:
		return CauseCancelled
	case 0, 255:
		return CauseUnclassified
	}
	return CauseTestFailure
}

// killedCause names a check that something killed. In a container the usual
// cause is a resource limit, so the report says so; outside one, the output
// proves no cause of its own.
func killedCause(container bool) FailureCause {
	if container {
		return CauseContainerFailure
	}
	return CauseUnclassified
}

// runCause names why a run stopped before its checks decided.
func runCause(node Node, ctx context.Context, err error) FailureCause {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return CauseTimeout
		}
		// The engine's own stop is not the repository's fault: the same commit
		// runs again, so the cause must be told apart from a cancellation that
		// a superseded head or the owner asked for.
		if EngineStopping() {
			return CauseEngineStop
		}
		return CauseCancelled
	}
	if unreachable(node, err) {
		return CauseSSHDrop
	}
	return CauseUnclassified
}

// unreachable reports a failure that proves the node did not answer: a node
// that stayed unreachable beyond the bound, or a call that ssh ended with 255
// after its retries.
func unreachable(node Node, err error) bool {
	var lost *jobs.NodeLostError
	if errors.As(err, &lost) {
		return !lost.Gone
	}
	var exit *exec.ExitError
	return node.SSH != "" && errors.As(err, &exit) && exit.ExitCode() == 255
}

// checkExit is the typed end of a check log: it says why the check stopped, so
// a person who opens the run folder reads the cause next to the output. A saved
// result in SQLite stays the authority.
type checkExit struct {
	Cause    FailureCause `json:"cause"`
	Status   Outcome      `json:"status"`
	ExitCode int          `json:"exitCode,omitempty"`
}

// checkExitCode is the exit status the evidence proves, and 0 when it proves
// none.
func checkExitCode(err error, markers [][]string) int {
	if len(markers) > 0 {
		if code, convErr := strconv.Atoi(markers[len(markers)-1][1]); convErr == nil {
			return code
		}
		return 0
	}
	code, _ := jobs.CommandExitStatus(err)
	return code
}

// checkFailureExcerpt reads a check's whole saved log and keeps its failing part.
// The stored log tail is short, so a failing test block earlier in the log is kept
// only here. Readers sanitize the excerpt, as they do the stored tail.
func checkFailureExcerpt(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Printf("local CI keep failure excerpt: %v", err)
		return ""
	}
	return ciexcerpt.Failure(string(raw), true)
}

// checkLogHead and checkLogTail bound what one check keeps in its local log: the
// start and the end of the output, so a very long log cannot fill the disk.
const (
	checkLogHead = 1 << 20
	checkLogTail = 64 << 10
)

// checkLog writes one check's own output to a local blob file. The evidence of
// a failed check stays there whatever happens to the report archive, the
// cleanup or the transport afterwards.
type checkLog struct {
	file     *os.File
	path     string
	written  int64
	dropped  int64
	tail     []byte
	flushed  bool
	recorded FailureCause
	exitCode int
	broken   bool
	failure  error
}

func newCheckLog(dir string, check nodeCheck) (*checkLog, error) {
	if err := os.MkdirAll(dir, 0700); err != nil { // not-state: report blobs live outside SQLite.
		return nil, err
	}
	path := filepath.Join(dir, string(check.id)+".log")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	return &checkLog{file: file, path: path}, nil
}

// Write keeps the start of the output in the file and the end of it in memory.
// A log file that fails never breaks the check: the caller still reads the tail.
func (l *checkLog) Write(p []byte) (int, error) {
	n := len(p)
	if !l.broken && l.written < checkLogHead {
		head := p
		if room := int64(checkLogHead) - l.written; int64(len(head)) > room {
			head = head[:room]
		}
		if _, err := l.file.Write(head); err != nil {
			l.broken, l.failure = true, err
			l.keep(p)
			return n, nil
		}
		l.written += int64(len(head))
		p = p[len(head):]
	}
	l.keep(p)
	return n, nil
}

// keep remembers the end of the output, which is where a failure states its
// cause.
func (l *checkLog) keep(p []byte) {
	l.dropped += int64(len(p))
	l.tail = append(l.tail, p...)
	if len(l.tail) > checkLogTail {
		l.tail = l.tail[len(l.tail)-checkLogTail:]
	}
}

// flush writes the end of the output it kept in memory, once. It runs before the
// report copy, so the output of a check is on disk whatever happens to the copy.
func (l *checkLog) flush() error {
	if l.broken {
		return l.failure
	}
	if l.flushed {
		return nil
	}
	l.flushed = true
	if len(l.tail) == 0 {
		return nil
	}
	if omitted := l.dropped - int64(len(l.tail)); omitted > 0 {
		if _, err := fmt.Fprintf(l.file, "\n[... %d bytes of the middle omitted ...]\n", omitted); err != nil {
			l.broken, l.failure = true, err
			return err
		}
	}
	if _, err := l.file.Write(l.tail); err != nil {
		l.broken, l.failure = true, err
		return err
	}
	return nil
}

// record appends the typed cause of the check, so the run folder alone says why
// it stopped. It is called once after the command and again after the report
// classification when that changes the cause; a cause already written is not
// written twice. A saved result in SQLite stays the authority.
func (l *checkLog) record(exit checkExit) error {
	// The output is flushed first, so a check that has no cause yet still keeps
	// its evidence before the report copy starts.
	if err := l.flush(); err != nil {
		return err
	}
	if exit.Cause == "" || exit.Cause == l.recorded {
		return nil
	}
	line, err := json.Marshal(exit)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(l.file, "\nDYNA_CI_CHECK_CAUSE %s\n", line); err != nil {
		l.broken, l.failure = true, err
		return err
	}
	l.recorded = exit.Cause
	return nil
}

// Close ends the log: any output not yet written, then the file.
func (l *checkLog) Close() error {
	err := l.flush()
	if closeErr := l.file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func combinedStatus(a, b Outcome) Outcome {
	if a == OutcomeError || b == OutcomeError {
		return OutcomeError
	}
	if a == OutcomeFailed || b == OutcomeFailed {
		return OutcomeFailed
	}
	return OutcomePassed
}

// nodeCheckWorkPath gives each real worktree a unique final path segment. Git
// uses that segment for its administrative directory under the shared bare
// repository, so two checkouts both named work would share worktrees/work.
// Workflow jobs use a local clone instead and keep their existing path.
func nodeCheckWorkPath(runDir, runID string, check nodeCheck) string {
	name := "work"
	if check.job == nil {
		name = "work-" + runID + "-" + string(check.id)
	}
	return path.Join(runDir, string(check.id), name)
}

// nodeWorktreeCommand serializes metadata mutations on one bare repository.
// The lock belongs to the repository, not to a run, so separate engine
// processes and parallel checks cannot prune or replace each other's metadata.
func nodeWorktreeCommand(cache, arguments string) string {
	return "flock " + shellquote.Quote(cache+".worktrees.lock") + " git --git-dir=" + shellquote.Quote(cache) + " worktree " + arguments
}

// teardownScript stops what a run's checks left on the machine, without removing
// the run folder: the containers first, so a scope keeps its place until its
// container has stopped, then each check's scope and detached unit, then the
// worktrees. It fails when a container is still running afterwards. The run
// folder is left to the removal that stops the run's keeper first: deleting it
// here would take the keeper's process ID away before anything could stop it.
func teardownScript(node Node, cache, runDir, runID string, checks []nodeCheck) string {
	script := jobs.UnitStoppedFunction + "set -e; docker=(docker); "
	if node.SSH == "" {
		script += "if ! docker info >/dev/null 2>&1; then docker=(sudo -n docker); fi; "
	}
	q := shellquote.Quote
	// A status query that fails proves nothing: only a query that answers and
	// finds nothing left counts. Each one exits 69 on a failed query and on a
	// survivor, and the caller then keeps the run's workspace and record.
	script += `no_container() { local out; out=$(timeout 15s "${docker[@]}" ps --format '{{.Names}}' "${@:2}") || { echo 'container status unavailable' >&2; return 69; }; if printf '%s\n' "$out" | grep -qx -- "$1"; then echo 'container cleanup failed' >&2; return 69; fi; }; `
	script += `no_labelled() { local out; out=$(timeout 15s "${docker[@]}" ps -q "$@") || { echo 'container status unavailable' >&2; return 69; }; if [ -n "$out" ]; then echo 'container cleanup failed' >&2; return 69; fi; }; `
	for _, check := range checks {
		name := "dyna-ci-" + runID + "-" + string(check.id)
		// A check known only by its id, such as one outside the current suite
		// selection, may have started a container or a workflow job, so both are
		// cleaned.
		unknown := !check.host && check.job == nil && check.image == ""
		if check.image != "" || unknown {
			script += `timeout 20s "${docker[@]}" rm -f ` + q(name) + " >/dev/null 2>&1 || true; "
			script += "no_container " + q(name) + "; "
		}
		if check.job != nil || unknown {
			filters := "--filter " + q("label=dyna.run="+runID) + " --filter " + q("label=dyna.check="+string(check.id))
			script += `ids=$(timeout 15s "${docker[@]}" ps -aq ` + filters + `) || { echo 'container status unavailable' >&2; exit 69; }; `
			script += `if [ -n "$ids" ]; then timeout 30s "${docker[@]}" rm -f $ids >/dev/null 2>&1 || true; fi; `
			script += "no_labelled " + filters + "; "
		}
		// A host check's scope is forced down, and the user manager must confirm it
		// is gone.
		if check.host || unknown {
			scope := q(name + ".scope")
			// A scope the user manager already reports down needs no forced stop, so
			// the query comes first and the stop and kill are skipped for it. An
			// active or restarted scope, or one whose state cannot be read, takes
			// the forced stop and the confirmation after it, exactly as before.
			script += "if unit_stopped " + scope + "; then :; else "
			script += "timeout 10s systemctl --user stop " + scope + " >/dev/null 2>&1 || true; "
			script += "systemctl --user kill --signal=KILL " + scope + " >/dev/null 2>&1 || true; "
			script += "if command -v systemctl >/dev/null 2>&1; then unit_stopped " + scope + " || { echo 'check scope is not confirmed stopped' >&2; exit 69; }; fi; "
			script += "fi; "
		}
		// The check's detached unit goes last: a check that ended on its own has
		// nothing left to stop, and one that a lost poll left behind dies here. A
		// unit that survives, or whose state cannot be read, fails the teardown.
		script += "bash -c " + q(jobs.StopScript) + " dyna-ci " + q(detachedDir(runDir, check.id)) + " " + q(checkUnit(runID, check.id)) + "; "
		script += nodeWorktreeCommand(cache, "remove --force "+q(nodeCheckWorkPath(runDir, runID, check))) + " >/dev/null 2>&1 || true; "
	}
	return script + nodeWorktreeCommand(cache, "prune")
}

// nodeSetupScript prepares one run on its node. The prepare stage repairs a
// linked go-build or go-mod path under the exclusive cache lock, before the run
// takes its ordinary shared lock, and refuses any remaining unsafe link. A run
// therefore never mounts or trims a cache that Dyna does not own.
func nodeSetupScript(base, reports, active string, cacheNames []string) string {
	q := shellquote.Quote
	// A setup that already started this run's keeper but lost the answer is
	// repeated: the keeper holds the run's lock and a share of the cache lock, so
	// the repeat skips the locking and only completes what is left.
	keeper := q(keeperPID(active))
	return "set -e; umask 077; " + baseLinkGuard(base) +
		"mkdir -p " + q(base) + " " + q(reports) + "; " +
		"if [ -s " + keeper + " ] && kill -0 \"$(cat " + keeper + ")\" 2>/dev/null; then :; else " +
		cacheRepairScript(base, admissionWait) + cacheLinkGuard(base, cacheNames) + runLocks(base, active) + "fi"
}

// killGroupLeftovers kills every process in the wrapper's group except the
// wrapper itself and its parent. The parent is the detached wrapper that
// writes the check's exit record after the check returns, and it shares the
// group. A check that leaves a child behind, such as a compiler daemon or a
// shell that outlived its deadline, must not hold the check's output pipe or
// the next check's room on the node.
const killGroupLeftovers = `pgid=$(ps -o pgid= -p $$ | tr -d " "); for p in $(pgrep -g "$pgid" 2>/dev/null); do [ "$p" = "$$" ] || [ "$p" = "$PPID" ] || kill -KILL "$p" 2>/dev/null || true; done`

// survivorSweep removes what a failed teardown left on the node: containers
// labelled dyna.run and dyna-ci-* systemd units that are older than six hours
// and belong to no live run. A run is live while its keeper holds the lock on
// its .active file. Nothing records such a survivor, so every check start on the
// node looks for them. It reaches containers the way the checks do, both the plain
// check containers and the act job containers. A query that fails or an age that cannot be read keeps the
// thing, and the sweep never fails the check.
func survivorSweep(base, containerSocket string) string {
	q := shellquote.Quote
	if containerSocket == "" {
		containerSocket = machines.DefaultContainerSocket
	}
	script := `base=` + q(base) + `; cutoff=$(( $(date +%s) - ` + strconv.Itoa(staleCheckTmpMinutes*60) + ` )); ` +
		// live_run takes a container's run ID or a unit's name without its dyna-ci- prefix.
		`live_run() { local d id; for d in "$base"/runs/*/; do [ -e "${d}.active" ] || continue; flock -n "${d}.active" true 2>/dev/null && continue; id=$(basename "$d"); case "$1" in "$id"|"$id"-*) return 0 ;; esac; done; return 1; }; ` +
		`old_enough() { local at; [ -n "$1" ] && at=$(date -d "$1" +%s 2>/dev/null) && [ "$at" -lt "$cutoff" ]; }; ` +
		`sweep_containers() { command -v "${docker[0]}" >/dev/null 2>&1 || return 0; ` +
		`timeout 15s "${docker[@]}" ps -a --filter label=dyna.run --format '{{.ID}} {{.Label "dyna.run"}}' 2>/dev/null | ` +
		`while read -r cid rid; do [ -n "$cid" ] && ! live_run "$rid" && old_enough "$(timeout 15s "${docker[@]}" inspect -f '{{.Created}}' "$cid" 2>/dev/null)" && timeout 30s "${docker[@]}" rm -f "$cid" >/dev/null 2>&1; done; }; ` +
		// A plain check container uses the node's ambient docker, with sudo when
		// that refuses. An act job container uses podman when docker is absent and
		// the machine's socket. Each daemon is swept the way its checks reach it.
		`( docker=(docker); if command -v docker >/dev/null 2>&1 && ! timeout 10s docker info >/dev/null 2>&1; then docker=(sudo -n docker); fi; sweep_containers ); ` +
		`( docker=(docker); command -v docker >/dev/null 2>&1 || docker=(podman); export DOCKER_HOST=` + q(containerSocket) + `; sweep_containers ); ` +
		`if command -v systemctl >/dev/null 2>&1; then timeout 15s systemctl --user list-units --all --plain --no-legend 'dyna-ci-*' 2>/dev/null | ` +
		`while read -r first second _; do unit=$first; [ "$first" = "●" ] && unit=$second; [ -n "$unit" ] || continue; live_run "${unit#dyna-ci-}" && continue; ` +
		`when=$(timeout 10s systemctl --user show -p ActiveEnterTimestamp --value "$unit" 2>/dev/null); [ -n "$when" ] || when=$(timeout 10s systemctl --user show -p InactiveEnterTimestamp --value "$unit" 2>/dev/null); ` +
		`old_enough "$when" && { timeout 10s systemctl --user stop "$unit" >/dev/null 2>&1; systemctl --user reset-failed "$unit" >/dev/null 2>&1; }; done; fi; `
	return "( " + script + "true ) || true; "
}

// noteScript writes at the start of a check's log what Dyna changed about its
// request. The evidence file then holds the clamp next to the result.
func noteScript(note string) string {
	if note == "" {
		return ""
	}
	return "printf '%s\\n' " + shellquote.Quote(note) + "; "
}

// staleCheckTmpMinutes is how old a check's temporary folder must be before the
// next check on the node may remove it. No check runs for that long.
const staleCheckTmpMinutes = 360

// checkTmpMarker is the file that marks a folder as a check's own. The sweep
// removes only folders that hold it, so a folder that merely has a matching name
// in a shared temporary directory is never touched.
const checkTmpMarker = ".dyna-ci-check"

// checkTmpFolder makes a host check's own temporary folder and sets isolated to
// it. The folder is made inside the machine's check-tmp setting, else inside the
// node's own temporary directory (TMPDIR, else /tmp), never under the run's
// source folder, which can sit on slow storage. The check's exit trap removes
// it. A check that was killed outright skips its trap, so the next check first
// removes folders of this owner under the same parent that carry the marker, are
// older than six hours and that no process uses (as its working folder, an open file or a
// TMPDIR or HOME in its environment). Nothing builds up in a node's memory.
func checkTmpFolder(setting string) string {
	parent := `"${TMPDIR:-/tmp}"`
	if setting != "" {
		parent = shellquote.Quote(setting)
	}
	sweep := `in_use() { grep -qaF -e "$1/" /proc/[0-9]*/environ 2>/dev/null || ls -l /proc/[0-9]*/cwd /proc/[0-9]*/fd 2>/dev/null | grep -qF -e " -> $1"; }; ` +
		`mkdir -p "$tmpparent" || { echo "local CI: cannot make the temporary folder parent $tmpparent" >&2; exit 70; }; ` +
		`find "$tmpparent" -mindepth 1 -maxdepth 1 -type d -name 'dyna-ci.*' -user "$(id -un)" -mmin +` + strconv.Itoa(staleCheckTmpMinutes) + ` -print 2>/dev/null | ` +
		`while IFS= read -r old; do [ -f "$old/` + checkTmpMarker + `" ] && ! in_use "$old" && rm -rf -- "$old"; done || true; `
	// The check does not start without its own folder: it would run with the
	// node's real temporary and home folders instead.
	make := `isolated=$(mktemp -d -p "$tmpparent" dyna-ci.XXXXXX) && : > "$isolated/` + checkTmpMarker + `" || { echo "local CI: cannot make the check's temporary folder in $tmpparent" >&2; exit 70; }; `
	return "tmpparent=" + parent + "; " + sweep + make
}

func nodeCheckScript(node Node, base, cache, runDir, runID, sha, baseCommit string, check nodeCheck, timeout time.Duration, port int, noExpand bool, actImage, containerSocket, taggedWorkflow string) string {
	work := nodeCheckWorkPath(runDir, runID, check)
	reportDir := path.Join(runDir, "reports", string(check.id))
	name := "dyna-ci-" + runID + "-" + string(check.id)
	q := shellquote.Quote
	seconds := int64((timeout + time.Second - 1) / time.Second)
	timing := `printf '{"durationNanos":%s}\n' "$(( $(date +%s%N) - started ))" > ` + q(path.Join(reportDir, "execution.json")) + "; "
	// A container that never started is an infrastructure failure, not a failing
	// test, so the node records whether the command inside it began. The sentinel
	// is a file because the check's own output can be longer than the tail a run
	// keeps.
	startedMarker := `if [ -e ` + q(path.Join(reportDir, commandStartedFile)) + ` ]; then printf 'DYNA_CI_COMMAND_STARTED=1\n'; else printf 'DYNA_CI_COMMAND_STARTED=0\n'; fi; `
	cleanup := "check_status=$?; " + timing + `if [ "$check_entered" = 1 ]; then { printf '\nDYNA_CI_CHECK_EXIT=%s\n' "$check_status"; ` + startedMarker + `} | emit; fi; `
	if !check.host {
		cleanup += `"${docker[@]}" rm -f ` + q(name) + " >/dev/null 2>&1 || true; "
	}
	if check.job != nil {
		// A job check's workspace is copied into the job container, so its
		// repository must stand alone: a worktree's .git points back at the
		// node's cache, which does not exist inside the container, and the
		// workflow's own git steps then fail. A local clone keeps the objects
		// shared with the cache and takes the commit under the ref the runner
		// pushed.
		cleanup += "rm -rf " + q(work) + " >/dev/null 2>&1 || true"
	} else {
		cleanup += nodeWorktreeCommand(cache, "remove --force "+q(work)) + " >/dev/null 2>&1 || true"
	}
	// The exit markers go to the trailer file the detached wrapper appends to the
	// log after the output has drained, past the limit it puts on the output.
	// Without that file they go to the output.
	emit := `emit() { if [ -n "${DYNA_CI_TRAILER:-}" ]; then cat >> "$DYNA_CI_TRAILER"; else cat; fi; }; `
	setup := emit + survivorSweep(base, containerSocket) + noteScript(check.resources.Note) + "mkdir -p " + q(reportDir) + "; started=$(date +%s%N); check_entered=0; docker=(docker); trap " + q(cleanup) + " EXIT; trap 'exit 143' HUP INT TERM; mkdir -p " + q(path.Dir(work)) + "; "
	if check.job != nil {
		// The checkout is a branch, not a detached commit: act copies the
		// workspace into the job container file by file, so an empty refs tree
		// never arrives and a detached commit leaves the copy a directory git
		// refuses. A named ref is a file and lands with the copy.
		setup += gitNodeStep("git clone --quiet --no-checkout "+q(cache)+" "+q(work)) +
			gitNodeStep("git -C "+q(work)+" fetch --quiet "+q(cache)+" +refs/dyna/ci/*:refs/dyna/ci/*") +
			gitNodeStep("git -C "+q(work)+" checkout --quiet -B dyna-ci "+q(sha))
	} else {
		setup += gitNodeStep(nodeWorktreeCommand(cache, "add --detach "+q(work)+" "+q(sha)))
	}
	for _, cache := range check.caches {
		setup += "mkdir -p " + q(repositoryCachePath(base, cache.Name)) + "; "
	}
	if check.job != nil {
		// act starts the job in containers of its own; the wrapper is a host
		// process, so the queue admission, the deadline and the cleanup stay exactly
		// what they are for every other check.
		artifacts := path.Join(reportDir, "artifacts")
		cachePath := path.Join(base, "actcache")
		label := "dyna.run=" + runID
		// The event is the run's own input, not a report: a file named *.json in
		// the report directory is read as a Go event stream, which rejects it.
		event := path.Join(path.Dir(work), "event.json")
		eventBody := `{"pull_request":{"base":{"sha":"` + baseCommit + `"},"head":{"sha":"` + sha + `"}}}`
		actEvent := "pull_request"
		if check.event == "push" {
			// A cache-warming job runs only on the default branch, so the run
			// sends the push event that names that ref.
			eventBody = `{"ref":"refs/heads/` + check.branch + `","after":"` + sha + `","before":"` + baseCommit + `"}`
			actEvent = "push"
		}
		setup += "cd " + q(work) + "; check_entered=1; check_status=0; " + installActScript(base)
		// act names a job's containers after the workflow and the job alone, so
		// two act processes that share a workflow fight over one container name,
		// starting with the job every other job needs: nine checks of one run
		// each started the workflow's "changes" job and collided. Each check
		// runs a tagged copy of the workflow outside its checkout, which names
		// every container it starts, its volumes included, alone. The workflow
		// name is the one value a job can read of it (github.workflow); a
		// repository that keys on that name sees the tagged one under act. The
		// engine wrote the tagged copy from the commit's bytes, so the checkout
		// keeps exactly what the commit holds and a job's own git commands see
		// no edit.
		tagged := path.Join(path.Dir(work), "workflow.yml")
		setup += "printf '%s' " + q(taggedWorkflow) + " > " + q(tagged) + "; "
		setup += "printf '%s' " + q(eventBody) + " > " + q(event) + "; "
		// act sees no host environment: a session token, an account key or a
		// harness variable on this machine must never reach a job container. The
		// allow-list is the act.env file next to the event, which is empty unless
		// a repository needs one.
		actHome := path.Join(reportDir, "home")
		allowList := path.Join(reportDir, "act.env")
		setup += "mkdir -p " + q(actHome) + "; : > " + q(allowList) + "; "
		// The runtime socket is the machine's setting, not the host environment:
		// a rootless Podman machine names its own socket and act talks to it.
		if containerSocket == "" {
			containerSocket = machines.DefaultContainerSocket
		}
		setup += "docker=(docker); command -v docker >/dev/null 2>&1 || docker=(podman); export DOCKER_HOST=" + q(containerSocket) + "; "
		setup += fmt.Sprintf("timeout --foreground --signal=TERM --kill-after=10s %ds env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin HOME="+q(actHome)+" DOCKER_HOST="+q(containerSocket)+" ", seconds) +
			q(actBinaryPath(base)) + " " + actEvent + " -W " + q(tagged) +
			" --env-file " + q(allowList) +
			" -j " + q(check.job.Name) + matrixFlags(check.job.Matrix) + " -e " + q(event) + " --rm -P " + q("ubuntu-latest="+actImage) +
			" --cache-server-path " + q(cachePath) + " --artifact-server-path " + q(artifacts) +
			// The queue's port index names this check's artifact port, so checks
			// of this node, whatever run they belong to, bind another one.
			" --artifact-server-port " + strconv.Itoa(actArtifactPortBase+port) +
			// act mounts the host's container socket into job containers by default.
			// A repository's workflow must not reach the host's runtime through it.
			" --container-daemon-socket -" +
			// Expand the identity on the node, before act parses the options. A
			// literal command substitution would reach Docker unchanged.
			" --container-options " + q(actContainerOptions(check, runID)+" --user ") + `"$(id -u):$(id -g)"` + " || check_status=$?; "
		// --rm covers the normal case; a cancelled job can leave containers, so the
		// labels remove exactly this check's, never a sibling check's.
		setup += `"${docker[@]}" ps -aq --filter ` + q("label="+label) + " --filter " + q("label=dyna.check="+string(check.id)) + ` | xargs -r "${docker[@]}" rm -f >/dev/null 2>&1 || true; `
		// The workflow uploads its JUnit as an artifact, and an upload is stored as
		// <artifact>/<artifact>.zip; the existing reader reads the check's report
		// directory, so the archives are unpacked into it first.
		// A job uploads its JUnit as XML and, for a Go job, the raw test events
		// as JSON, which the Go reader needs for failure detail.
		setup += "find " + q(artifacts) + " \\( -name '*.xml' -o -name '*.json' \\) -type f -exec cp -t " + q(reportDir) + " {} + 2>/dev/null || true; "
		// Each archive is unpacked into a directory of its own and every entry
		// keeps the archive's name and its path inside it, so two uploads that
		// both hold junit.xml land as two reports instead of overwriting one.
		unpacked := q(path.Join(reportDir, "unpacked"))
		// Every entry gets its own ordinal, so two entries never share a name,
		// whatever their paths inside the archive look like.
		setup += "rm -rf " + unpacked + "; mkdir -p " + unpacked + "; archiveOrdinal=0; " +
			"while IFS= read -r archive; do archiveOrdinal=$((archiveOrdinal + 1)); " +
			`artifact=$(basename "$archive" .zip); dest=` + unpacked + `/$archiveOrdinal-$artifact; mkdir -p "$dest"; ` +
			`if command -v unzip >/dev/null 2>&1; then unzip -qq -o "$archive" -d "$dest" >/dev/null 2>&1 || true; ` +
			`elif command -v python3 >/dev/null 2>&1; then python3 -m zipfile -e "$archive" "$dest" >/dev/null 2>&1 || true; fi; ` +
			`entryOrdinal=0; while IFS= read -r entry; do entryOrdinal=$((entryOrdinal + 1)); ` +
			`cp "$entry" ` + q(reportDir) + `/"$archiveOrdinal-$entryOrdinal.${entry##*.}" || true; ` +
			`done < <(find "$dest" \( -name '*.xml' -o -name '*.json' \) -type f 2>/dev/null); ` +
			`done < <(find ` + q(artifacts) + ` -name '*.zip' -type f 2>/dev/null); ` +
			"rm -rf " + unpacked + "; "
		setup += "( exit $check_status )"
		return setup
	}
	if check.host {
		environment := map[string]string{
			"RUNNER_TEMP": reportDir, "GITHUB_STEP_SUMMARY": path.Join(reportDir, "step-summary.md"),
			"JUNIT_REPORT_PATH": path.Join(reportDir, string(check.id)+"-test.xml"),
			"GIT_CONFIG_COUNT":  "1", "GIT_CONFIG_KEY_0": "safe.directory", "GIT_CONFIG_VALUE_0": work,
		}
		for _, cache := range check.caches {
			if cache.Env != "" {
				environment[cache.Env] = repositoryCachePath(base, cache.Name)
			}
		}
		for key, value := range check.env {
			environment[key] = value
		}
		serverPort := ""
		if _, set := environment["SCCACHE_SERVER_PORT"]; !set {
			serverPort = " SCCACHE_SERVER_PORT=" + strconv.Itoa(sccachePortBase+port)
		}
		// The check gets no owner bus, runtime folder or display. Its runtime
		// folder is its own temporary folder, and its session bus address names a
		// socket that nothing listens on, so a check that asks for a bus fails
		// instead of reaching the owner's desktop. A repository opts back in
		// through the check's env, which is applied after these values.
		commandEnv := `env -i PATH="$PATH" HOME="$isolated/home" XDG_DATA_HOME="$isolated/data" XDG_CONFIG_HOME="$isolated/config" XDG_CACHE_HOME="$isolated/cache" TMPDIR="$isolated" TMP="$isolated" TEMP="$isolated" GOTMPDIR="$isolated" XDG_RUNTIME_DIR="$isolated" DBUS_SESSION_BUS_ADDRESS="unix:path=$isolated/no-session-bus"` + hostEnvironment(environment) + serverPort
		// sccache starts its server in a new session, outside the scope and
		// process-group cleanup. Stop it through the check's own port while the
		// server's temporary folder still exists. No running server, a missing
		// sccache binary and a stop error must not change the check's status.
		stopSccache := "if command -v sccache >/dev/null 2>&1; then timeout --kill-after=1s 5s " + commandEnv + " sccache --stop-server >/dev/null 2>&1 || true; fi; "
		setup += "cd " + q(work) + "; : > " + q(path.Join(reportDir, commandStartedFile)) + "; " + checkTmpFolder(node.CheckTmp) + "trap " + q(cleanup+"; "+stopSccache+`rm -rf "$isolated"`) + " EXIT; "
		// --foreground keeps the check in the wrapper's process group, so what
		// the check leaves behind is in the wrapper's group too: timeout still
		// enforces the deadline on the check itself, and after it returns the
		// wrapper kills every other member of the group. A child that held the
		// output pipe open is gone before the caller waits on the pipe, so the
		// ErrWaitDelay path is a fallback rather than the way out.
		// The wrapper stays outside the check's scope: it starts the command
		// inside it, and once the command returns it kills what is left in the
		// scope, so a descendant that started its own session cannot hold the
		// output pipe and keep the transport up.
		scopeArgs := []string{"--user", "--scope", "--quiet"}
		if noExpand {
			scopeArgs = append(scopeArgs, systemdver.NoExpandFlag)
		}
		limits := check.resources.Scope(true)
		scopeArgs = append(scopeArgs, "--unit", name,
			"-p", "CPUQuota="+strconv.Itoa(limits.CPUs*100)+"%",
			"-p", "MemoryMax="+jobs.SystemdMemory(limits.MemoryMiB),
			"-p", "TasksMax="+strconv.Itoa(limits.TasksMax),
			"--", "nice", "-n", "10")
		scope := "systemd-run"
		for _, arg := range scopeArgs {
			scope += " " + q(arg)
		}
		// A SIGKILL to the scope comes first: a detached child that ignores
		// SIGTERM must not hold the check's transport for the manager's stop
		// timeout. `systemctl kill` without --kill-who/--kill-whom works on every
		// systemd that runs a check (the option differs between 249 and 250, and
		// the default is every process in the unit). The stop is bounded, and a
		// scope that is still active afterwards is reported in the check's log
		// rather than hidden.
		unitName := name + ".scope"
		unit := q(unitName)
		release := "systemctl --user kill --signal=KILL " + unit + " >/dev/null 2>&1 || true; " +
			"timeout 5s systemctl --user stop " + unit + " >/dev/null 2>&1 || true; " +
			"if systemctl --user is-active --quiet " + unit + " 2>/dev/null; then printf 'local CI: the check scope %s is still active after cleanup\\n' " + q(unitName) + " >&2; fi; " +
			"systemctl --user reset-failed " + unit + " >/dev/null 2>&1 || true; "
		setup += "check_entered=1; check_status=0; " + scope + fmt.Sprintf(" timeout --foreground --signal=TERM --kill-after=10s %ds ", seconds) + commandEnv + " bash -o pipefail -c " + q(check.inner) + " || check_status=$?; " + release + killGroupLeftovers + "; ( exit $check_status )"
		return setup
	}
	if node.SSH == "" {
		setup += "if ! docker info >/dev/null 2>&1; then docker=(sudo -n docker); if ! \"${docker[@]}\" info >/dev/null 2>&1; then echo 'Docker access is unavailable' >&2; exit 69; fi; fi; "
	}
	inner := "mkdir -p /tmp/home /tmp/data /tmp/config /tmp/cache; : > /reports/" + commandStartedFile + "; " + check.inner
	user := " --user $(id -u):$(id -g)"
	if check.hostUser {
		user = ""
		prepare := ""
		if check.setup != "" {
			prepare = "(" + check.setup + ") || exit 69; "
		}
		inner = prepare + "(getent group $CI_GID >/dev/null || groupadd --gid $CI_GID dyna-ci) && (getent passwd $CI_UID >/dev/null || useradd --no-log-init --uid $CI_UID --gid $CI_GID --no-create-home --home-dir /tmp/home dyna-ci) && exec setpriv --reuid=$CI_UID --regid=$CI_GID --clear-groups bash -c " + q(inner)
	}
	// A stable source path lets build tools reuse results across run IDs. Each
	// check keeps a separate host worktree and report directory.
	args := `"${docker[@]}" run --rm --init --cpus=` + strconv.Itoa(check.resources.CPUs) +
		" --memory=" + jobs.DockerMemory(check.resources.MemoryMiB) + " --memory-swap=" + jobs.DockerMemory(check.resources.MemoryMiB) +
		` --pids-limit=512 --tmpfs /tmp:rw,exec,nosuid,nodev,size=1g --label dyna.run=` + q(runID) + ` --name ` + q(name) + user +
		" -v " + q(work+":/workspace") + " -v " + q(cache+":"+cache) + " -v " + q(reportDir+":/reports") + " -w /workspace"
	environment := map[string]string{
		"HOME": "/tmp/home", "XDG_DATA_HOME": "/tmp/data", "XDG_CONFIG_HOME": "/tmp/config", "XDG_CACHE_HOME": "/tmp/cache",
		"RUNNER_TEMP": "/reports", "GITHUB_STEP_SUMMARY": "/reports/step-summary.md",
		"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "safe.directory", "GIT_CONFIG_VALUE_0": "/workspace", "GITHUB_WORKSPACE": "/workspace",
		"JUNIT_REPORT_PATH": "/reports/" + string(check.id) + "-test.xml",
	}
	for _, cache := range check.caches {
		args += " -v " + q(repositoryCachePath(base, cache.Name)+":"+cache.Target)
		if cache.Env != "" {
			environment[cache.Env] = cache.Target
		}
	}
	for key, value := range check.env {
		environment[key] = value
	}
	// A check that starts sccache must not lose it to another check's cleanup:
	// the port follows the queue index this check holds, and the cache directory
	// stays where the repository put it.
	if _, set := environment["SCCACHE_SERVER_PORT"]; !set {
		args += " -e SCCACHE_SERVER_PORT=" + strconv.Itoa(sccachePortBase+port)
	}
	args += dockerEnvironment(environment) + " -e CI_UID=$(id -u) -e CI_GID=$(id -g) " + q(check.image)
	// Both host and container deadlines survive a disconnected SSH client.
	setup += fmt.Sprintf("check_entered=1; timeout --signal=TERM --kill-after=10s %ds ", seconds+15) + args + fmt.Sprintf(" timeout --signal=TERM --kill-after=10s %ds bash -c ", seconds) + q(inner)
	return setup
}

func dockerEnvironment(environment map[string]string) string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		out.WriteString(" -e ")
		out.WriteString(shellquote.Quote(key + "=" + environment[key]))
	}
	return out.String()
}

func hostEnvironment(environment map[string]string) string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		out.WriteByte(' ')
		out.WriteString(shellquote.Quote(key + "=" + environment[key]))
	}
	return out.String()
}
