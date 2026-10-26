package localci

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Every failed check keeps its own output, a typed cause, a local log file and a
// saved result, even when the report copy fails afterwards. The cases cover the
// causes a node proves from its exit status and from the marker it writes.
func TestNodeKeepsFailedCheckEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		node   Node
		output []string
		code   int
		cause  FailureCause
		status Outcome
	}{
		{"early container failure", Node{Name: "local", Slots: 1, Enabled: true}, []string{"Docker access is unavailable"}, 69, CauseContainerFailure, OutcomeError},
		{"killed container", Node{Name: "local", Slots: 1, Enabled: true}, []string{"Killed"}, 137, CauseContainerFailure, OutcomeError},
		{"ssh drop", Node{Name: "remote", SSH: "pip@host", Slots: 1, Enabled: true}, []string{"Connection to port 22 timed out"}, 255, CauseSSHDrop, OutcomeError},
		{"timeout", Node{Name: "local", Slots: 1, Enabled: true}, []string{"check reached its deadline", "DYNA_CI_CHECK_EXIT=124"}, 124, CauseTimeout, OutcomeError},
		{"test failure", Node{Name: "local", Slots: 1, Enabled: true}, []string{"--- FAIL: TestBroken", "DYNA_CI_CHECK_EXIT=1"}, 1, CauseTestFailure, OutcomeFailed},
		{"reports unavailable", Node{Name: "local", Slots: 1, Enabled: true}, []string{"ok ./...", "DYNA_CI_CHECK_EXIT=0"}, 0, CauseReports, OutcomeError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := Runner{DataDir: t.TempDir(), Node: &tc.node}
			runner.Command = func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
				if fakeSystemdVersion(out, name, args) {
					return nil
				}
				if fakeNodeGit(dir, out, name, args) {
					return nil
				}
				script := args[len(args)-1]
				switch {
				case strings.Contains(script, "df -Pk"):
					fmt.Fprintln(out, "Filesystem 1024-blocks Used Available Capacity Mounted on")
					fmt.Fprintln(out, "/dev/x 1048576 1 104857600 1% /var/tmp")
					return nil
				case name == "systemd-run" || strings.Contains(script, "systemd-run"):
					for _, line := range tc.output {
						fmt.Fprintln(out, line)
					}
					if tc.code == 0 {
						return nil
					}
					return exitError(t, tc.code)
				case strings.Contains(script, "tar -C"):
					// The copy must find the check's output already on disk.
					logs, _ := filepath.Glob(filepath.Join(runner.DataDir, "local-ci", "*", "*", "run-*", "checks", "go-static.log"))
					if len(logs) != 1 {
						t.Errorf("check log missing before the report copy: %v", logs)
					} else if data, readErr := os.ReadFile(logs[0]); readErr != nil || !strings.Contains(string(data), tc.output[0]) {
						t.Errorf("check log held no output before the report copy: %v", readErr)
					}
					return errors.New("transport lost")
				}
				return nil
			}
			result, err := runner.Run(t.Context(), Request{Definition: testDynaDefinition(t), Repo: t.TempDir(), Suites: []Suite{SuiteGoStatic}, MaxTest: time.Second})
			if err == nil || !strings.Contains(err.Error(), "copy reports") {
				t.Fatalf("report copy failure was not reported: %v", err)
			}
			check := result.Checks[0]
			if check.Status != tc.status || check.Cause != tc.cause {
				t.Fatalf("check = %+v, want %s with cause %s", check, tc.status, tc.cause)
			}
			if !strings.Contains(check.LogTail, tc.output[0]) {
				t.Fatalf("check result lost its output: %+v", check)
			}
			blob := ""
			for _, report := range check.Reports {
				if filepath.Ext(report) == ".log" {
					blob = report
				}
			}
			if blob == "" {
				t.Fatalf("check kept no local log file: %+v", check.Reports)
			}
			data, err := os.ReadFile(blob)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range tc.output {
				if !strings.Contains(string(data), line) {
					t.Fatalf("local log lost %q: %q", line, data)
				}
			}
			// The run folder alone says why the check stopped: the log ends with
			// its typed cause, not only the output.
			exit := loggedExit(t, check)
			if exit.Cause != tc.cause || exit.Status != tc.status || exit.ExitCode != tc.code {
				t.Fatalf("local log exit record = %+v, want %s/%s/%d", exit, tc.cause, tc.status, tc.code)
			}
			saved, err := (Store{DataDir: runner.DataDir}).LoadResult(t.Context(), result.Repo, result.SHA)
			if err != nil {
				t.Fatalf("failed run was not saved: %v", err)
			}
			if len(saved.Checks) == 0 || saved.Checks[0].Status != tc.status || saved.Checks[0].Cause != tc.cause {
				t.Fatalf("saved result lost the check cause: %+v", saved)
			}
			if !strings.Contains(saved.Checks[0].LogTail, tc.output[0]) {
				t.Fatalf("saved result lost the output: %+v", saved.Checks[0])
			}
		})
	}
}

