// Package localci runs the shared CI suites on bounded local and SSH nodes.
package localci

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"dyna-agent/internal/cibudget"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/store"
)

// admissionWait matches the explicit queue bound passed to the shared host gate.
const admissionWait = 55 * time.Minute

type Command func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error

type Runner struct {
	DataDir string
	Command Command
	// OnCacheRepair reports a run that switched linked managed caches back to
	// real directories, so the caller can publish one typed event. The runner
	// reports it even when the run fails afterwards.
	OnCacheRepair CacheRepairReporter
	// Node is the registered CI machine that runs the checks. A run needs one:
	// there is no other execution pipeline.
	Node *Node
}
type Request struct {
	// Definition is the validated graph at SHA. A nil definition loads
	// DefinitionPath from SHA before any node work starts.
	Definition *Definition
	// Parallelism limits this run without changing the shared node slot limit.
	// Zero uses all node slots.
	Parallelism int
	Repo        string
	RepoDir     string
	RoomID      string
	SHA         string
	// Base is the commit the change is compared against, which a workflow job
	// sees as github.event.pull_request.base.sha.
	Base    string
	Suites  []Suite
	Timeout time.Duration
	MaxTest time.Duration
	Allowed map[string]bool
	// OnStart runs after node setup, before the first check starts.
	OnStart func(context.Context) error
	// DeferSave lets the scheduler apply room budgets before publishing a result.
	DeferSave bool
	// Event is the GitHub event a job check runs, "pull_request" by default.
	// A cache-warming job runs only on the default branch, so a warm run sends
	// "push" and names Branch in the payload.
	Event  string
	Branch string
}

// Outcome is how one local CI check or a whole run ended. A check that could
// not test the commit reports OutcomeError, which is an infrastructure cause and
// not a test failure. The values are stored in saved results and sent to other
// machines, so their spelling must not change.
type Outcome string

const (
	OutcomePassed Outcome = "passed"
	OutcomeFailed Outcome = "failed"
	OutcomeError  Outcome = "error"
)

// Conclusive reports an outcome that no later run will change. All three
// outcomes are final; only a run still in progress has none of them.
func (o Outcome) Conclusive() bool {
	return o == OutcomePassed || o == OutcomeFailed || o == OutcomeError
}

// Decided reports an outcome that says whether the commit passed its checks. An
// infrastructure error is conclusive but decides nothing, so CI waits.
func (o Outcome) Decided() bool { return o == OutcomePassed || o == OutcomeFailed }

type SuiteResult struct {
	Suite     Suite         `json:"suite"`
	Node      string        `json:"node,omitempty"`
	CheckName CheckName     `json:"checkName,omitempty"`
	Group     Group         `json:"group,omitempty"`
	Aggregate bool          `json:"aggregate,omitempty"`
	Duration  time.Duration `json:"duration"`
	SlowTests []TestTiming  `json:"slowTests,omitempty"`
	Status    Outcome       `json:"status"`
	Cause     FailureCause  `json:"cause,omitempty"`
	LogTail   string        `json:"logTail,omitempty"`
	Failures  []string      `json:"failures,omitempty"`
	Reports   []string      `json:"reports,omitempty"`
	Error     string        `json:"error,omitempty"`
	// Note says what Dyna changed about the check's request, such as a CPU or
	// memory request it clamped to the machine. It is empty when the check got
	// what it asked for.
	Note string `json:"note,omitempty"`
	// Warnings are what the runner had to clean up after a check that still
	// decided its commit, so the evidence says what the pass cost the machine.
	Warnings []CleanupWarning `json:"warnings,omitempty"`
}

// Name is the name a check reports under: its own name, or its suite for a result
// saved before checks had names.
func (s SuiteResult) Name() CheckName {
	if s.CheckName != "" {
		return s.CheckName
	}
	return CheckName(s.Suite)
}

