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
	"strconv"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/shellquote"
)

type nodeCheck struct {
	id    Suite
	name  CheckName
	inner string
	image string
}

// checkCommand is how a node runs one check: its container image and the command
// inside it. The suite description says which checks exist; this says what each
// one runs.
type checkCommand struct {
	image string
	inner string
}

var nodeCommands = func() map[Suite]checkCommand {
	commands := map[Suite]checkCommand{
		SuiteRelay:         {image: "node:22", inner: "bash scripts/ci.sh relay"},
		SuiteFrontend:      {image: "node:22", inner: "bash scripts/ci.sh frontend test"},
		SuiteFrontendCheck: {image: "node:22", inner: "bash scripts/ci.sh frontend check"},
		SuiteGoStatic:      {image: "golang:1.26.3", inner: "bash scripts/ci.sh go modules && bash scripts/ci.sh go vet && bash scripts/ci.sh go installer"},
		SuiteCrossBuild:    {image: "golang:1.26.3", inner: "bash scripts/ci.sh cross-build"},
		// The computer smoke test needs the node itself, not a container.
		SuiteComputerSmoke: {},
	}
	for i := 0; i < ShardCount; i++ {
		commands[shardID(i)] = checkCommand{image: "golang:1.26.3", inner: shardCommand(i)}
	}
	return commands
}()

// nodeChecks expands suites into the checks this node runs, in the order the
// suite description gives and with the go suite's expansion already applied.
func nodeChecks(suites []Suite) ([]nodeCheck, bool, error) {
	if len(suites) == 0 {
		suites = WorkflowSuites()
	}
	var checks []nodeCheck
	seen := map[Suite]bool{}
	aggregate := false
	for _, suite := range suites {
		expanded, ok := Checks(suite)
		if !ok {
			return nil, false, fmt.Errorf("unknown CI suite %q", suite)
		}
		for _, check := range expanded {
			if seen[check.ID] {
				continue
			}
			seen[check.ID] = true
			command, ok := nodeCommands[check.ID]
			if !ok {
				return nil, false, fmt.Errorf("no command for CI check %q", check.ID)
			}
			aggregate = aggregate || check.Group != ""
			checks = append(checks, nodeCheck{id: check.ID, name: check.Name, inner: command.inner, image: command.image})
		}
	}
	return checks, aggregate, nil
}

// shardCommand runs one go-test shard: its slice of the shared test set, and the
// middle shard also builds the desktop app.
func shardCommand(index int) string {
	inner := fmt.Sprintf("export CI_SHARD=%d/%d CI_SLICE=''; ", index, ShardCount)
	switch index {
	case 0:
		inner += "export CI_SLICE=0/2; "
	case ShardCount - 1:
		inner += "export CI_SLICE=1/2; "
	}
	inner += "bash scripts/ci.sh go build && bash scripts/ci.sh go test"
	if index == ShardCount/2 {
		inner += " && touch web/dist/index.html && bash scripts/ci.sh go desktop"
	}
	return inner
}

