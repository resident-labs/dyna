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
	"sync"
	"time"

	"dyna-agent/internal/cibudget"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/store"
)

// admissionWait matches the explicit queue bound passed to the shared host gate.
const admissionWait = 55 * time.Minute

// hostAdmissionGate is installed with the runner hooks, never loaded from the
// revision under test. All installed hook copies must share this protocol.
const hostAdmissionGate = "/var/tmp/dyna-ci/ci-runner-slot.sh"

type Command func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error

type Runner struct {
	Remote  string
	DataDir string
	Command Command
	Node    *Node
}
type Request struct {
	// Parallelism limits this run without changing the shared node slot limit.
	// Zero uses all node slots.
	Parallelism int
	Repo        string
	RepoDir     string
	RoomID      string
	SHA         string
	Suites      []string
	Timeout     time.Duration
	MaxTest     time.Duration
	Allowed     map[string]bool
	// OnStart runs after node setup, before the first check starts.
	OnStart func(context.Context) error
	// DeferSave lets the scheduler apply room budgets before publishing a result.
	DeferSave bool
}
type SuiteResult struct {
	Suite     string        `json:"suite"`
	Node      string        `json:"node,omitempty"`
	CheckName string        `json:"checkName,omitempty"`
	Duration  time.Duration `json:"duration"`
	SlowTests []TestTiming  `json:"slowTests,omitempty"`
	Status    string        `json:"status"`
	LogTail   string        `json:"logTail,omitempty"`
	Failures  []string      `json:"failures,omitempty"`
	Reports   []string      `json:"reports,omitempty"`
	Error     string        `json:"error,omitempty"`
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
	Status   string        `json:"status"`
	Started  time.Time     `json:"started"`
	Duration time.Duration `json:"duration"`
	Suites   []SuiteResult `json:"suites"`
	Checks   []SuiteResult `json:"checks,omitempty"`
	Error    string        `json:"error,omitempty"`
}