// loggedExit reads the typed exit record a check log ends with.
func loggedExit(t *testing.T, check SuiteResult) checkExit {
	t.Helper()
	var exit checkExit
	for _, report := range check.Reports {
		if filepath.Ext(report) != ".log" {
			continue
		}
		data, err := os.ReadFile(report)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			record, ok := strings.CutPrefix(line, "DYNA_CI_CHECK_CAUSE ")
			if !ok {
				continue
			}
			if err := json.Unmarshal([]byte(record), &exit); err != nil {
				t.Fatalf("invalid exit record %q: %v", record, err)
			}
		}
	}
	return exit
}

// exitError is the error a command returns when it exits with code.
func exitError(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("bash", "-c", "exit "+strconv.Itoa(code)).Run()
	if err == nil {
		t.Fatalf("exit %d did not fail", code)
	}
	return err
}

// fakeSystemdVersion answers the version the runner reads before it bounds a
// check, so a fake command answers for the check itself. The version matches
// the systemd that expands a command's arguments and knows the argument that
// stops it.
func fakeSystemdVersion(out io.Writer, name string, args []string) bool {
	joined := strings.Join(append([]string{name}, args...), " ")
	if !strings.Contains(joined, "systemd-run") || !strings.Contains(joined, "--version") {
		return false
	}
	fmt.Fprintln(out, "systemd 259 (259.5-0ubuntu3.4)")
	return true
}

// --expand-environment=no arrived in systemd 254. An older systemd rejects it
// and expands nothing of its own, so the scope runs without it; from 254 Dyna
// keeps it, because a systemd that expands $ would rewrite the check script.
// The script reaches bash as written either way.
func TestNodeScopeSuitsTheSystemdThatRunsIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		version  string
		noExpand bool
	}{
		{"debian 12", "systemd 252 (252.39-1~deb12u1)", false},
		{"systemd 253", "systemd 253.5-1", false},
		{"systemd 254", "systemd 254.5-1", true},
		{"arch", "systemd 262 (262-1-arch)", true},
		{"unreadable", "systemd-run: no version here\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			runner := Runner{Node: &Node{Name: "local", Slots: 1, Enabled: true}}
			runner.Command = func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
				calls = append(calls, append([]string{name}, args...))
				if name == "systemd-run" && len(args) == 1 && args[0] == "--version" {
					_, err := io.WriteString(out, tc.version+"\n")
					return err
				}
				return nil
			}
			if err := runner.nodeCommand(t.Context(), io.Discard, io.Discard, "dyna-ci-run", "echo $isolated"); err != nil {
				t.Fatal(err)
			}
			scope := calls[len(calls)-1]
			if got := slices.Contains(scope, "--expand-environment=no"); got != tc.noExpand {
				t.Fatalf("scope args = %v, want the no-expand argument %t", scope, tc.noExpand)
			}
			if last := scope[len(scope)-1]; last != "echo $isolated" {
				t.Fatalf("script argument = %q, want it unchanged", last)
			}
		})
	}
}

func fakeNodeGit(dir string, out io.Writer, name string, args []string) bool {
	if name != "git" {
		return false
	}
	if args[0] == "rev-parse" {
		if args[len(args)-1] == "--git-common-dir" {
			fmt.Fprintln(out, dir)
		} else {
			fmt.Fprintln(out, strings.Repeat("a", 40))
		}
	}
	return true
}