type TestTiming struct {
	Name     string        `json:"name"`
	Duration time.Duration `json:"duration"`
}
type Result struct {
	Repo     string        `json:"repo"`
	RoomID   string        `json:"roomID,omitempty"`
	Node     string        `json:"node,omitempty"`
	SHA      string        `json:"sha"`
	Status   Outcome       `json:"status"`
	Cause    FailureCause  `json:"cause,omitempty"`
	Started  time.Time     `json:"started"`
	Duration time.Duration `json:"duration"`
	Suites   []SuiteResult `json:"suites"`
	Checks   []SuiteResult `json:"checks,omitempty"`
	Error    string        `json:"error,omitempty"`
	// RetriedFrom names the machine whose infrastructure failure moved this run
	// to another machine. A result that carries it is that retry, so a second
	// infrastructure failure ends the run instead of walking around the fleet.
	RetriedFrom string `json:"retriedFrom,omitempty"`
	// Resumed marks a result that Dyna itself stopped and then replaced with
	// another run on the same machine. One marker bounds that retry, so a run the
	// engine keeps stopping cannot loop for ever.
	Resumed bool `json:"resumed,omitempty"`
}

// CleanupWarning is a cleanup the runner did after a check that still decided
// its commit. It never changes the verdict.
type CleanupWarning string

const (
	// CleanupLeftoverOutput means the check's command exited, but a child it
	// started kept the check's output pipe open. os/exec reports that as
	// ErrWaitDelay; the runner kills what is left of the command's group.
	CleanupLeftoverOutput CleanupWarning = "leftover-output"
)

// Label is the sentence the checks view shows for a cleanup warning.
func (w CleanupWarning) Label() string {
	switch w {
	case CleanupLeftoverOutput:
		return "a process left by the check held its output open; the runner killed it"
	}
	return ""
}

// FailureCause is why a check or a run stopped without testing the commit, or
// failed the tests. A check's cause says why that check could not test the
// change; a run's cause says the same for the run as a whole, such as a node
// that went away. The value tells an infrastructure cause from a real test
// failure in a saved result and in a failure report. The values are stored, so
// their spelling must not change.
type FailureCause string

const (
	// CauseTestFailure is a check that ran its tests and failed them.
	CauseTestFailure FailureCause = "test-failure"
	// CauseOverBudget is a check whose tests all passed but one of them ran over
	// the room's per-test time budget. The check fails, yet no test is broken:
	// the cause keeps a person from debugging a test that only needs to be
	// faster, or needs its fixture shrunk.
	CauseOverBudget FailureCause = "over-budget"
	// CauseContainerFailure is a check that stopped before its command ran: the
	// container, its setup, or the worktree it needs was unavailable. A process
	// that something killed is a container failure inside a container, where the
	// usual cause is a resource limit.
	CauseContainerFailure FailureCause = "container-failure"
	// CauseSSHDrop is a check whose SSH client lost the connection to the node.
	// Only a connection failure proves this; an unmarked local error never does.
	CauseSSHDrop FailureCause = "ssh-drop"
	// CauseTimeout is a check or a run that reached its own deadline, or a check
	// whose command reported the timeout status. A bare SIGKILL is not a timeout.
	CauseTimeout FailureCause = "timeout"
	// CauseCancelled is a check or a run that its caller stopped.
	CauseCancelled FailureCause = "cancelled"
	// CauseEngineStop is a check or a run that Dyna itself stopped because its
	// engine was shutting down. It proves nothing about the commit, unlike a
	// cancellation that a superseded head or an owner asked for, so the same
	// commit is run again.
	CauseEngineStop FailureCause = "engine-stop"
	// CauseDiskFull is a check that ran out of disk space on the node.
	CauseDiskFull FailureCause = "disk-full"
	// CauseReports is a check whose report files were missing or could not be
	// copied back from the node.
	CauseReports FailureCause = "reports-unavailable"
	// CauseUnclassified is a failure whose output proves no more specific cause.
	CauseUnclassified FailureCause = "unclassified"
	// CauseScheduling is a run that never started: preparing it failed, such as a
	// fetch of the trial head or base. No check ran, so nothing in the result says
	// anything about the commit.
	CauseScheduling FailureCause = "scheduling"
)

// Label is how a cause reads to a person. The stored value never changes; only a
// view, the command line and a report convert it here.
func (c FailureCause) Label() string {
	switch c {
	case CauseTestFailure:
		return "failed tests"
	case CauseOverBudget:
		return "over the time budget"
	case CauseContainerFailure:
		return "container failure"
	case CauseSSHDrop:
		return "SSH connection lost"
	case CauseTimeout:
		return "timeout"
	case CauseCancelled:
		return "cancelled"
	case CauseEngineStop:
		return "engine stopping"
	case CauseDiskFull:
		return "disk full"
	case CauseReports:
		return "reports unavailable"
	case CauseUnclassified:
		return "cause not proven"
	case CauseScheduling:
		return "the run could not start"
	}
	return ""
}

