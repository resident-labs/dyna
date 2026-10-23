package localci

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestRunChecksReportsAndSavesResult(t *testing.T) {
	for _, tc := range []struct{ name, xml, want string }{
		{"pass", `<testsuite><testcase name="fast" time="0.1"/></testsuite>`, "passed"},
		{"slow", `<testsuite><testcase name="slow" time="11"/></testsuite>`, "failed"},
		{"failure", `<testsuite><testcase name="broken"><failure message="bad"/></testcase></testsuite>`, "failed"},
		{"missing", "", "error"},
		{"malformed", "<broken>", "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var calls []string
			runner := Runner{DataDir: t.TempDir(), Remote: "ci@example:/tmp/cache", Command: func(ctx context.Context, dir string, in io.Reader, out, stderr io.Writer, name string, args ...string) error {
				call := name + " " + strings.Join(args, " ")
				mu.Lock()
				calls = append(calls, call)
				mu.Unlock()
				if name == "git" && args[0] == "rev-parse" {
					if args[len(args)-1] == "--git-common-dir" {
						fmt.Fprintln(out, dir)
					} else {
						fmt.Fprintln(out, strings.Repeat("a", 40))
					}
				}
				if name == "ssh" && strings.Contains(call, "tar -C") {
					tw := tar.NewWriter(out)
					if tc.xml != "" {
						xml := `<testsuite><testcase name="frontend" time="0.1"/></testsuite>`
						tw.WriteHeader(&tar.Header{Name: "frontend/frontend-test.xml", Mode: 0600, Size: int64(len(xml))})
						io.WriteString(tw, xml)
					}
					if tc.xml != "" {
						tw.WriteHeader(&tar.Header{Name: "go/go-test.xml", Mode: 0600, Size: int64(len(tc.xml))})
						io.WriteString(tw, tc.xml)
					}
					const diagnostics = "{\"kind\":\"failure\",\"key\":\"go/test\",\"value\":\"exit=1\"}\n"
					tw.WriteHeader(&tar.Header{Name: "go/ci-diagnostics.jsonl", Mode: 0600, Size: int64(len(diagnostics))})
					io.WriteString(tw, diagnostics)
					return tw.Close()
				}
				// Every remote script must parse: a setup that does not stops each run.
				if name == "ssh" {
					if output, err := exec.Command("bash", "-n", "-c", args[len(args)-1]).CombinedOutput(); err != nil {
						return fmt.Errorf("invalid script: %w: %s", err, output)
					}
				}
				return nil
			}}
			result, err := runner.Run(context.Background(), Request{Repo: t.TempDir(), SHA: "HEAD", Suites: []string{"go"}, MaxTest: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.want {
				t.Fatalf("status=%s, want %s: %+v", result.Status, tc.want, result)
			}
			var diagnosticPath string
			for _, suite := range result.Suites {
				if suite.Suite == "go" {
					for _, report := range suite.Reports {
						if filepath.Base(report) == "ci-diagnostics.jsonl" {
							diagnosticPath = report
						}
					}
				}
			}
			if diagnosticPath == "" {
				t.Fatal("diagnostics were not retained with the suite result")
			}
			if _, err := os.Stat(diagnosticPath); err != nil {
				t.Fatal(err)
			}

			saved, err := runner.Show(result.Repo, result.SHA)
			if err != nil || saved.Status != tc.want {
				t.Fatalf("saved=%+v err=%v", saved, err)
			}
			all := strings.Join(calls, "\n")
			for _, want := range []string{"push", "refs/dyna/ci/", "worktree add --detach", "'/tmp/cache/npm'", "scripts/ci.sh", "worktree remove"} {
				if !strings.Contains(all, want) {
					t.Errorf("missing %q in %s", want, all)
				}
			}
		})
	}
}

func TestRunRejectsInvalidSuiteBeforeTransport(t *testing.T) {
	r := Runner{DataDir: t.TempDir(), Command: func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error {
		t.Fatal("transport called")
		return nil
	}}
	_, err := r.Run(context.Background(), Request{Repo: ".", SHA: "HEAD", Suites: []string{"computer-smoke"}})
	if err == nil {
		t.Fatal("accepted unsupported suite")
	}
}

func TestReadArchiveRejectsPathsAndLinks(t *testing.T) {
	for _, name := range []string{"../escape.xml", "/escape.xml"} {
		var b bytes.Buffer
		tw := tar.NewWriter(&b)
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: 1})
		tw.Write([]byte("x"))
		tw.Close()
		if err := readArchive(&b, t.TempDir()); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	tw.WriteHeader(&tar.Header{Name: "go/x.xml", Typeflag: tar.TypeSymlink, Linkname: "/tmp/x"})
	tw.Close()
	if err := readArchive(&b, t.TempDir()); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestRunCleansUpAfterTransportFailure(t *testing.T) {
	var calls []string
	r := Runner{DataDir: t.TempDir(), Remote: "ci@example:/tmp/cache", Command: func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		if name == "git" && args[0] == "rev-parse" {
			fmt.Fprintln(out, strings.Repeat("b", 40))
		}
		if strings.Contains(call, "docker run") || strings.Contains(call, "docker create") {
			return os.ErrPermission
		}
		return nil
	}}
	result, err := r.Run(context.Background(), Request{Repo: t.TempDir(), Suites: []string{"cross-build"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "error" {
		t.Fatalf("status=%s", result.Status)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "worktree remove") {
		t.Fatal("no cleanup")
	}
}

func TestContainerMountsWorktreeAndGitMetadataAtHostPaths(t *testing.T) {
	script := containerScript("/cache", "/cache/repo.git", "/cache/runs/one/work", "/cache/runs/one/reports", "run-one", "frontend", time.Minute)
	for _, mount := range []string{"'/cache/repo.git:/cache/repo.git'", "'/cache/runs/one/work:/cache/runs/one/work'", "'/cache/npm:/cache/npm'", "-e npm_config_cache=/cache/npm", "-w '/cache/runs/one/work'"} {
		if !strings.Contains(script, mount) {
			t.Errorf("missing mount %s in %s", mount, script)
		}
	}
}

func TestGoFailureDetailsSurviveJUnitExport(t *testing.T) {
	dir := t.TempDir()
	reports := filepath.Join(dir, "reports", "go")
	if err := os.MkdirAll(reports, 0700); err != nil {
		t.Fatal(err)
	}
	// The existing Go JUnit exporter writes timing only. Preserve execution details too.
	if err := os.WriteFile(filepath.Join(reports, "go-test.xml"), []byte(`<testsuite><testcase name="TestBroken" classname="pkg" time="0.1"/></testsuite>`), 0600); err != nil {
		t.Fatal(err)
	}
	raw := `{"Action":"output","Package":"pkg","Output":"--- FAIL: TestBroken\n"}
{"Action":"fail","Package":"pkg","Test":"TestBroken","Elapsed":0.1}
{"Action":"fail","Package":"pkg"}
`
	if err := os.WriteFile(filepath.Join(reports, "go-test.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	result := SuiteResult{Suite: "go", Status: "failed"}
	if err := checkReports(&result, dir, 10*time.Second, nil); err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) == 0 || !strings.Contains(result.Failures[0], "TestBroken") {
		t.Fatalf("lost failure details: %+v", result)
	}
}

func TestCrossBuildWaitsForSelectedFrontend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		frontendStarted := make(chan struct{})
		releaseFrontend := make(chan struct{})
		frontendFinished := make(chan struct{})
		var mu sync.Mutex
		var order []string
		runner := Runner{DataDir: t.TempDir(), Remote: "ci@example:/tmp/cache", Command: func(ctx context.Context, dir string, _ io.Reader, out, stderr io.Writer, name string, args ...string) error {
			call := strings.Join(args, " ")
			if name == "git" && args[0] == "rev-parse" {
				if args[len(args)-1] == "--git-common-dir" {
					fmt.Fprintln(out, dir)
				} else {
					fmt.Fprintln(out, strings.Repeat("c", 40))
				}
			}
			if name == "ssh" && (strings.Contains(call, "docker run") || strings.Contains(call, "docker create")) {
				if strings.Contains(call, "scripts/ci.sh frontend") {
					close(frontendStarted)
					select {
					case <-releaseFrontend:
					case <-ctx.Done():
						return ctx.Err()
					}
					mu.Lock()
					order = append(order, "frontend finished")
					mu.Unlock()
					close(frontendFinished)
				}
				if strings.Contains(call, "scripts/ci.sh cross-build") {
					select {
					case <-frontendFinished:
					default:
						return fmt.Errorf("cross-build started before frontend finished")
					}
					mu.Lock()
					order = append(order, "cross-build started")
					mu.Unlock()
				}
			}
			if name == "ssh" && strings.Contains(call, "tar -C") {
				tw := tar.NewWriter(out)
				xml := `<testsuite><testcase name="frontend" time="0.1"/></testsuite>`
				if err := tw.WriteHeader(&tar.Header{Name: "frontend/frontend-test.xml", Mode: 0600, Size: int64(len(xml))}); err != nil {
					return err
				}
				io.WriteString(tw, xml)
				return tw.Close()
			}
			return nil
		}}
		type outcome struct {
			result Result
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			result, err := runner.Run(ctx, Request{Repo: t.TempDir(), Suites: []string{"cross-build", "frontend"}, DeferSave: true})
			done <- outcome{result, err}
		}()
		select {
		case <-frontendStarted:
		case <-ctx.Done():
			t.Fatal("frontend did not start")
		}
		synctest.Wait()
		close(releaseFrontend)
		select {
		case got := <-done:
			if got.err != nil || got.result.Status != "passed" {
				t.Fatalf("result=%+v err=%v", got.result, got.err)
			}
		case <-ctx.Done():
			t.Fatal("runner did not finish")
		}
		if strings.Join(order, ",") != "frontend finished,cross-build started" {
			t.Fatalf("order=%v", order)
		}
	})
}

func TestCrossBuildAloneDoesNotRequireFrontend(t *testing.T) {
	runner := Runner{DataDir: t.TempDir(), Remote: "ci@example:/tmp/cache", Command: func(_ context.Context, dir string, _ io.Reader, out, stderr io.Writer, name string, args ...string) error {
		call := strings.Join(args, " ")
		if name == "git" && args[0] == "rev-parse" {
			if args[len(args)-1] == "--git-common-dir" {
				fmt.Fprintln(out, dir)
			} else {
				fmt.Fprintln(out, strings.Repeat("d", 40))
			}
		}
		if name == "ssh" && strings.Contains(call, "scripts/ci.sh frontend") {
			return fmt.Errorf("unexpected frontend")
		}
		if name == "ssh" && strings.Contains(call, "tar -C") {
			return tar.NewWriter(out).Close()
		}
		return nil
	}}
	result, err := runner.Run(context.Background(), Request{Repo: t.TempDir(), Suites: []string{"cross-build"}})
	if err != nil || result.Status != "passed" || len(result.Suites) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// Inspect the bytes sent over the transport, before readArchive can filter
// them. Build tools and logs share RUNNER_TEMP with the required reports.
func TestRunArchivesOnlyRequiredReportFiles(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	wanted := map[string]string{
		"relay/relay-test.xml":         `<testsuite><testcase name="fast" time="0.001"/></testsuite>`,
		"relay/details.json":           `{"result":"kept"}`,
		"relay/report with space.json": `{"space":true}`,
		"relay/report\nline.json":      `{"newline":true}`,
		"relay/ci-diagnostics.jsonl":   "{\"kind\":\"counter\"}\n",
	}
	unwanted := map[string]string{"relay/dyna-ci": "binary contents", "relay/suite.log": "unrelated output", "relay/unrelated.jsonl": "ignored log", "relay/nested/report.xml/tool": "directory contents", "relay/node_modules/tool/package.json": `{"name":"tool"}`, "orphan.json": `{"unconsumed":true}`}
	var archived map[string]string
	base := filepath.Join(t.TempDir(), "remote")
	runner := Runner{DataDir: t.TempDir(), Remote: "ci@example:" + base, Command: func(ctx context.Context, dir string, _ io.Reader, out, stderr io.Writer, name string, args ...string) error {
		if name == "git" && args[0] == "rev-parse" {
			if args[len(args)-1] == "--git-common-dir" {
				_, err := fmt.Fprintln(out, dir)
				return err
			}
			_, err := fmt.Fprintln(out, strings.Repeat("e", 40))
			return err
		}
		if name != "ssh" {
			return nil
		}
		script := args[len(args)-1]
		if !strings.Contains(script, "tar -C") {
			return nil
		}
		remoteReports := regexp.MustCompile(regexp.QuoteMeta(base) + `/runs/[^/]+/reports`).FindString(script)
		if remoteReports == "" {
			return fmt.Errorf("missing archive report root: %s", script)
		}
		for _, files := range []map[string]string{wanted, unwanted} {
			for name, content := range files {
				file := filepath.Join(remoteReports, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					return err
				}
				if err := os.WriteFile(file, []byte(content), 0600); err != nil {
					return err
				}
			}
		}
		var archive bytes.Buffer
		cmd := exec.CommandContext(ctx, "bash", "-c", script)
		cmd.Stdout, cmd.Stderr = io.MultiWriter(out, &archive), stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		archived = map[string]string{}
		tr := tar.NewReader(&archive)
		for {
			header, err := tr.Next()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if header.Typeflag == tar.TypeDir {
				continue
			}
			content, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			archived[strings.TrimPrefix(header.Name, "./")] = string(content)
		}
	}}
	result, err := runner.Run(ctx, Request{Repo: t.TempDir(), Suites: []string{"relay"}, Timeout: 30 * time.Second, MaxTest: time.Second})
	if err != nil || result.Status != "passed" {
		t.Fatalf("report retrieval: %+v %v", result, err)
	}
	if len(archived) != len(wanted) {
		t.Fatalf("transport included unrelated files: %v", archived)
	}
	for name, content := range wanted {
		if archived[name] != content {
			t.Errorf("archive lost %q: %q", name, archived[name])
		}
	}
	for name := range unwanted {
		if _, ok := archived[name]; ok {
			t.Errorf("archive included %q", name)
		}
	}
	for name, content := range wanted {
		files, err := filepath.Glob(filepath.Join(runner.DataDir, "local-ci", "*", result.SHA, "run-*", "reports", filepath.FromSlash(name)))
		if err != nil || len(files) != 1 {
			t.Fatalf("missing extracted report %q: %v %v", name, files, err)
		}
		data, err := os.ReadFile(files[0])
		if err != nil || string(data) != content {
			t.Errorf("extracted report %q: %q %v", name, data, err)
		}
	}
}

func TestContainerLimitsAndSharedAdmission(t *testing.T) {
	for _, suite := range DefaultSuites() {
		t.Run(suite, func(t *testing.T) {
			script := containerScript("/cache", "/cache/repo.git", "/cache/work", "/cache/reports", "bounded-job", suite, 30*time.Second)
			for _, want := range []string{
				"--init", "--cpus=2", "--memory=4g", "--memory-swap=4g", "--pids-limit=512",
				"timeout --signal=TERM --kill-after=10s 30s bash -c",
			} {
				if !strings.Contains(script, want) {
					t.Errorf("%s omits container/resource/deadline contract %q", suite, want)
				}
			}
			goSuite := suite == "go" || suite == "cross-build"
			if goSuite {
				for _, want := range []string{
					"bash '/var/tmp/dyna-ci/ci-runner-slot.sh' --container 'bounded-job' 30 -- docker create",
					"-e GOMAXPROCS=2", "-e GOMEMLIMIT=3GiB", "-e GOFLAGS=-p=2",
				} {
					if !strings.Contains(script, want) {
						t.Errorf("%s bypasses shared admission or parallelism bound %q", suite, want)
					}
				}
				if strings.Contains(script, "timeout --signal=TERM --kill-after=10s 31s docker") {
					t.Error("execution timeout starts before admission")
				}
			} else if strings.Contains(script, "--container") {
				t.Error("Node-only suite unexpectedly consumes a Go slot")
			}
		})
	}
}

func TestAdmissionQueueDoesNotConsumeExecutionBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queued := make(chan struct{})
		admit := make(chan struct{})
		completed := make(chan Result, 1)
		runner := Runner{DataDir: t.TempDir(), Remote: "ci@example:/tmp/cache", Command: func(ctx context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
			call := strings.Join(args, " ")
			if name == "git" && args[0] == "rev-parse" {
				if args[len(args)-1] == "--git-common-dir" {
					fmt.Fprintln(out, dir)
				} else {
					fmt.Fprintln(out, strings.Repeat("c", 40))
				}
			}
			if name == "ssh" && strings.Contains(call, "scripts/ci.sh cross-build") {
				close(queued)
				select {
				case <-admit:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if name == "ssh" && strings.Contains(call, "tar -C") {
				return tar.NewWriter(out).Close()
			}
			return nil
		}}
		go func() {
			result, err := runner.Run(t.Context(), Request{Repo: t.TempDir(), Suites: []string{"cross-build"}, Timeout: 30 * time.Second, DeferSave: true})
			if err != nil {
				result.Status = "error"
				result.Error = err.Error()
			}
			completed <- result
		}()
		<-queued
		// This timer models queue time in synctest's virtual clock. The actual
		// admission event remains explicit; no wall-clock sleep controls order.
		queueElapsed := time.NewTimer(31 * time.Second)
		defer queueElapsed.Stop()
		<-queueElapsed.C
		select {
		case result := <-completed:
			t.Fatalf("queue consumed execution timeout: %+v", result)
		default:
		}
		close(admit)
		if result := <-completed; result.Status != "passed" {
			t.Fatalf("admitted suite failed: %+v", result)
		}
	})
}

func TestOlderTargetRequiresHostAdmissionProtocol(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprintf("supported=%t", supported), func(t *testing.T) {
			dir := t.TempDir()
			work := filepath.Join(dir, "old-target")
			if err := os.MkdirAll(filepath.Join(work, "scripts"), 0700); err != nil {
				t.Fatal(err)
			}
			targetUsed := filepath.Join(dir, "target-gate-used")
			oldGate := fmt.Sprintf("#!/bin/sh\n: > %q\nexit 0\n", targetUsed)
			if err := os.WriteFile(filepath.Join(work, "scripts", "ci-runner-slot.sh"), []byte(oldGate), 0700); err != nil {
				t.Fatal(err)
			}
			hostGate := filepath.Join(dir, "host-gate")
			if supported {
				hostGate = writeTestHostGate(t, dir)
			} else if err := os.WriteFile(hostGate, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			executed := filepath.Join(dir, "suite-executed")
			fixture := fmt.Sprintf("#!/bin/sh\n[ \"$1\" = cross-build ] || exit 1\n: > %q\n", executed)
			if err := os.WriteFile(filepath.Join(work, "scripts", "ci.sh"), []byte(fixture), 0700); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(dir, "container-created")
			docker := fmt.Sprintf(`#!/bin/sh
case "$1" in
  create) : > %q; echo old-target-job ;;
  start) cd %q && bash scripts/ci.sh cross-build ;;
  rm) rm -f %q ;;
  container) if [ -f %q ]; then echo old-target-job; fi ;;
  *) exit 2 ;;
esac
`, state, work, state, state)
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(docker), 0700); err != nil {
				t.Fatal(err)
			}
			script := containerScript(dir, filepath.Join(dir, "cache.git"), work, filepath.Join(dir, "reports"), "old-target-job", "cross-build", 30*time.Second)
			script = strings.ReplaceAll(script, "/var/tmp/dyna-ci/ci-runner-slot.sh", hostGate)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", "-c", script)
			command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "CI_SLOT_DIR="+filepath.Join(dir, "slots"), "CI_SLOT_POLL=0.001")
			output, err := command.CombinedOutput()
			if _, used := os.Stat(targetUsed); used == nil {
				t.Fatal("used the untrusted hook from the older target revision")
			}
			if supported {
				if err != nil {
					t.Fatalf("trusted gate failed: %v: %s", err, output)
				}
				if _, err := os.Stat(executed); err != nil {
					t.Fatalf("returned success without running the suite through the host gate: %v", err)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 75 {
					t.Fatalf("unsupported host protocol returned false success or wrong error: %v: %s", err, output)
				}
				if _, err := os.Stat(executed); err == nil {
					t.Fatal("executed a container without a supported host gate")
				}
			}
		})
	}
}

// Force the keeper to publish its result and exit between the caller's
// empty-result observation and its process-liveness check.
func TestAdmissionAcceptsResultPublishedBeforeKeeperExit(t *testing.T) {
	dir := t.TempDir()
	gate := writeTestHostGate(t, dir)
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	barrier := filepath.Join(dir, "barrier.sh")
	if err := os.WriteFile(barrier, []byte(`function [() {
  if [[ $# == 4 && $1 == '!' && $2 == -s && $3 == */result && -z ${observed_result:-} ]]; then
    observed_result=true
    wait "$!"
    echo "injected empty-result observation before keeper exit" >&2
    return 0
  fi
  builtin [ "$@"
}
`), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "bash", gate, "--container", "keeper-race", "30", "--", "docker", "create", "--name", "keeper-race")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "BASH_ENV="+barrier, "CI_SLOT_DIR="+filepath.Join(dir, "slots"), "CI_SLOT_POLL=0.001")
	output, err := cmd.CombinedOutput()
	if !strings.Contains(string(output), "injected empty-result observation") {
		t.Fatalf("keeper exit barrier was not reached: %v: %s", err, output)
	}
	if err != nil {
		t.Fatalf("completed keeper result was rejected: %v: %s", err, output)
	}
}