func fakeNodeReports(out io.Writer, checks []nodeCheck, failFrontend bool) error {
	tw := tar.NewWriter(out)
	for _, check := range checks {
		files := map[string]string{"execution.json": `{"durationNanos":1000000000}`}
		if check.reports == ReportJUnit || check.reports == ReportGo {
			xml := `<testsuite><testcase name="fast" time="0.1"/></testsuite>`
			if check.id == Suite("frontend-0") && failFrontend {
				xml = `<testsuite><testcase name="broken"><failure message="bad"/></testcase></testsuite>`
			}
			files["test.xml"] = xml
		}
		for name, body := range files {
			if err := tw.WriteHeader(&tar.Header{Name: string(check.id) + "/" + name, Mode: 0600, Size: int64(len(body))}); err != nil {
				return err
			}
			if _, err := io.WriteString(tw, body); err != nil {
				return err
			}
		}
	}
	return tw.Close()
}

// A second run of the same commit must reach the node. An interrupted first run
// can leave refs/dyna/ci/<sha> pointing at a missing object, and git refuses to
// push over it, so the runner drops the ref before it pushes.
func TestRunPushesACommitWhoseRefAPreviousRunLeft(t *testing.T) {
	repo := t.TempDir()
	gitRun := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitRun(repo, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(repo, "file"), []byte("content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(repo, "add", "file")
	gitRun(repo, "-c", "user.name=Dyna Test", "-c", "user.email=dyna@example.invalid", "commit", "--quiet", "-m", "one")
	sha := gitRun(repo, "rev-parse", "HEAD")
	cache := filepath.Join(t.TempDir(), "cache.git")
	if out, err := exec.Command("git", "init", "--quiet", "--bare", cache).CombinedOutput(); err != nil {
		t.Fatalf("init bare cache: %v: %s", err, out)
	}
	// The ref an interrupted run leaves behind names an object the node does not
	// have, which is what a plain and a forced push both refuse.
	ref := filepath.Join(cache, "refs", "dyna", "ci", sha)
	if err := os.MkdirAll(filepath.Dir(ref), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ref, []byte(strings.Repeat("1", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Node: &Node{Name: "local", Slots: 1, Enabled: true}}
	if err := runner.pushCommitToNode(t.Context(), repo, cache, sha); err != nil {
		t.Fatalf("a second run of the commit could not push: %v", err)
	}
	if got := gitRun(cache, "rev-parse", "refs/dyna/ci/"+sha); got != sha {
		t.Fatalf("node ref = %s, want %s", got, sha)
	}
	// A third run, now over the run's own ref, is a no-op rather than an error.
	if err := runner.pushCommitToNode(t.Context(), repo, cache, sha); err != nil {
		t.Fatalf("a repeated run of the same commit failed: %v", err)
	}
}

func TestNodeRunsBoundedWorkflowChecksAndAggregatesFrontendFailure(t *testing.T) {
	const slots = 4
	checks, _, err := nodeChecks(testDynaDefinition(t), []Suite{SuiteGo, SuiteCross}, true)
	if err != nil {
		t.Fatal(err)
	}
	var active, peak atomic.Int32
	var started, crossStarted atomic.Bool
	slotsStarted := make(chan struct{})
	release := make(chan struct{})
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "local", Slots: 8, Enabled: true}}
	runner.Command = func(ctx context.Context, dir string, _ io.Reader, out, stderr io.Writer, name string, args ...string) error {
		if fakeSystemdVersion(out, name, args) {
			return nil
		}
		if fakeNodeGit(dir, out, name, args) {
			return nil
		}
		script := args[len(args)-1]
		if strings.Contains(script, "tar -C") {
			return fakeNodeReports(out, checks, true)
		}
		// Every node script must parse: a setup that does not stops each run.
		if output, err := exec.Command("bash", "-n", "-c", script).CombinedOutput(); name == "bash" && err != nil {
			return fmt.Errorf("invalid script: %w: %s", err, output)
		}
		if name != "systemd-run" {
			return nil
		}
		if !started.Load() {
			t.Error("check ran before OnStart")
		}
		for _, want := range []string{"CPUQuota=200%", "MemoryMax=5G", "nice -n 10", "--unit", "--expand-environment=no"} {
			if !strings.Contains(strings.Join(args, " "), want) {
				t.Errorf("scope lacks %s", want)
			}
		}
		for _, want := range []string{"flock -n 9", "--cpus=2", "--memory=4g", "--tmpfs /tmp:rw,exec,nosuid,nodev,size=1g", "sudo -n docker", "GITHUB_STEP_SUMMARY=/reports/step-summary.md", ":/workspace", "-w /workspace", "GIT_CONFIG_VALUE_0=/workspace"} {
			if !strings.Contains(script, want) {
				t.Errorf("check lacks %s", want)
			}
		}
		for _, check := range checks {
			if !strings.Contains(script, "/"+string(check.id)+"/work") {
				continue
			}
			for _, cache := range check.caches {
				for _, want := range []string{"/" + cache.Name + ":" + cache.Target, cache.Env + "=" + cache.Target} {
					if !strings.Contains(script, want) {
						t.Errorf("check %s lacks cache setting %s", check.id, want)
					}
				}
			}
		}
		if output, err := exec.Command("bash", "-n", "-c", script).CombinedOutput(); err != nil {
			return fmt.Errorf("invalid script: %w: %s", err, output)
		}
		if strings.Contains(script, "bash scripts/ci.sh cross-build") {
			crossStarted.Store(true)
		}
		current := active.Add(1)
		defer active.Add(-1)
		// A plain Load then Store loses the highest value when checks overlap,
		// so raise the peak with a CAS loop.
		for {
			high := peak.Load()
			if current <= high || peak.CompareAndSwap(high, current) {
				break
			}
		}
		if current == slots {
			if !crossStarted.Load() {
				t.Error("cross-build did not start beside the other checks")
			}
			select {
			case <-slotsStarted:
			default:
				close(slotsStarted)
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		fmt.Fprintln(out, "DYNA_CI_CHECK_EXIT=0")
		return nil
	}
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := runner.Run(t.Context(), Request{Definition: testDynaDefinition(t), RepoDir: t.TempDir(), Suites: []Suite{"go", "cross-build"}, Parallelism: slots, RoomID: "room", DeferSave: true, OnStart: func(context.Context) error { started.Store(true); return nil }})
		done <- outcome{result, err}
	}()
	select {
	case <-slotsStarted:
	case got := <-done:
		t.Fatalf("run ended before its checks started: result=%+v err=%v", got.result, got.err)
	}
	close(release)
	got := <-done
	if got.err != nil || got.result.Status != "failed" || peak.Load() != slots {
		t.Fatalf("result=%+v err=%v peak=%d", got.result, got.err, peak.Load())
	}
	wanted := map[CheckName]bool{"frontend test (0)": true, "frontend test (1)": true, "frontend test (2)": true, "frontend test (3)": true, "frontend test (4)": true, "frontend test (5)": true, "frontend": true, "frontend-check": true, "go-static": true, "go-test": true,
		"cross-build (linux/amd64)": true, "cross-build (linux/arm64)": true, "cross-build (darwin/amd64)": true, "cross-build (darwin/arm64)": true, "cross-build": true}
	for i := 0; i < 8; i++ {
		wanted[CheckName(fmt.Sprintf("go-test-shard (%d)", i))] = true
	}
	for _, check := range got.result.Checks {
		if !wanted[check.CheckName] || check.Node != "local" {
			t.Fatalf("unexpected check: %+v", check)
		}
		delete(wanted, check.CheckName)
		if check.CheckName == "go-test" && check.Status != "failed" {
			t.Fatal("aggregate lost frontend failure")
		}
		if check.CheckName != "go-test" && !check.Aggregate && check.Duration != time.Second {
			t.Fatalf("check uses wall time instead of admitted execution time: %+v", check)
		}
		// This check's command passed; the reports proved the failure, and the
		// log must end with that cause too.
		if check.CheckName == "frontend test (0)" {
			if cause := loggedExit(t, check).Cause; cause != CauseTestFailure {
				t.Fatalf("frontend log lost the cause the reports proved: %q", cause)
			}
		}
	}
	if len(wanted) != 0 {
		t.Fatalf("missing checks: %v", wanted)
	}
}

func TestNodePreservesTestFailureWhenReportsFail(t *testing.T) {
	for _, entered := range []bool{false, true} {
		t.Run(fmt.Sprint(entered), func(t *testing.T) {
			var sawSSH, sawPush, sawCleanup, sawRemove, reportsCopied bool
			runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "remote", SSH: "pip@host", Slots: 1, Enabled: true}}
			runner.Command = func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
				if fakeSystemdVersion(out, name, args) {
					return nil
				}
				if fakeNodeGit(dir, out, name, args) {
					if strings.Contains(strings.Join(args, " "), "push") {
						sawPush = strings.Contains(strings.Join(args, " "), "push pip@host:")
					}
					return nil
				}
				sawSSH = sawSSH || strings.Contains(strings.Join(args, " "), "-o BatchMode=yes pip@host")
				script := args[len(args)-1]
				if strings.Contains(script, "systemd-run") {
					if entered {
						fmt.Fprintln(out, "DYNA_CI_CHECK_EXIT=1")
					}
					return errors.New("command exited 1")
				}
				if strings.Contains(script, "tar -C") {
					reportsCopied = true
					return errors.New("transport lost")
				}
				if strings.Contains(script, "rm -rf --one-file-system") && strings.Contains(script, "kill -TERM -- -") {
					sawRemove = true
					if !reportsCopied || !sawCleanup || !strings.Contains(script, "/runs/") {
						t.Errorf("run directory removed before the reports or the cleanup, or by the wrong path: %s", script)
					}
					// The keeper is stopped before its process ID can go away,
					// and the run's own lock acknowledges that it is gone.
					if !strings.Contains(script, "kill -TERM -- -") || !strings.Contains(script, "flock -n ") {
						t.Errorf("run directory removed without stopping its keeper: %s", script)
					}
				}
				if strings.Contains(script, "systemctl") {
					sawCleanup = true
					if strings.Index(script, "inspect") > strings.Index(script, "systemctl") {
						t.Error("scope releases slot before container is confirmed stopped")
					}
					// The keeper's process ID lives in the run directory, so this
					// cleanup must not take the directory away.
					if strings.Contains(script, "rm -rf") {
						t.Errorf("container cleanup removed the run directory: %s", script)
					}
				}
				return nil
			}
			result, err := runner.Run(t.Context(), Request{Definition: testDynaDefinition(t), Repo: t.TempDir(), Suites: []Suite{"go-static"}, DeferSave: true})
			want := OutcomeError
			if entered {
				want = OutcomeFailed
			}
			if err == nil || len(result.Checks) != 1 || result.Checks[0].Status != want || !sawSSH || !sawPush || !sawCleanup || !sawRemove {
				t.Fatalf("result=%+v err=%v ssh=%v push=%v cleanup=%v remove=%v", result, err, sawSSH, sawPush, sawCleanup, sawRemove)
			}
		})
	}
}