func command(ctx context.Context, dir string, in io.Reader, out, stderr io.Writer, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	c.Env = processguard.MetadataEnv()
	c.Stdin = in
	c.Stdout = out
	c.Stderr = stderr
	return c.Run()
}
func (r Runner) run(ctx context.Context, dir string, out io.Writer, name string, args ...string) error {
	fn := r.Command
	if fn == nil {
		fn = command
	}
	return fn(ctx, dir, nil, out, out, name, args...)
}
func (r Runner) ssh(ctx context.Context, host, script string, out io.Writer) error {
	return r.run(ctx, "", out, "ssh", "-o", "BatchMode=yes", host, script)
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

// DefaultSuites returns the complete suite set used for a trial run.
func DefaultSuites() []string { return []string{"relay", "frontend", "go", "cross-build"} }

// Run returns completed suite failures in Result. Setup failures also return an error.
// Go requires frontend assets. Cross-build also waits when frontend is selected.
// Live computer-smoke tests are intentionally excluded.
func (r Runner) Run(ctx context.Context, req Request) (result Result, err error) {
	if req.Repo == "" {
		req.Repo = req.RepoDir
	}
	if r.Node != nil {
		return r.runNodeWithDiskRetry(ctx, req)
	}
	suites := append([]string(nil), req.Suites...)
	if len(suites) == 0 {
		suites = DefaultSuites()
	}
	seen := map[string]bool{}
	for _, s := range suites {
		switch s {
		case "relay", "frontend", "go", "cross-build":
		default:
			return result, fmt.Errorf("unknown CI suite %q", s)
		}
		if seen[s] {
			return result, fmt.Errorf("duplicate CI suite %q", s)
		}
		seen[s] = true
	}
	if seen["go"] && !seen["frontend"] {
		suites = append(suites, "frontend")
	}
	if req.Timeout <= 0 {
		req.Timeout = 15 * time.Minute
	}
	if req.MaxTest <= 0 {
		req.MaxTest = 10 * time.Second
	}
	repo, e := filepath.Abs(req.Repo)
	if e != nil {
		return result, e
	}
	var sha bytes.Buffer
	ref := req.SHA
	if ref == "" {
		ref = "HEAD"
	}
	if err = r.run(ctx, repo, &sha, "git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}"); err != nil {
		return result, fmt.Errorf("resolve commit: %w: %s", err, &sha)
	}
	common, e := r.commonDir(ctx, repo)
	if e != nil {
		return result, e
	}
	result = Result{Repo: common, RoomID: req.RoomID, SHA: strings.TrimSpace(sha.String()), Status: "error", Started: time.Now().UTC()}
	if !regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(result.SHA) {
		return result, fmt.Errorf("git returned invalid commit SHA")
	}
	dir := r.resultDir(common, result.SHA)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return result, err
	}
	defer func() {
		result.Duration = time.Since(result.Started)
		if err != nil {
			result.Error = err.Error()
			result.Status = "error"
		}
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
			return
		}
		if req.DeferSave {
			return
		}
		e := (Store{DataDir: r.root()}).SaveResult(context.Background(), result)
		if e != nil {
			err = errors.Join(err, e)
		}
	}()
	remote := r.Remote
	if remote == "" {
		v, e := settings.Load("")
		if e != nil {
			return result, e
		}
		remote = v.LocalCIRemote
	}
	host, base, e := settings.ParseLocalCIRemote(remote)
	if e != nil {
		return result, e
	}
	cache := path.Join(base, repoKey(common)+".git")
	runID := fmt.Sprintf("%s-%d", result.SHA[:12], time.Now().UnixNano())
	runDir := path.Join(base, "runs", runID)
	work := path.Join(runDir, "work")
	reports := path.Join(runDir, "reports")
	reportRoot, e := os.MkdirTemp(dir, "run-")
	if e != nil {
		return result, e
	}
	controlBudget := time.Duration(len(suites))*req.Timeout + 2*time.Minute
	for _, suite := range suites {
		if suite == "go" || suite == "cross-build" {
			controlBudget += admissionWait + req.Timeout
		}
	}
	sshCtx, cancel := context.WithTimeout(ctx, controlBudget)
	defer cancel()
	var log bytes.Buffer
	if err = r.ssh(sshCtx, host, "mkdir -p "+shellquote.Quote(base)+"; if [ ! -d "+shellquote.Quote(cache)+" ]; then git init --bare "+shellquote.Quote(cache)+"; fi", &log); err != nil {
		return result, fmt.Errorf("initialize cache: %w: %s", err, &log)
	}
	log.Reset()
	if err = r.run(sshCtx, repo, &log, "git", "push", host+":"+cache, result.SHA+":refs/dyna/ci/"+result.SHA); err != nil {
		return result, fmt.Errorf("push commit: %w: %s", err, &log)
	}
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		var output bytes.Buffer
		var containers []string
		for _, suite := range suites {
			containers = append(containers, shellquote.Quote("dyna-ci-"+runID+"-"+suite))
		}
		script := "docker rm -f " + strings.Join(containers, " ") + " >/dev/null 2>&1 || true; git --git-dir=" + shellquote.Quote(cache) + " worktree remove --force " + shellquote.Quote(work) + " >/dev/null 2>&1 || true; rm -rf --one-file-system " + shellquote.Quote(runDir) + " || true"
		if e := r.ssh(cleanupCtx, host, script, &output); e != nil {
			err = errors.Join(err, fmt.Errorf("cleanup: %w: %s", e, &output))
		}
	}()
	log.Reset()
	// The run holds a lock on its own directory while it runs, so a cleanup can
	// tell a live run from one that died and never trims the caches it mounts.
	active := path.Join(runDir, ".active")
	setup := "set -e; mkdir -p " + shellquote.Quote(reports) + " " + shellquote.Quote(path.Join(base, "go-build")) + " " + shellquote.Quote(path.Join(base, "go-mod")) + " " + shellquote.Quote(path.Join(base, "npm")) + " " + shellquote.Quote(path.Dir(active)) + " && git --git-dir=" + shellquote.Quote(cache) + " worktree add --detach " + shellquote.Quote(work) + " " + shellquote.Quote(result.SHA) + "; : > " + shellquote.Quote(active) + "; " + runKeeper(active) + " &"
	if err = r.ssh(sshCtx, host, setup, &log); err != nil {
		return result, fmt.Errorf("create worktree: %w: %s", err, &log)
	}
	result.Suites = make([]SuiteResult, len(suites))
	frontendDone := make(chan struct{})
	var frontendOK bool
	if !seen["go"] && !seen["frontend"] {
		close(frontendDone)
	}
	var wg sync.WaitGroup
	for i, suite := range suites {
		wg.Add(1)
		go func(i int, suite string) {
			defer wg.Done()
			sr := SuiteResult{Suite: suite, Status: "passed"}
			if suite == "frontend" {
				defer close(frontendDone)
			}
			if suite == "go" || (suite == "cross-build" && (seen["go"] || seen["frontend"])) {
				select {
				case <-frontendDone:
				case <-sshCtx.Done():
					sr.Status = "error"
					sr.Error = sshCtx.Err().Error()
					result.Suites[i] = sr
					return
				}
				if !frontendOK {
					sr.Status = "error"
					sr.Error = "frontend assets unavailable"
					result.Suites[i] = sr
					return
				}
			}
			started := time.Now()
			// Admission is outside execution time. The remote supervisor starts
			// its hard deadline only after the shared host gate grants a slot.
			controlTimeout := req.Timeout + time.Minute
			if suite == "go" || suite == "cross-build" {
				controlTimeout += admissionWait + req.Timeout
			}
			suiteCtx, stop := context.WithTimeout(sshCtx, controlTimeout)
			defer stop()
			var output tailBuffer
			container := "dyna-ci-" + runID + "-" + suite
			script := containerScript(base, cache, work, reports, container, suite, req.Timeout)
			e := r.ssh(suiteCtx, host, script, &output)
			sr.Duration = time.Since(started)
			sr.LogTail = output.String()
			if e != nil {
				sr.Status = "error"
				sr.Error = e.Error()
				var exit *exec.ExitError
				if errors.As(e, &exit) && exit.ExitCode() != 255 && exit.ExitCode() != 75 && exit.ExitCode() != 124 && exit.ExitCode() != 137 && suiteCtx.Err() == nil {
					sr.Status = "failed"
				}
			}
			// A full disk on the machine is not a failing test.
			if sr.Status == "failed" && diskFull(sr.LogTail+"\n"+sr.Error) {
				sr.Status, sr.Error = "error", "check ran out of disk space on the CI machine"
			}
			if suite == "frontend" {
				frontendOK = e == nil
			}
			result.Suites[i] = sr
		}(i, suite)
	}
	wg.Wait()
	// Retrieve reports even when a suite failed. Never extract paths supplied by tar.
	archive, archiveOpenErr := os.CreateTemp(reportRoot, "reports-*.tar")
	if archiveOpenErr != nil {
		return result, archiveOpenErr
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	var archiveErr error
	fn := r.Command
	if fn == nil {
		fn = command
	}
	var stderr tailBuffer
	// Select reports at the source, including only the named bounded diagnostics log.
	script := "cd " + shellquote.Quote(reports) + " && find . -mindepth 2 -maxdepth 2 -type f \\( -name '*.xml' -o -name '*.json' -o -name 'ci-diagnostics.jsonl' \\) -print0 | tar -C " + shellquote.Quote(reports) + " --null -T - -cf -"
	archiveErr = fn(sshCtx, "", nil, &boundedWriter{writer: archive, remaining: 70 << 20}, &stderr, "ssh", "-o", "BatchMode=yes", host, "bash -o pipefail -c "+shellquote.Quote(script))
	if archiveErr == nil {
		if _, e := archive.Seek(0, io.SeekStart); e != nil {
			archiveErr = e
		} else {
			archiveErr = readArchive(archive, reportRoot)
		}
	}
	result.Status = "passed"
	for i := range result.Suites {
		sr := &result.Suites[i]
		if archiveErr != nil {
			sr.Status = "error"
			sr.Error = fmt.Sprintf("copy reports: %v: %s", archiveErr, stderr.String())
		} else {
			diagnostics := filepath.Join(reportRoot, "reports", sr.Suite, "ci-diagnostics.jsonl")
			if info, e := os.Stat(diagnostics); e == nil && info.Mode().IsRegular() {
				sr.Reports = append(sr.Reports, diagnostics)
			}
			if e := checkReports(sr, reportRoot, req.MaxTest, req.Allowed); e != nil {
				sr.Status = "error"
				sr.Error = e.Error()
			}
		}
		if sr.Status == "error" {
			result.Status = "error"
		} else if sr.Status == "failed" && result.Status == "passed" {
			result.Status = "failed"
		}
	}
	return result, nil
}