// command runs one local command in the agent's own cgroup. Cancellation kills
// its whole process group and bounds the pipe drain, so a check's shell that
// dies cannot leave a child timeout or docker holding the output pipe open and
// block the run before its cleanup.
func command(ctx context.Context, dir string, in io.Reader, out, stderr io.Writer, name string, args ...string) error {
	c := processguard.Command(ctx, name, args...)
	c.Dir = dir
	c.Env = processguard.MetadataEnv()
	c.Stdin = in
	c.Stdout = out
	c.Stderr = stderr
	err := runAndReap(c)
	// A cancelled command reports the context, not the signal that stopped it.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// runAndReap runs cmd and cleans up after a command that exited while a child
// it started kept the output pipe open, which os/exec reports as ErrWaitDelay
// once cmd.WaitDelay passes. The command's group id is its own pid, so what is
// left of the group is killed here instead of outliving the check.
func runAndReap(c *exec.Cmd) error {
	return runAndReapWith(c.Run, c, reapGroup)
}

// runAndReapWith is runAndReap with the run and the reaper given, so a test can
// prove the decision without a real wait deadline or a real child.
func runAndReapWith(run func() error, c *exec.Cmd, reap func(int) error) error {
	err := run()
	if errors.Is(err, exec.ErrWaitDelay) && c.Process != nil {
		_ = reap(c.Process.Pid)
	}
	return err
}

// reapGroup kills what is left of a command's process group, whose id is the
// command's own pid. Tests replace it to observe the pid without racing a
// zombie.
var reapGroup = func(pid int) error { return syscall.Kill(-pid, syscall.SIGKILL) }

func (r Runner) run(ctx context.Context, dir string, out io.Writer, name string, args ...string) error {
	fn := r.Command
	if fn == nil {
		fn = command
	}
	return fn(ctx, dir, nil, out, out, name, args...)
}
func repoKey(repo string) string {
	sum := sha256.Sum256([]byte(repo))
	return hex.EncodeToString(sum[:16])
}
func (r Runner) root() string {
	if r.DataDir != "" {
		return r.DataDir
	}
	return store.DataDir()
}
func (r Runner) resultDir(repo, sha string) string {
	return filepath.Join(r.root(), "local-ci", repoKey(repo), sha)
}

func (r Runner) Show(repo, sha string) (Result, error) {
	var result Result
	if !regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(sha) {
		return result, fmt.Errorf("expected a full commit SHA")
	}
	common, err := r.commonDir(context.Background(), repo)
	if err != nil {
		return result, err
	}
	return (Store{DataDir: r.root()}).LoadResult(context.Background(), common, sha)
}

func (r Runner) commonDir(ctx context.Context, repo string) (string, error) {
	var out bytes.Buffer
	if err := r.run(ctx, repo, &out, "git", "rev-parse", "--path-format=absolute", "--git-common-dir"); err != nil {
		return "", fmt.Errorf("resolve repository: %w: %s", err, &out)
	}
	return strings.TrimSpace(out.String()), nil
}

// Run runs the checks on the registered node and saves the result. There is one
// execution pipeline: a caller selects a node first, so a nil node fails here
// before any command runs. Suite failures also return them in Result; a setup
// failure also returns an error.
func (r Runner) Run(ctx context.Context, req Request) (result Result, err error) {
	if req.Repo == "" {
		req.Repo = req.RepoDir
	}
	if r.Node == nil {
		return result, errors.New("local CI needs a registered node: none was selected")
	}
	return r.runNodeWithDiskRetry(ctx, req)
}

type boundedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("report archive exceeds 70 MiB")
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

type tailBuffer struct{ data []byte }

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.data = append(b.data, p...)
	if len(b.data) > 16384 {
		b.data = b.data[len(b.data)-16384:]
	}
	return n, nil
}
func (b *tailBuffer) String() string { return string(b.data) }