func TestNodeCancellationDoesNotSaveResult(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "local", Slots: 1, Enabled: true}}
	runner.Command = func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		fakeNodeGit(dir, out, name, args)
		return nil
	}
	result, err := runner.Run(ctx, Request{Definition: testDynaDefinition(t), Repo: t.TempDir(), Suites: []Suite{"cross-build"}, OnStart: func(context.Context) error { cancel(); return ctx.Err() }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if _, err := (Store{DataDir: runner.DataDir}).LoadResult(t.Context(), result.Repo, result.SHA); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled run persisted: %v", err)
	}
}

// A flush keeps the end of a check's log whatever its size, before the file is
// closed: the run copies reports only after that flush. Output just past the
// head limit must not lose its ending, and a shortened log says how much it cut.
func TestCheckLogKeepsTheEndOfTheOutput(t *testing.T) {
	const end = "THE END OF THE CHECK LOG"
	for _, tc := range []struct {
		name      string
		size      int
		truncated bool
	}{
		{"just past the head", checkLogHead + 1, false},
		{"shortened", checkLogHead + checkLogTail, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log, err := newCheckLog(t.TempDir(), nodeCheck{id: SuiteGoStatic})
			if err != nil {
				t.Fatal(err)
			}
			body := append(bytes.Repeat([]byte("x"), tc.size), end...)
			if _, err := log.Write(body); err != nil {
				t.Fatal(err)
			}
			if err := log.flush(); err != nil {
				t.Fatal(err)
			}
			// Read while the log is still open: the flush alone must have written
			// the end, because the report copy runs before the file is closed.
			data, err := os.ReadFile(log.path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(string(data), end) {
				t.Fatalf("a %d byte log lost its end: %q", len(body), data[len(data)-64:])
			}
			if omitted := strings.Contains(string(data), "bytes of the middle omitted"); omitted != tc.truncated {
				t.Fatalf("omission marker = %v for a %d byte log", omitted, len(body))
			}
			if tc.truncated && len(data) > checkLogHead+checkLogTail+len(end)+64 {
				t.Fatalf("shortened log kept %d bytes", len(data))
			}
			if err := log.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A run that reaches its own deadline saves its evidence, so the owner can see
// the cause for the commit. A deliberate cancel still belongs to its caller.
func TestNodeSavesResultWhenTheRunReachesItsDeadline(t *testing.T) {
	// The deadline has already passed, so the run ends at once and no clock runs.
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "local", Slots: 1, Enabled: true}}
	runner.Command = func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		if fakeSystemdVersion(out, name, args) {
			return nil
		}
		if fakeNodeGit(dir, out, name, args) {
			return nil
		}
		if strings.Contains(args[len(args)-1], "df -Pk") {
			fmt.Fprintln(out, "Filesystem 1024-blocks Used Available Capacity Mounted on")
			fmt.Fprintln(out, "/dev/x 1048576 1 104857600 1% /var/tmp")
		}
		return nil
	}
	result, err := runner.Run(ctx, Request{Definition: testDynaDefinition(t), Repo: t.TempDir(), Suites: []Suite{SuiteGoStatic}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	saved, err := (Store{DataDir: runner.DataDir}).LoadResult(t.Context(), result.Repo, result.SHA)
	if err != nil {
		t.Fatalf("timed out run was not saved: %v", err)
	}
	if saved.Cause != CauseTimeout || len(saved.Checks) == 0 || saved.Checks[0].Cause != CauseTimeout {
		t.Fatalf("saved result lost the timeout cause: %+v", saved)
	}
}

func TestNodeScriptWritesValidExecutionTiming(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"docker", "git"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	checks, _, err := nodeChecks(testDynaDefinition(t), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range checks {
		script := nodeCheckScript(Node{Name: "local", Slots: 1}, dir, filepath.Join(dir, "cache.git"), dir, "run", strings.Repeat("a", 40), check, time.Minute, 0)
		if output, err := exec.Command("bash", "-n", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("%s syntax: %v: %s", check.name, err, output)
		}
	}
	check := nodeCheck{id: SuiteFrontend, name: "frontend", inner: "true", image: "node:22"}
	script := nodeCheckScript(Node{Name: "local", Slots: 1}, dir, filepath.Join(dir, "cache.git"), dir, "run", strings.Repeat("a", 40), check, time.Minute, 0)
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "DYNA_CI_CHECK_EXIT=0") {
		t.Fatalf("script: %v: %s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(dir, "reports", "frontend", "execution.json"))
	if err != nil || !json.Valid(data) {
		t.Fatalf("invalid timing: %q %v", data, err)
	}
	for _, name := range []string{"docker", "sudo"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cmd = exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	output, err = cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 69 || strings.Contains(string(output), "DYNA_CI_CHECK_EXIT") {
		t.Fatalf("denied Docker access became a test failure: %v: %s", err, output)
	}
}

// TestNodeRetriesOnceAfterAFullDisk covers a full disk on the CI machine: the
// check is an infrastructure failure rather than a test failure, the run cleans
// Dyna's own caches and retries once, and a second full disk stops there.
func TestNodeRetriesOnceAfterAFullDisk(t *testing.T) {
	checks, _, err := nodeChecks(testDynaDefinition(t), []Suite{SuiteGoStatic}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, fullAttempts := range []int32{1, 2} {
		t.Run(fmt.Sprint(fullAttempts), func(t *testing.T) {
			var attempts, cleans atomic.Int32
			runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "local", Slots: 1, Enabled: true}}
			runner.Command = func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
				if fakeSystemdVersion(out, name, args) {
					return nil
				}
				if fakeNodeGit(dir, out, name, args) {
					return nil
				}
				script := args[len(args)-1]
				switch {
				case strings.Contains(script, "df -Pk"):
					fmt.Fprintln(out, "Filesystem 1024-blocks Used Available Capacity Mounted on")
					fmt.Fprintln(out, "/dev/x 1048576 1 104857600 1% /var/tmp")
				case strings.Contains(script, "dyna-ci-clean:"):
					cleans.Add(1)
					fmt.Fprintln(out, "DYNA_CI_CACHE_CLEAN_ITEM 2048 /var/tmp/dyna-local-ci-1000/local/runs/leftover")
					fmt.Fprintln(out, "DYNA_CI_CACHE_CLEAN_FREED_KB=2048")
				case strings.Contains(script, "tar -C"):
					return fakeNodeReports(out, checks, false)
				case strings.Contains(script, "init.lock"):
					attempts.Add(1)
				case name == "systemd-run" || strings.Contains(script, "systemd-run"):
					if attempts.Load() <= fullAttempts {
						fmt.Fprintln(out, "write /workspace/report.xml: No space left on device")
						fmt.Fprintln(out, "DYNA_CI_CHECK_EXIT=1")
						return errors.New("command exited 1")
					}
					fmt.Fprintln(out, "DYNA_CI_CHECK_EXIT=0")
				}
				return nil
			}
			result, err := runner.Run(t.Context(), Request{Definition: testDynaDefinition(t), Repo: t.TempDir(), Suites: []Suite{"go-static"}, DeferSave: true})
			if err != nil {
				t.Fatalf("run failed: %v", err)
			}
			if got := attempts.Load(); got != 2 {
				t.Fatalf("full disk retried %d runs, want one retry: %d", fullAttempts, got)
			}
			if got := cleans.Load(); got != 1 {
				t.Fatalf("cleanups = %d, want one before the retry", got)
			}
			want := OutcomePassed
			if fullAttempts > 1 {
				want = OutcomeError
			}
			if result.Status != want || len(result.Checks) != 1 || result.Checks[0].Status != want {
				t.Fatalf("result = %+v, want %s", result, want)
			}
			if fullAttempts > 1 && !strings.Contains(result.Checks[0].Error, "disk space") {
				t.Fatalf("a second full disk lost the reason: %+v", result.Checks[0])
			}
		})
	}
}

// A run whose internal budget ends after a check reported success is not a pass:
// the reports were never read, so the check is an error with the run's cause and
// its log stays available. The command cancels the run itself, so no clock runs.
func TestNodeRunFailsWhenItsBudgetEndsBeforeTheReports(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "local", Slots: 1, Enabled: true}}
	runner.Command = func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		if fakeSystemdVersion(out, name, args) {
			return nil
		}
		if fakeNodeGit(dir, out, name, args) {
			return nil
		}
		script := args[len(args)-1]
		switch {
		case strings.Contains(script, "df -Pk"):
			fmt.Fprintln(out, "Filesystem 1024-blocks Used Available Capacity Mounted on")
			fmt.Fprintln(out, "/dev/x 1048576 1 104857600 1% /var/tmp")
		case name == "systemd-run" || strings.Contains(script, "systemd-run"):
			fmt.Fprintln(out, "ok: the check reported success")
			fmt.Fprintln(out, "DYNA_CI_CHECK_EXIT=0")
			// The run's own budget ends before its reports can be read.
			cancel()
		case strings.Contains(script, "tar -C"):
			return errors.New("reports fetched after the run ended")
		}
		return nil
	}
	result, err := runner.Run(ctx, Request{Definition: testDynaDefinition(t), Repo: t.TempDir(), Suites: []Suite{SuiteGoStatic}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v after its budget ended", err)
	}
	if result.Cause != CauseCancelled || len(result.Checks) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Checks[0].Status != OutcomeError || result.Checks[0].Cause != CauseCancelled {
		t.Fatalf("unvalidated check = %+v", result.Checks[0])
	}
	blob := ""
	for _, report := range result.Checks[0].Reports {
		if filepath.Ext(report) == ".log" {
			blob = report
		}
	}
	data, err := os.ReadFile(blob)
	if err != nil || !strings.Contains(string(data), "the check reported success") {
		t.Fatalf("check log lost after the run ended: %v: %q", err, data)
	}
}

// writeCheckReports writes the report files one check produced on its node.
func writeCheckReports(out io.Writer, files map[string]string) error {
	tw := tar.NewWriter(out)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body))}); err != nil {
			return err
		}
		if _, err := io.WriteString(tw, body); err != nil {
			return err
		}
	}
	return tw.Close()
}

