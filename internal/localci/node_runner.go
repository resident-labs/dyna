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

	"dyna-agent/internal/shellquote"
)

// The two steps of a cancelled run's teardown, on a machine that may answer
// slowly. A caller that stops a run and waits for that teardown must allow both.
const (
	// containerCleanupTimeout bounds the node command that stops a run's
	// containers, scopes and worktrees.
	containerCleanupTimeout = time.Minute
	// runRemovalTimeout bounds the node command that stops the run's keeper,
	// which then removes the run directory and releases its locks.
	runRemovalTimeout = 2 * time.Minute
	// TeardownBudget is how long a cancelled run can take to leave its machine:
	// it stops the containers and then the keeper, in that order.
	TeardownBudget = containerCleanupTimeout + runRemovalTimeout
)

type nodeCheck struct {
	id       Suite
	name     CheckName
	group    Group
	inner    string
	image    string
	host     bool
	hostUser bool
	setup    string
	env      map[string]string
	caches   []CacheDefinition
	reports  ReportMode
	priority int
	// cpus and memoryMiB are what the repository asked for, and resources is
	// what the node granted.
	cpus      int
	memoryMiB int64
	resources checkResources
}

// A check's default resources. The slot pool hands every check two CPUs and the
// default container memory, so a repository that asks for nothing keeps exactly
// the limits it had before a check could ask for more.
const (
	defaultCheckCPUs          = 2
	defaultContainerMemoryMiB = int64(4096)
	defaultHostMemoryMiB      = int64(5120)
	// hostTasksMax bounds the processes one host check may start.
	hostTasksMax = 768
)

// checkResources is the CPU count and memory one check runs with, after the node
// bounded the repository's request.
type checkResources struct {
	CPUs      int
	MemoryMiB int64
	// Note says what Dyna clamped, and is empty when the check got what it
	// asked for.
	Note string
}

// clampCheckResources bounds a check's request by the machine. The slot pool
// gives two CPUs and the default container memory per slot, and a probe of the
// machine bounds both again. A request within the bound is granted. A default is
// never clamped, so a small machine keeps the limits its checks already had.
func clampCheckResources(check nodeCheck, node Node) checkResources {
	resources := checkResources{CPUs: defaultCheckCPUs, MemoryMiB: defaultContainerMemoryMiB}
	if check.host {
		resources.MemoryMiB = defaultHostMemoryMiB
	}
	if check.cpus > 0 {
		resources.CPUs = check.cpus
	}
	if check.memoryMiB > 0 {
		resources.MemoryMiB = check.memoryMiB
	}
	cpuLimit := node.Slots * defaultCheckCPUs
	memoryLimit := int64(node.Slots) * defaultContainerMemoryMiB
	if node.CPUs > 0 {
		cpuLimit = min(cpuLimit, node.CPUs)
	}
	if node.MemoryBytes > 0 {
		// A probe total sits a little below the nominal size, so the bound is
		// a whole GiB and reads as one number in the run report.
		probed := int64(node.MemoryBytes / (1 << 20))
		memoryLimit = min(memoryLimit, probed-probed%1024)
	}
	cpuLimit = max(cpuLimit, defaultCheckCPUs)
	memoryLimit = max(memoryLimit, defaultContainerMemoryMiB)
	if check.host {
		memoryLimit = max(memoryLimit, defaultHostMemoryMiB)
	}
	var clamped []string
	if resources.CPUs > cpuLimit {
		clamped = append(clamped, fmt.Sprintf("cpus from %d to %d", resources.CPUs, cpuLimit))
		resources.CPUs = cpuLimit
	}
	if resources.MemoryMiB > memoryLimit {
		clamped = append(clamped, "memory from "+memoryLabel(resources.MemoryMiB)+" to "+memoryLabel(memoryLimit))
		resources.MemoryMiB = memoryLimit
	}
	if len(clamped) > 0 {
		resources.Note = "Dyna clamped " + strings.Join(clamped, " and ")
	}
	return resources
}

// memoryLabel writes a memory size in MiB or GiB, as a person reads it.
func memoryLabel(mib int64) string {
	if mib%1024 == 0 {
		return strconv.FormatInt(mib/1024, 10) + " GiB"
	}
	return strconv.FormatInt(mib, 10) + " MiB"
}