func containerScript(base, cache, work, reports, name, suite string, timeout time.Duration) string {
	image := "node:22"
	inner := "bash scripts/ci.sh " + suite
	if suite == "go" || suite == "cross-build" {
		image = "golang:1.26.3"
	}
	if suite == "go" {
		inner = "apt-get update -qq && apt-get install -y -qq shellcheck >/dev/null && " +
			"(getent group $CI_GID >/dev/null || groupadd --gid $CI_GID dyna-ci) && " +
			"(getent passwd $CI_UID >/dev/null || useradd --no-log-init --uid $CI_UID --gid $CI_GID --no-create-home --home-dir /tmp dyna-ci) && " +
			"exec setpriv --reuid=$CI_UID --regid=$CI_GID --clear-groups bash scripts/ci.sh go"
	}
	reportDir := path.Join(reports, suite)
	operation := "run"
	if suite == "go" || suite == "cross-build" {
		operation = "create"
	}
	// Docker init reaps orphaned test children before they exhaust the PID limit.
	args := "docker " + operation + " --rm --init --cpus=2 --memory=4g --memory-swap=4g --pids-limit=512 --name " + shellquote.Quote(name) + " -v " + shellquote.Quote(work+":"+work) + " -v " + shellquote.Quote(cache+":"+cache) + " -v " + shellquote.Quote(reportDir+":/reports") + " -v " + shellquote.Quote(path.Join(base, "go-build")+":/cache/build") + " -v " + shellquote.Quote(path.Join(base, "go-mod")+":/cache/mod") + " -v " + shellquote.Quote(path.Join(base, "npm")+":/cache/npm") + " -w " + shellquote.Quote(work) + " -e HOME=/tmp -e RUNNER_TEMP=/reports -e GITHUB_STEP_SUMMARY=/reports/step-summary.md -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod -e npm_config_cache=/cache/npm -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=" + shellquote.Quote(work) + " -e CI_UID=$(id -u) -e CI_GID=$(id -g) -e JUNIT_REPORT_PATH=/reports/" + suite + "-test.xml"
	if suite == "go" || suite == "cross-build" {
		args += " -e GOMAXPROCS=2 -e GOMEMLIMIT=3GiB -e GOFLAGS=-p=2"
	}
	if suite != "go" {
		args += " --user $(id -u):$(id -g)"
	}
	seconds := fmt.Sprintf("%d", int64((timeout+time.Second-1)/time.Second))
	// The daemon also owns an execution deadline if the host keeper is killed.
	args += " " + image + " timeout --signal=TERM --kill-after=10s " + seconds + "s bash -c " + shellquote.Quote(inner)
	if operation == "create" {
		gate := shellquote.Quote(hostAdmissionGate)
		probe := "if ! protocol=$(timeout 30s env -u GITHUB_JOB bash " + gate + " --protocol) || [ \"$protocol\" != dyna-ci-container-slot-v1 ]; then echo \"unsupported host Go admission protocol\" >&2; exit 75; fi; "
		return probe + "mkdir -p " + shellquote.Quote(reportDir) + " && CI_SLOT_WAIT=" + fmt.Sprintf("%d", int(admissionWait.Seconds())) + " bash " + gate + " --container " + shellquote.Quote(name) + " " + seconds + " -- " + args
	}
	return "mkdir -p " + shellquote.Quote(reportDir) + "; trap 'docker rm -f " + name + " >/dev/null 2>&1 || true' EXIT HUP INT TERM; timeout --signal=TERM --kill-after=10s " + fmt.Sprintf("%ds", int(timeout.Seconds())+1) + " " + args
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
func checkReports(sr *SuiteResult, dir string, max time.Duration, allowed map[string]bool) error {
	switch sr.Suite {
	case "cross-build", "frontend-check", "go-static", "computer-smoke":
		return nil
	}
	files, err := filepath.Glob(filepath.Join(dir, "reports", sr.Suite, "*.xml"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		if sr.Status != "passed" {
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
	if sr.Suite == "go" || strings.HasPrefix(sr.Suite, "go-test-shard-") {
		streams, e := filepath.Glob(filepath.Join(dir, "reports", sr.Suite, "*.json"))
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
		if sr.Status == "passed" {
			sr.Status = "failed"
		}
		sr.Failures = append(sr.Failures, report.Failures...)
		for _, test := range report.BudgetTests() {
			if test.Duration > max && !test.Allowed(allowed) {
				sr.Failures = append(sr.Failures, "OVER BUDGET "+test.ID())
			}
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