// A check that fails its command still keeps everything its reports prove: the
// execution timing, the structured test failures, the slow tests and the report
// links. The command's own outcome stays the check's outcome and cause.
func TestNodeKeepsReportsOfACheckWhoseCommandFailed(t *testing.T) {
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "local", Slots: 1, Enabled: true}}
	runner.Command = func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		if fakeSystemdVersion(out, name, args) {
			return nil
		}
		if fakeNodeGit(dir, out, name, args) {
			return nil
		}
		script := args[len(args)-1]
		switch {
		case strings.Contains(script, "df -Pk"):
			fmt.Fprintln(out, "Filesystem 1024-blocks Used Available Capacity Mounted on")
			fmt.Fprintln(out, "/dev/x 1048576 1 104857600 1% /var/tmp")
		case name == "systemd-run" || strings.Contains(script, "systemd-run"):
			fmt.Fprintln(out, "--- FAIL: TestBroken")
			fmt.Fprintln(out, "DYNA_CI_CHECK_EXIT=1")
			return exitError(t, 1)
		case strings.Contains(script, "tar -C"):
			return writeCheckReports(out, map[string]string{
				"relay/execution.json":       `{"durationNanos":250000000}`,
				"relay/relay-test.xml":       `<testsuite><testcase name="TestSlow" time="0.2"/><testcase name="TestBroken"><failure message="bad"/></testcase></testsuite>`,
				"relay/ci-diagnostics.jsonl": "{\"kind\":\"counter\",\"key\":\"go/test\",\"value\":\"exit=1\"}\n",
			})
		}
		return nil
	}
	result, err := runner.Run(t.Context(), Request{Definition: testDynaDefinition(t), Repo: t.TempDir(), Suites: []Suite{SuiteRelay}, MaxTest: time.Second, DeferSave: true})
	if err != nil {
		t.Fatal(err)
	}
	check := result.Checks[0]
	if check.Status != OutcomeFailed || check.Cause != CauseTestFailure {
		t.Fatalf("command outcome lost: %+v", check)
	}
	if check.Duration != 250*time.Millisecond {
		t.Fatalf("execution timing lost: %+v", check)
	}
	if failures := strings.Join(check.Failures, "\n"); !strings.Contains(failures, "TestBroken") {
		t.Fatalf("structured failures lost: %+v", check.Failures)
	}
	if len(check.SlowTests) == 0 || check.SlowTests[0].Name != "TestSlow" {
		t.Fatalf("slow tests lost: %+v", check.SlowTests)
	}
	links := map[string]bool{}
	for _, report := range check.Reports {
		links[filepath.Base(report)] = true
	}
	if !links["relay-test.xml"] || !links["ci-diagnostics.jsonl"] {
		t.Fatalf("report links lost: %v", check.Reports)
	}
	if !strings.Contains(check.LogTail, "TestBroken") {
		t.Fatalf("structured output lost from the tail: %q", check.LogTail)
	}
}