// dockerMemory writes a memory size in the unit docker reads.
func dockerMemory(mib int64) string {
	if mib%1024 == 0 {
		return strconv.FormatInt(mib/1024, 10) + "g"
	}
	return strconv.FormatInt(mib, 10) + "m"
}

// systemdMemory writes a memory size in the unit systemd reads.
func systemdMemory(mib int64) string {
	if mib%1024 == 0 {
		return strconv.FormatInt(mib/1024, 10) + "G"
	}
	return strconv.FormatInt(mib, 10) + "M"
}

// scopeLimits bound one systemd scope.
type scopeLimits struct {
	CPUs      int
	MemoryMiB int64
	TasksMax  int
}

// defaultScopeLimits bound a scope that is not a check, such as node setup or
// cleanup.
var defaultScopeLimits = scopeLimits{CPUs: defaultCheckCPUs, MemoryMiB: defaultHostMemoryMiB, TasksMax: hostTasksMax}

// scope is the systemd scope limit a granted check runs in. A container keeps
// one GiB above its own memory limit for the docker client, as it always had. A
// host check has no container limit, so the scope is its only memory bound.
func (c checkResources) scope(host bool) scopeLimits {
	memory := c.MemoryMiB
	if !host {
		memory += 1024
	}
	return scopeLimits{CPUs: c.CPUs, MemoryMiB: memory, TasksMax: hostTasksMax}
}

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
			if command.Host && !allowHost {
				return nil, false, fmt.Errorf("%w: check %q", ErrHostChecksDisabled, check.ID)
			}
			seen[check.ID] = len(checks)
			checks = append(checks, nodeCheck{
				id: check.ID, name: check.Name, group: check.Group,
				inner: command.Command, image: command.Image,
				host: command.Host, hostUser: command.HostUser, setup: command.Setup,
				env: cloneStrings(command.Env), caches: append([]CacheDefinition(nil), command.Caches...),
				reports: command.Reports, priority: command.Priority,
				cpus: command.CPUs, memoryMiB: command.MemoryMiB,
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

func (r Runner) nodeCommand(ctx context.Context, out, stderr io.Writer, scope, script string) error {
	return r.nodeCommandScoped(ctx, out, stderr, scope, defaultScopeLimits, script)
}

// nodeCommandScoped runs a node script in a systemd scope with the given limits.
// A check passes the limits the node granted it, so a host check cannot use more
// of the machine than the repository asked for.
func (r Runner) nodeCommandScoped(ctx context.Context, out, stderr io.Writer, scope string, limits scopeLimits, script string) error {
	fn := r.Command
	if fn == nil {
		fn = command
	}
	name, args := "bash", []string{"-o", "pipefail", "-c", script}
	if scope != "" {
		name = "systemd-run"
		args = append([]string{"--user", "--scope", "--quiet", "--expand-environment=no", "--unit", scope,
			"-p", "CPUQuota=" + strconv.Itoa(limits.CPUs*100) + "%",
			"-p", "MemoryMax=" + systemdMemory(limits.MemoryMiB),
			"-p", "TasksMax=" + strconv.Itoa(limits.TasksMax),
			"--", "nice", "-n", "10", "bash", "-o", "pipefail", "-c"}, script)
	}
	if r.Node.SSH != "" {
		remote := shellquote.Quote(name)
		for _, arg := range args {
			remote += " " + shellquote.Quote(arg)
		}
		name, args = "ssh", []string{"-o", "BatchMode=yes"}
		args = append(args, r.Node.SSH, remote)
	}
	return fn(ctx, "", nil, out, stderr, name, args...)
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
		checks, aggregate, prepareErr = nodeChecks(req.Definition, req.Suites, node.AllowHostChecks)
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
	if !regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(sha) {
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
	runID := fmt.Sprintf("%s-%d", sha[:12], time.Now().UnixNano())
	runDir := path.Join(base, "runs", runID)
	// A run holds a lock on this file while it runs. A cleanup takes the lock to
	// learn that the run died, so a crashed run's directory never stays forever.
	active := path.Join(runDir, ".active")
	reports := path.Join(runDir, "reports")
	dir := r.resultDir(common, sha)
	if err := os.MkdirAll(dir, 0700); err != nil { // not-state: report blobs live outside SQLite.
		return result, err
	}
	reportRoot, err := os.MkdirTemp(dir, "run-") // not-state: report blobs live outside SQLite.
	if err != nil {
		return result, err
	}
	control, cancel := context.WithTimeout(ctx, time.Duration(len(checks))*(req.Timeout+admissionWait)+2*time.Minute)
	defer cancel()
	// A run removes its own directory on the node, whatever ended it: a finished
	// run left about half a gigabyte behind, and the machine filled its disk.
	// It is a separate call because the cleanup below stops at its first failed
	// container check (exit 69) and would then keep the directory. It runs on its
	// own context, so a cancelled run still removes it, and it runs last, after
	// the reports were copied back.
	defer func() {
		remove, stop := context.WithTimeout(context.WithoutCancel(ctx), runRemovalTimeout)
		defer stop()
		var log tailBuffer
		if e := r.nodeCommand(remove, &log, &log, "", removeRunDir(base, active)); e != nil {
			err = errors.Join(err, fmt.Errorf("remove run directory: %w: %s", e, log.String()))
		}
	}()
	output.Reset()
	// A machine that is close to a full disk cleans Dyna's own caches first, so
	// the checks do not die on a full disk halfway through.
	if e := r.cleanBeforeRun(control, node); e != nil {
		log.Printf("local CI %s: free space check: %v", node.Name, e)
	}
	setup := nodeSetupScript(base, cache, reports, active, nodeCacheNames(checks))
	setupErr := r.nodeCommand(control, &output, &output, "", setup)
	// A repair is a fact whether or not the run then succeeded, so report it
	// before the setup error returns.
	if kinds := parseCacheRepairs(output.String()); len(kinds) > 0 && r.OnCacheRepair != nil {
		r.OnCacheRepair(control, CacheRepair{Node: node.Name, Kinds: kinds})
	}
	if setupErr != nil {
		return result, fmt.Errorf("initialize node: %w: %s", setupErr, &output)
	}
	output.Reset()
	if node.SSH == "" {
		err = r.run(control, repo, &output, "git", "push", cache, sha+":refs/dyna/ci/"+sha)
	} else {
		err = r.run(control, repo, &output, "git", "push", node.SSH+":"+cache, sha+":refs/dyna/ci/"+sha)
	}
	if err != nil {
		return result, fmt.Errorf("push commit to node: %w: %s", err, &output)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), containerCleanupTimeout)
		defer stop()
		var log tailBuffer
		// Stop daemon-owned containers before scopes, so a scope keeps its slot
		// until its container has stopped, including after a lost SSH client.
		script := "set -e; docker=(docker); "
		if node.SSH == "" {
			script += "if ! docker info >/dev/null 2>&1; then docker=(sudo -n docker); fi; "
		}
		for _, check := range checks {
			name := "dyna-ci-" + runID + "-" + string(check.id)
			if check.image != "" {
				script += `timeout 20s "${docker[@]}" rm -f ` + shellquote.Quote(name) + " >/dev/null 2>&1 || true; "
			}
			if check.image != "" {
				script += `if [ "$(timeout 10s "${docker[@]}" inspect -f '{{.State.Running}}' ` + shellquote.Quote(name) + ` 2>/dev/null)" = true ]; then echo 'container cleanup failed' >&2; exit 69; fi; `
			}
			script += "timeout 10s systemctl --user stop " + shellquote.Quote(name+".scope") + " >/dev/null 2>&1 || true; "
			script += "git --git-dir=" + shellquote.Quote(cache) + " worktree remove --force " + shellquote.Quote(path.Join(runDir, string(check.id), "work")) + " >/dev/null 2>&1 || true; "
		}
		// The run directory itself is left to the removal defer, which stops the
		// run's keeper first: deleting the directory here would take the keeper's
		// process ID away before anything could stop it.
		script += "git --git-dir=" + shellquote.Quote(cache) + " worktree prune"
		if e := r.nodeCommand(cleanup, &log, &log, "", script); e != nil {
			err = errors.Join(err, fmt.Errorf("cleanup node: %w: %s", e, log.String()))
		}
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
	jobs := make(chan int, len(checks))
	order := make([]int, len(checks))
	for i := range checks {
		order[i] = i
	}
	// Higher-priority checks start first; equal priorities keep definition order.
	sort.SliceStable(order, func(i, j int) bool { return checks[order[i]].priority > checks[order[j]].priority })
	for _, index := range order {
		jobs <- index
	}
	close(jobs)
	var wg sync.WaitGroup
	workers := min(node.Slots, len(checks))
	if req.Parallelism > 0 {
		workers = min(workers, req.Parallelism)
	}
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
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
				runErr := r.nodeCommandScoped(checkCtx, writer, writer, "dyna-ci-"+runID+"-"+string(check.id), check.resources.scope(check.host), nodeCheckScript(node, base, cache, runDir, runID, sha, check, req.Timeout, i))
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
				// A full disk on the machine is not a failing test: report it as an
				// infrastructure error, so the run cleans up and retries, and hosted
				// CI resumes when it stays broken.
				if sr.Status == OutcomeFailed && diskFull(sr.LogTail+"\n"+sr.Error) {
					sr.Status, sr.Error, sr.Cause = OutcomeError, "check ran out of disk space on the CI machine", CauseDiskFull
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
			if sr.Status == OutcomePassed {
				sr.Status, sr.Error, sr.Cause = OutcomeError, "copy reports: "+copyErr.Error(), CauseReports
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
		result.Cause = CauseReports
		return result, fmt.Errorf("copy reports: %w", copyErr)
	}
	return result, nil
}

// fetchReports copies the node's report files into the local report blob folder.
// The caller treats a failure as evidence about a check and not as a reason to
// drop the run: the checks keep their logs either way.
func (r Runner) fetchReports(ctx context.Context, reportRoot, reports string) error {
	archive, err := os.CreateTemp(reportRoot, "reports-*.tar") // not-state: temporary report transport.
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	script := "cd " + shellquote.Quote(reports) + " && find . -mindepth 2 -maxdepth 2 -type f \\( -name '*.xml' -o -name '*.json' -o -name 'ci-diagnostics.jsonl' \\) -print0 | tar -C " + shellquote.Quote(reports) + " --null -T - -cf -"
	var stderr tailBuffer
	if err := r.nodeCommand(ctx, &boundedWriter{writer: archive, remaining: 70 << 20}, &stderr, "", script); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return readArchive(archive, reportRoot)
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

// checkMarker is the exit status the node writes after a check's command ran.
// Without it the check never reached its command or never came back.
var checkMarker = regexp.MustCompile(`(?m)^DYNA_CI_CHECK_EXIT=([0-9]+)$`)

// checkCause reads why a check stopped from what its own output proves: the
// deadline, the exit status the node recorded, and the marker it printed. It
// never calls an unmarked local error an SSH drop: only an SSH client that lost
// the connection returns 255 without the node's marker. Exit status 137 proves a
// SIGKILL, which is not a timeout: a container that was killed ran out of
// resources, and any other process was killed from outside.
func checkCause(node Node, ctx context.Context, err error, markers [][]string, container bool) FailureCause {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return CauseTimeout
		}
		return CauseCancelled
	}
	var exit *exec.ExitError
	hasExit := errors.As(err, &exit)
	if len(markers) == 0 {
		if hasExit {
			switch code := exit.ExitCode(); {
			case code == 255 && node.SSH != "":
				return CauseSSHDrop
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
		return CauseCancelled
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 255 && node.SSH != "" {
		return CauseSSHDrop
	}
	return CauseUnclassified
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
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return 0
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

// nodeSetupScript prepares one run on its node. The prepare stage repairs a
// linked go-build or go-mod path under the exclusive cache lock, before the run
// takes its ordinary shared lock, and refuses any remaining unsafe link. A run
// therefore never mounts or trims a cache that Dyna does not own.
func nodeSetupScript(base, cache, reports, active string, cacheNames []string) string {
	q := shellquote.Quote
	return "set -e; umask 077; " + baseLinkGuard(base) +
		"mkdir -p " + q(base) + " " + q(reports) + "; " +
		cacheRepairScript(base, admissionWait) + cacheLinkGuard(base, cacheNames) + runLocks(base, active) +
		"flock " + q(path.Join(base, "init.lock")) + " bash -c " + q("if [ ! -d "+q(cache)+" ]; then git init --bare "+q(cache)+"; fi")
}

// Slots belong to checks, across repositories and runner processes. A waiter
// tries every slot before blocking on one slot without polling.
func nodeSlotScript(base string, slots, index int, inner string) string {
	return "set -euo pipefail; mkdir -p " + shellquote.Quote(path.Join(base, "slots")) + "; acquired=0; " +
		fmt.Sprintf("for slot in $(seq 0 %d); do exec 9>\"%s/slots/$slot.lock\"; if flock -n 9; then acquired=1; break; fi; done; ", slots-1, base) +
		fmt.Sprintf("if [ \"$acquired\" = 0 ]; then exec 9>%s; flock -w %d 9 || exit 75; fi; ", shellquote.Quote(path.Join(base, "slots", fmt.Sprintf("%d.lock", index%slots))), int(admissionWait.Seconds())) + inner
}

// noteScript writes at the start of a check's log what Dyna changed about its
// request. The evidence file then holds the clamp next to the result.
func noteScript(note string) string {
	if note == "" {
		return ""
	}
	return "printf '%s\\n' " + shellquote.Quote(note) + "; "
}

func nodeCheckScript(node Node, base, cache, runDir, runID, sha string, check nodeCheck, timeout time.Duration, index int) string {
	work := path.Join(runDir, string(check.id), "work")
	reportDir := path.Join(runDir, "reports", string(check.id))
	name := "dyna-ci-" + runID + "-" + string(check.id)
	q := shellquote.Quote
	seconds := int64((timeout + time.Second - 1) / time.Second)
	timing := `printf '{"durationNanos":%s}\n' "$(( $(date +%s%N) - started ))" > ` + q(path.Join(reportDir, "execution.json")) + "; "
	cleanup := "check_status=$?; " + timing + `if [ "$check_entered" = 1 ]; then printf '\nDYNA_CI_CHECK_EXIT=%s\n' "$check_status"; fi; `
	if !check.host {
		cleanup += `"${docker[@]}" rm -f ` + q(name) + " >/dev/null 2>&1 || true; "
	}
	cleanup += "git --git-dir=" + q(cache) + " worktree remove --force " + q(work) + " >/dev/null 2>&1 || true"
	setup := noteScript(check.resources.Note) + "mkdir -p " + q(reportDir) + "; started=$(date +%s%N); check_entered=0; docker=(docker); trap " + q(cleanup) + " EXIT; trap 'exit 143' HUP INT TERM; mkdir -p " + q(path.Dir(work)) + "; git --git-dir=" + q(cache) + " worktree add --detach " + q(work) + " " + q(sha) + " || exit 69; "
	for _, cache := range check.caches {
		setup += "mkdir -p " + q(repositoryCachePath(base, cache.Name)) + "; "
	}
	if check.host {
		setup += "cd " + q(work) + "; isolated=$(mktemp -d); trap " + q(cleanup+`; rm -rf "$isolated"`) + " EXIT; "
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
		setup += fmt.Sprintf("check_entered=1; timeout --signal=TERM --kill-after=10s %ds env -i PATH=\"$PATH\" HOME=\"$isolated/home\" XDG_DATA_HOME=\"$isolated/data\" XDG_CONFIG_HOME=\"$isolated/config\" XDG_CACHE_HOME=\"$isolated/cache\" XDG_RUNTIME_DIR=\"${XDG_RUNTIME_DIR:-/run/user/$(id -u)}\" DBUS_SESSION_BUS_ADDRESS=\"${DBUS_SESSION_BUS_ADDRESS:-unix:path=/run/user/$(id -u)/bus}\"", seconds)
		setup += hostEnvironment(environment) + " bash -o pipefail -c " + q(check.inner)
		return nodeSlotScript(base, node.Slots, index, setup)
	}
	if node.SSH == "" {
		setup += "if ! docker info >/dev/null 2>&1; then docker=(sudo -n docker); if ! \"${docker[@]}\" info >/dev/null 2>&1; then echo 'Docker access is unavailable' >&2; exit 69; fi; fi; "
	}
	inner := "mkdir -p /tmp/home /tmp/data /tmp/config /tmp/cache; " + check.inner
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
		" --memory=" + dockerMemory(check.resources.MemoryMiB) + " --memory-swap=" + dockerMemory(check.resources.MemoryMiB) +
		` --pids-limit=512 --tmpfs /tmp:rw,exec,nosuid,nodev,size=1g --name ` + q(name) + user +
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
	args += dockerEnvironment(environment) + " -e CI_UID=$(id -u) -e CI_GID=$(id -g) " + q(check.image)
	// Both host and container deadlines survive a disconnected SSH client.
	setup += fmt.Sprintf("check_entered=1; timeout --signal=TERM --kill-after=10s %ds ", seconds+15) + args + fmt.Sprintf(" timeout --signal=TERM --kill-after=10s %ds bash -c ", seconds) + q(inner)
	return nodeSlotScript(base, node.Slots, index, setup)
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