func (r Runner) nodeCommand(ctx context.Context, out, stderr io.Writer, scope, script string) error {
	fn := r.Command
	if fn == nil {
		fn = command
	}
	name, args := "bash", []string{"-o", "pipefail", "-c", script}
	if scope != "" {
		name = "systemd-run"
		args = append([]string{"--user", "--scope", "--quiet", "--expand-environment=no", "--unit", scope, "-p", "CPUQuota=200%", "-p", "MemoryMax=5G", "-p", "TasksMax=768", "--", "nice", "-n", "10", "bash", "-o", "pipefail", "-c"}, script)
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
	checks, aggregate, err := nodeChecks(req.Suites)
	if err != nil {
		return result, err
	}
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
		remove, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
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
	setup := "set -e; umask 077; mkdir -p " + shellquote.Quote(base) + " " + shellquote.Quote(reports) + "; " + runLocks(base, active) + "flock " + shellquote.Quote(path.Join(base, "init.lock")) + " bash -c " + shellquote.Quote("if [ ! -d "+shellquote.Quote(cache)+" ]; then git init --bare "+shellquote.Quote(cache)+"; fi")
	if err := r.nodeCommand(control, &output, &output, "", setup); err != nil {
		return result, fmt.Errorf("initialize node: %w: %s", err, &output)
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
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
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
	defer func() {
		result.Checks = append([]SuiteResult(nil), result.Suites...)
		if aggregate {
			status := OutcomePassed
			for _, check := range result.Suites {
				if GroupOf(check.Name()) == GroupGoTest {
					status = combinedStatus(status, check.Status)
				}
			}
			result.Checks = append(result.Checks, SuiteResult{Suite: SuiteGoTest, CheckName: GroupGoTest.Name(), Node: node.Name, Status: status})
		}
	}()
	jobs := make(chan int, len(checks))
	// Cross-build is the longest cold check. Start it beside the other suites.
	for i, check := range checks {
		if check.id == SuiteCrossBuild {
			jobs <- i
		}
	}
	for i, check := range checks {
		if check.id != SuiteCrossBuild {
			jobs <- i
		}
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
				sr := SuiteResult{Suite: check.id, CheckName: check.name, Node: node.Name, Status: "passed"}
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
				e := r.nodeCommand(checkCtx, writer, writer, "dyna-ci-"+runID+"-"+string(check.id), nodeCheckScript(node, base, cache, runDir, runID, sha, check, req.Timeout, i))
				if blobErr == nil {
					if closeErr := blob.Close(); closeErr != nil {
						log.Printf("local CI %s: finish check log: %v", node.Name, closeErr)
					}
					sr.Reports = append(sr.Reports, blob.path)
				}
				sr.LogTail = output.String()
				markers := checkMarker.FindAllStringSubmatch(sr.LogTail, -1)
				if e != nil || len(markers) == 0 {
					sr.Status, sr.Error = OutcomeError, "check command did not complete"
					if e != nil {
						sr.Error = e.Error()
					}
					sr.Cause = checkCause(node, checkCtx, e, markers)
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
	if control.Err() == nil {
		copyErr = r.fetchReports(control, reportRoot, reports)
	}
	result.Status = OutcomePassed
	for i := range result.Suites {
		sr := &result.Suites[i]
		// A check keeps what it proved. A report copy that failed makes an
		// undecided check an error with its cause, but it does not rewrite a
		// check that already reported its own outcome.
		if sr.Status == OutcomePassed && copyErr != nil && control.Err() == nil {
			sr.Status, sr.Error, sr.Cause = OutcomeError, "copy reports: "+copyErr.Error(), CauseReports
		}
		if control.Err() == nil && copyErr == nil {
			readReports(sr, reportRoot, req.MaxTest, req.Allowed)
		}
		result.Status = combinedStatus(result.Status, sr.Status)
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
// copied report files. A check that cannot show its result is an infrastructure
// error and never a passing one.
func readReports(sr *SuiteResult, reportRoot string, max time.Duration, allowed map[string]bool) {
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
	if e := checkReports(sr, reportRoot, max, allowed); e != nil {
		if sr.Status != OutcomeFailed {
			sr.Status = OutcomeError
			if sr.Cause == "" {
				sr.Cause = CauseReports
			}
		}
		sr.Error = e.Error()
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
// the connection returns 255 without the node's marker.
func checkCause(node Node, ctx context.Context, err error, markers [][]string) FailureCause {
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
			case code == 124, code == 137:
				return CauseTimeout
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
	case 75, 124, 137:
		return CauseTimeout
	case 143:
		return CauseCancelled
	case 0, 255:
		return CauseUnclassified
	}
	return CauseTestFailure
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
	file    *os.File
	path    string
	written int64
	dropped int64
	tail    []byte
	broken  bool
	failure error
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

// Close ends the log with the size of the output it dropped and the end of the
// output.
func (l *checkLog) Close() error {
	if l.broken {
		l.file.Close()
		return l.failure
	}
	if omitted := l.dropped - int64(len(l.tail)); omitted > 0 {
		if _, err := fmt.Fprintf(l.file, "\n[... %d bytes of the middle omitted ...]\n", omitted); err != nil {
			l.file.Close()
			return err
		}
		if _, err := l.file.Write(l.tail); err != nil {
			l.file.Close()
			return err
		}
	}
	return l.file.Close()
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

// Slots belong to checks, across repositories and runner processes. A waiter
// tries every slot before blocking on one slot without polling.
func nodeSlotScript(base string, slots, index int, inner string) string {
	return "set -euo pipefail; mkdir -p " + shellquote.Quote(path.Join(base, "slots")) + "; acquired=0; " +
		fmt.Sprintf("for slot in $(seq 0 %d); do exec 9>\"%s/slots/$slot.lock\"; if flock -n 9; then acquired=1; break; fi; done; ", slots-1, base) +
		fmt.Sprintf("if [ \"$acquired\" = 0 ]; then exec 9>%s; flock -w %d 9 || exit 75; fi; ", shellquote.Quote(path.Join(base, "slots", fmt.Sprintf("%d.lock", index%slots))), int(admissionWait.Seconds())) + inner
}

func nodeCheckScript(node Node, base, cache, runDir, runID, sha string, check nodeCheck, timeout time.Duration, index int) string {
	work := path.Join(runDir, string(check.id), "work")
	reportDir := path.Join(runDir, "reports", string(check.id))
	name := "dyna-ci-" + runID + "-" + string(check.id)
	q := shellquote.Quote
	seconds := int64((timeout + time.Second - 1) / time.Second)
	docker := "docker=(docker); "
	if node.SSH == "" {
		docker += "if ! docker info >/dev/null 2>&1; then docker=(sudo -n docker); if ! \"${docker[@]}\" info >/dev/null 2>&1; then echo 'Docker access is unavailable' >&2; exit 69; fi; fi; "
	}
	timing := `printf '{"durationNanos":%s}\n' "$(( $(date +%s%N) - started ))" > ` + q(path.Join(reportDir, "execution.json")) + "; "
	cleanup := "check_status=$?; " + timing + `if [ "$check_entered" = 1 ]; then printf '\nDYNA_CI_CHECK_EXIT=%s\n' "$check_status"; fi; "${docker[@]}" rm -f ` + q(name) + " >/dev/null 2>&1 || true; git --git-dir=" + q(cache) + " worktree remove --force " + q(work) + " >/dev/null 2>&1 || true"
	setup := "mkdir -p " + q(reportDir) + "; started=$(date +%s%N); check_entered=0; " + docker + "trap " + q(cleanup) + " EXIT; trap 'exit 143' HUP INT TERM; mkdir -p " + q(path.Dir(work)) + "; git --git-dir=" + q(cache) + " worktree add --detach " + q(work) + " " + q(sha) + " || exit 69; "
	if check.id == SuiteComputerSmoke {
		setup += "for tool in go labwc Xwayland sqlite3 agent-browser; do command -v \"$tool\" >/dev/null || { echo \"Missing computer-smoke prerequisite: $tool\" >&2; exit 69; }; done; "
		setup += "cd " + q(work) + "; mkdir -p " + q(path.Join(base, "go-build")) + " " + q(path.Join(base, "go-mod")) + "; isolated=$(mktemp -d); trap " + q(cleanup+`; rm -rf "$isolated"`) + " EXIT; "
		setup += fmt.Sprintf("check_entered=1; timeout --signal=TERM --kill-after=10s %ds env -i PATH=\"$PATH\" HOME=\"$isolated/home\" XDG_DATA_HOME=\"$isolated/data\" XDG_CONFIG_HOME=\"$isolated/config\" XDG_CACHE_HOME=\"$isolated/cache\" XDG_RUNTIME_DIR=\"${XDG_RUNTIME_DIR:-/run/user/$(id -u)}\" DBUS_SESSION_BUS_ADDRESS=\"${DBUS_SESSION_BUS_ADDRESS:-unix:path=/run/user/$(id -u)/bus}\" GOCACHE=%s GOMODCACHE=%s GOMAXPROCS=2 GOFLAGS=-p=2 DYNA_COMPUTERS_ENABLED=0 DYNA_COMPUTERS_REQUIRE_LIVE=1 go test -count=1 -timeout 5m ./internal/computers", seconds, q(path.Join(base, "go-build")), q(path.Join(base, "go-mod")))
		return nodeSlotScript(base, node.Slots, index, setup)
	}
	for _, cacheDir := range []string{"go-build", "go-mod", "npm"} {
		setup += "mkdir -p " + q(path.Join(base, cacheDir)) + "; "
	}
	inner := "mkdir -p /tmp/home /tmp/data /tmp/config /tmp/cache; " + check.inner
	user := " --user $(id -u):$(id -g)"
	if strings.HasPrefix(check.image, "golang:") {
		user = ""
		prepare := ""
		if check.id == SuiteGoStatic {
			prepare = "timeout 120s apt-get -o Acquire::Retries=2 -o Acquire::http::Timeout=15 update -qq && timeout 120s apt-get -o Acquire::Retries=2 -o Acquire::http::Timeout=15 install -y -qq shellcheck >/dev/null || exit 69; "
		}
		inner = prepare + "(getent group $CI_GID >/dev/null || groupadd --gid $CI_GID dyna-ci) && (getent passwd $CI_UID >/dev/null || useradd --no-log-init --uid $CI_UID --gid $CI_GID --no-create-home --home-dir /tmp/home dyna-ci) && exec setpriv --reuid=$CI_UID --regid=$CI_GID --clear-groups bash -c " + q(inner)
	}
	// A stable source path lets Go reuse build and test results across run IDs.
	// The host worktree and reports stay separate for each check.
	// Temporary SQLite fixtures need no disk flushes and share the memory cap.
	args := `"${docker[@]}" run --rm --init --cpus=2 --memory=4g --memory-swap=4g --pids-limit=512 --tmpfs /tmp:rw,exec,nosuid,nodev,size=1g --name ` + q(name) + user +
		" -v " + q(work+":/workspace") + " -v " + q(cache+":"+cache) + " -v " + q(reportDir+":/reports") + " -w /workspace"
	for _, mount := range [][2]string{{"go-build", "build"}, {"go-mod", "mod"}, {"npm", "npm"}} {
		args += " -v " + q(path.Join(base, mount[0])+":/cache/"+mount[1])
	}
	args += " -e HOME=/tmp/home -e XDG_DATA_HOME=/tmp/data -e XDG_CONFIG_HOME=/tmp/config -e XDG_CACHE_HOME=/tmp/cache -e RUNNER_TEMP=/reports -e GITHUB_STEP_SUMMARY=/reports/step-summary.md -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod -e npm_config_cache=/cache/npm -e GOMAXPROCS=2 -e GOMEMLIMIT=3GiB -e GOFLAGS=-p=2 -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/workspace -e GITHUB_WORKSPACE=/workspace -e CI_UID=$(id -u) -e CI_GID=$(id -g) -e JUNIT_REPORT_PATH=/reports/" + string(check.id) + "-test.xml " + check.image
	// Both host and container deadlines survive a disconnected SSH client.
	setup += fmt.Sprintf("check_entered=1; timeout --signal=TERM --kill-after=10s %ds ", seconds+15) + args + fmt.Sprintf(" timeout --signal=TERM --kill-after=10s %ds bash -c ", seconds) + q(inner)
	return nodeSlotScript(base, node.Slots, index, setup)
}