// A command that exited 0 must be judged by its own completion marker even when
// os/exec reports ErrWaitDelay, which only means a child it started kept the
// output pipe open. An sccache server started by cargo does exactly that on
// every run, and the check passed: it must not be saved as an error.
func TestNodePassesACheckWhoseCommandLeftAChildHoldingOutput(t *testing.T) {
	definition := testDynaDefinition(t)
	checks, _, err := nodeChecks(definition, []Suite{SuiteGoStatic}, false)
	if err != nil {
		t.Fatal(err)
	}
	node := Node{Name: "local", Slots: 1, Enabled: true}
	runner := Runner{DataDir: t.TempDir(), Node: &node}
	runner.Command = func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		if fakeSystemdVersion(out, name, args) {
			return nil
		}
		if fakeNodeGit(dir, out, name, args) {
			return nil
		}
		script := args[len(args)-1]
		switch {
		case strings.Contains(script, "df -Pk"):
			fmt.Fprintln(out, "Filesystem 1024-blocks Used Available Capacity Mounted on")
			fmt.Fprintln(out, "/dev/x 1048576 1 104857600 1% /var/tmp")
			return nil
		case strings.Contains(script, "tar -C"):
			return fakeNodeReports(out, checks, false)
		case name == "systemd-run" || strings.Contains(script, "systemd-run"):
			fmt.Fprintln(out, "ok  dyna-agent/internal/voice")
			fmt.Fprintln(out, "DYNA_CI_CHECK_EXIT=0")
			fmt.Fprintln(out, "DYNA_CI_COMMAND_STARTED=1")
			return exec.ErrWaitDelay
		}
		return nil
	}
	result, err := runner.Run(t.Context(), Request{Definition: definition, Repo: t.TempDir(), Suites: []Suite{SuiteGoStatic}, MaxTest: time.Second})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != OutcomePassed || len(result.Checks) != 1 || result.Checks[0].Status != OutcomePassed {
		t.Fatalf("a check that exited 0 was judged by its leftover child: %+v", result)
	}
	if got := result.Checks[0].Warnings; len(got) != 1 || got[0] != CleanupLeftoverOutput {
		t.Fatalf("the leftover pipe holder was not recorded as a cleanup warning: %+v", got)
	}
}
