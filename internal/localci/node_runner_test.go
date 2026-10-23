package localci

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

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
		if check.id == "frontend" || strings.HasPrefix(check.id, "go-test-shard-") {
			xml := `<testsuite><testcase name="fast" time="0.1"/></testsuite>`
			if check.id == "frontend" && failFrontend {
				xml = `<testsuite><testcase name="broken"><failure message="bad"/></testcase></testsuite>`
			}
			files["test.xml"] = xml
		}
		for name, body := range files {
			if err := tw.WriteHeader(&tar.Header{Name: check.id + "/" + name, Mode: 0600, Size: int64(len(body))}); err != nil {
				return err
			}
			if _, err := io.WriteString(tw, body); err != nil {
				return err
			}
		}
	}
	return tw.Close()
}

func TestNodeRunsBoundedWorkflowChecksAndAggregatesFrontendFailure(t *testing.T) {
	const slots = 4
	checks, _, err := nodeChecks([]string{"go", "cross-build"})
	if err != nil {
		t.Fatal(err)
	}
	var active, peak atomic.Int32
	var started, crossStarted atomic.Bool
	slotsStarted := make(chan struct{})
	release := make(chan struct{})
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "local", Slots: 8, Enabled: true}}
	runner.Command = func(ctx context.Context, dir string, _ io.Reader, out, stderr io.Writer, name string, args ...string) error {
		if fakeNodeGit(dir, out, name, args) {
			return nil
		}
		script := args[len(args)-1]
		if strings.Contains(script, "tar -C") {
			return fakeNodeReports(out, checks, true)
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
		for _, want := range []string{"flock -n 9", "--cpus=2", "--memory=4g", "--tmpfs /tmp:rw,exec,nosuid,nodev,size=1g", "sudo -n docker", "GOCACHE=/cache/build", "GOMODCACHE=/cache/mod", "GITHUB_STEP_SUMMARY=/reports/step-summary.md", ":/workspace", "-w /workspace", "GIT_CONFIG_VALUE_0=/workspace", "npm_config_cache=/cache/npm"} {
			if !strings.Contains(script, want) {
				t.Errorf("check lacks %s", want)
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
		result, err := runner.Run(t.Context(), Request{RepoDir: t.TempDir(), Suites: []string{"go", "cross-build"}, Parallelism: slots, RoomID: "room", DeferSave: true, OnStart: func(context.Context) error { started.Store(true); return nil }})
		done <- outcome{result, err}
	}()
	<-slotsStarted
	close(release)
	got := <-done
	if got.err != nil || got.result.Status != "failed" || peak.Load() != slots {
		t.Fatalf("result=%+v err=%v peak=%d", got.result, got.err, peak.Load())
	}
	wanted := map[string]bool{"frontend": true, "frontend-check": true, "go-static": true, "go-test": true, "cross-build": true}
	for i := 0; i < 8; i++ {
		wanted[fmt.Sprintf("go-test-shard (%d)", i)] = true
	}
	for _, check := range got.result.Checks {
		if !wanted[check.CheckName] || check.Node != "local" {
			t.Fatalf("unexpected check: %+v", check)
		}
		delete(wanted, check.CheckName)
		if check.CheckName == "go-test" && check.Status != "failed" {
			t.Fatal("aggregate lost frontend failure")
		}
		if check.CheckName != "go-test" && check.Duration != time.Second {
			t.Fatalf("check uses wall time instead of admitted execution time: %+v", check)
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
				if strings.Contains(script, "rm -rf --one-file-system") {
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
			result, err := runner.Run(t.Context(), Request{Repo: t.TempDir(), Suites: []string{"go-static"}, DeferSave: true})
			want := "error"
			if entered {
				want = "failed"
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
	result, err := runner.Run(ctx, Request{Repo: t.TempDir(), Suites: []string{"cross-build"}, OnStart: func(context.Context) error { cancel(); return ctx.Err() }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if _, err := (Store{DataDir: runner.DataDir}).LoadResult(t.Context(), result.Repo, result.SHA); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled run persisted: %v", err)
	}
}

func TestNodeScriptWritesValidExecutionTiming(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"docker", "git"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	checks, _, err := nodeChecks(WorkflowSuites())
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range checks {
		script := nodeCheckScript(Node{Name: "local", Slots: 1}, dir, filepath.Join(dir, "cache.git"), dir, "run", strings.Repeat("a", 40), check, time.Minute, 0)
		if output, err := exec.Command("bash", "-n", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("%s syntax: %v: %s", check.name, err, output)
		}
	}
	check := nodeCheck{id: "frontend", name: "frontend", inner: "true", image: "node:22"}
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
	checks, _, err := nodeChecks([]string{"go-static"})
	if err != nil {
		t.Fatal(err)
	}
	for _, fullAttempts := range []int32{1, 2} {
		t.Run(fmt.Sprint(fullAttempts), func(t *testing.T) {
			var attempts, cleans atomic.Int32
			runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "local", Slots: 1, Enabled: true}}
			runner.Command = func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
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
			result, err := runner.Run(t.Context(), Request{Repo: t.TempDir(), Suites: []string{"go-static"}, DeferSave: true})
			if err != nil {
				t.Fatalf("run failed: %v", err)
			}
			if got := attempts.Load(); got != 2 {
				t.Fatalf("full disk retried %d runs, want one retry: %d", fullAttempts, got)
			}
			if got := cleans.Load(); got != 1 {
				t.Fatalf("cleanups = %d, want one before the retry", got)
			}
			want := "passed"
			if fullAttempts > 1 {
				want = "error"
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