func readArchive(input io.Reader, dir string) error {
	tr := tar.NewReader(input)
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg || path.IsAbs(name) || name == ".." || strings.HasPrefix(path.Clean(name), "../") {
			return fmt.Errorf("unsafe report path %q", h.Name)
		}
		if path.Ext(name) != ".xml" && path.Ext(name) != ".json" && path.Base(name) != "ci-diagnostics.jsonl" {
			continue
		}
		if path.Base(name) == "ci-diagnostics.jsonl" && h.Size > 64<<10 {
			return fmt.Errorf("diagnostics exceed 64 KiB")
		}
		total += h.Size
		if h.Size < 0 || total > 64<<20 {
			return fmt.Errorf("reports exceed 64 MiB")
		}
		target := filepath.Join(dir, "reports", filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, tr)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
}
func checkReports(sr *SuiteResult, mode ReportMode, dir string, max time.Duration, allowed map[string]bool) error {
	if mode == ReportNone {
		return nil
	}
	files, err := filepath.Glob(filepath.Join(dir, "reports", string(sr.Suite), "*.xml"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		if sr.Status != OutcomePassed {
			return nil
		}
		return fmt.Errorf("%s produced no JUnit reports", sr.Suite)
	}
	var report cibudget.Report
	for _, file := range files {
		f, e := os.Open(file)
		if e != nil {
			return e
		}
		e = report.ReadJUnit(f)
		f.Close()
		if e != nil {
			return fmt.Errorf("%s: %w", file, e)
		}
		sr.Reports = append(sr.Reports, file)
	}
	// Go's JUnit exporter records timing; its JSON stream retains failure details.
	if mode == ReportGo {
		streams, e := filepath.Glob(filepath.Join(dir, "reports", string(sr.Suite), "*.json"))
		if e != nil {
			return e
		}
		for _, file := range streams {
			if filepath.Base(file) == "execution.json" {
				continue
			}
			f, e := os.Open(file)
			if e != nil {
				return e
			}
			var execution cibudget.Report
			e = execution.ReadGo(f)
			f.Close()
			if e != nil {
				return e
			}
			report.Failures = append(report.Failures, execution.Failures...)
			sr.Reports = append(sr.Reports, file)
		}
	}
	if len(report.Tests) == 0 {
		return fmt.Errorf("%s report contains no completed tests", sr.Suite)
	}
	var output bytes.Buffer
	if !report.Write(&output, max, allowed) {
		// Only a real failure fails the check here. A test that ran over the
		// room's per-test budget is recorded with its own cause and its time;
		// whether that fails the check is the room's decision, made by the
		// watcher from ciTimeBudget, so a slow run on warn still merges.
		if len(report.Failures) > 0 {
			if sr.Status == OutcomePassed {
				sr.Status = OutcomeFailed
			}
			sr.Failures = append(sr.Failures, report.Failures...)
		}
		overBudget := 0
		for _, test := range report.BudgetTests() {
			if test.Duration > max && !test.Allowed(allowed) {
				overBudget++
			}
		}
		// A test that only ran too long did not fail. Its own cause says the
		// budget was missed, so nothing points a person at a broken test.
		if len(report.Failures) == 0 && overBudget > 0 && sr.Cause == "" {
			sr.Cause = CauseOverBudget
		}
	}
	tests := report.BudgetTests()
	sort.SliceStable(tests, func(i, j int) bool { return tests[i].Duration > tests[j].Duration })
	for i, test := range tests {
		if i == 10 {
			break
		}
		sr.SlowTests = append(sr.SlowTests, TestTiming{Name: test.ID(), Duration: test.Duration})
	}
	sr.LogTail = tail(sr.LogTail + "\n" + output.String())
	return nil
}
func tail(s string) string {
	if len(s) > 16384 {
		return s[len(s)-16384:]
	}
	return s
}

// engineStopping records that this process is stopping the engine, so a run this
// stop cancels reports CauseEngineStop rather than a plain cancellation.
var engineStopping atomic.Bool

// MarkEngineStopping tells the runner that the engine is shutting down.
func MarkEngineStopping() { engineStopping.Store(true) }

// EngineStopping reports whether the engine is shutting down.
func EngineStopping() bool { return engineStopping.Load() }

// ClearEngineStopping forgets the mark, for a test that shares the process.
func ClearEngineStopping() { engineStopping.Store(false) }
