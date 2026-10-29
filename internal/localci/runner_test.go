package localci

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A run needs a registered node. There is one execution pipeline, so a nil node
// must stop the run before any command starts.
func TestRunNeedsARegisteredNode(t *testing.T) {
	runner := Runner{DataDir: t.TempDir(), Command: func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error {
		t.Fatal("ran a command without a registered node")
		return nil
	}}
	if _, err := runner.Run(t.Context(), Request{Definition: testDynaDefinition(t), Repo: t.TempDir(), Suites: []Suite{SuiteRelay}}); err == nil || !strings.Contains(err.Error(), "registered node") {
		t.Fatalf("err=%v", err)
	}
}

// A mistyped suite never starts work on the node.
func TestRunRejectsUnknownSuiteBeforeAnyCommand(t *testing.T) {
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "local", Slots: 1, Enabled: true}, Command: func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error {
		t.Fatal("ran a command for an unknown suite")
		return nil
	}}
	if _, err := runner.Run(t.Context(), Request{Definition: testDynaDefinition(t), Repo: t.TempDir(), Suites: []Suite{"go-test-typo"}}); err == nil {
		t.Fatal("accepted an unknown suite")
	}
}

// A short SHA can name no stored result, so the guard needs no repository:
// it must stop the read before any git command runs.
func TestShowRejectsAShortSHABeforeAnyCommand(t *testing.T) {
	runner := Runner{DataDir: t.TempDir(), Command: func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error {
		t.Fatal("ran a command for a short SHA")
		return nil
	}}
	if _, err := runner.Show(t.TempDir(), "abc123"); err == nil || !strings.Contains(err.Error(), "full commit SHA") {
		t.Fatalf("err=%v", err)
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
	if err := checkReports(&result, ReportGo, dir, 10*time.Second, nil); err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) == 0 || !strings.Contains(result.Failures[0], "TestBroken") {
		t.Fatalf("lost failure details: %+v", result)
	}
}

// Every finished check now leaves the node's exit record in its report
// directory (D-169). That record is transport evidence, not a Go event stream:
// the Go report reader must skip it, or a check that passed with perfect
// reports fails as reports-unavailable.
func TestGoReportsSkipTheNodeExitRecord(t *testing.T) {
	dir := t.TempDir()
	reports := filepath.Join(dir, "reports", "go-test")
	if err := os.MkdirAll(reports, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(reports, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("execution.json", `{"durationNanos":1000000}`)
	write("go-test.xml", `<testsuite><testcase name="TestAlive" classname="pkg" time="0.1"/></testsuite>`)
	write("go-test.json", "{\"Action\":\"pass\",\"Package\":\"pkg\",\"Test\":\"TestAlive\",\"Elapsed\":0.1}\n"+
		"{\"Action\":\"pass\",\"Package\":\"pkg\"}\n")

	result := SuiteResult{Suite: "go-test", Status: OutcomePassed}
	readReports(&result, ReportGo, dir, 10*time.Second, nil)
	if result.Status != OutcomePassed || result.Cause != "" {
		t.Fatalf("status=%s cause=%s error=%q, want a passed check with no report problem", result.Status, result.Cause, result.Error)
	}
	if len(result.Reports) != 2 {
		t.Fatalf("reports = %v, want the JUnit and Go stream files only", result.Reports)
	}
}

// A test that only ran over the room's per-test time budget did not fail. The
// check keeps passing here and its cause names the budget: whether that fails
// the check is the room's ciTimeBudget decision, made by the watcher, so nobody
// hunts a bug in a test that passed.
func TestOverBudgetTestIsNotATestFailure(t *testing.T) {
	dir := t.TempDir()
	reports := filepath.Join(dir, "reports", "frontend")
	if err := os.MkdirAll(reports, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reports, "execution.json"), []byte(`{"durationNanos":1000000}`), 0600); err != nil {
		t.Fatal(err)
	}
	write := func(xml string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(reports, "frontend.xml"), []byte(xml), 0600); err != nil {
			t.Fatal(err)
		}
	}

	write(`<testsuite><testcase name="slow" classname="rooms" time="2.5"/><testcase name="fast" classname="rooms" time="0.1"/></testsuite>`)
	result := SuiteResult{Suite: "frontend", Status: OutcomePassed}
	readReports(&result, ReportJUnit, dir, 2*time.Second, nil)
	if result.Status != OutcomePassed || result.Cause != CauseOverBudget {
		t.Fatalf("status=%s cause=%s, want passed with cause %s", result.Status, result.Cause, CauseOverBudget)
	}
	if len(result.Failures) != 0 {
		t.Fatalf("an over-budget test was listed as a failure: %v", result.Failures)
	}
	if label := result.Cause.Label(); label != "over the time budget" {
		t.Fatalf("label=%q, want the time budget", label)
	}

	// A broken test keeps its own cause, even beside one that ran too long.
	write(`<testsuite><testcase name="slow" classname="rooms" time="2.5"/><testcase name="broken" classname="rooms" time="0.1"><failure message="boom"/></testcase></testsuite>`)
	failed := SuiteResult{Suite: "frontend", Status: OutcomePassed}
	readReports(&failed, ReportJUnit, dir, 2*time.Second, nil)
	if failed.Cause != CauseTestFailure {
		t.Fatalf("cause=%s, want %s when a test failed", failed.Cause, CauseTestFailure)
	}
}

// Inspect the bytes sent over the transport, before readArchive can filter
// them. Build tools and logs share RUNNER_TEMP with the required reports.
func TestNodeRunArchivesOnlyRequiredReportFiles(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	wanted := map[string]string{
		"relay/relay-test.xml":         `<testsuite><testcase name="fast" time="0.001"/></testsuite>`,
		"relay/details.json":           `{"result":"kept"}`,
		"relay/report with space.json": `{"space":true}`,
		"relay/report\nline.json":      `{"newline":true}`,
		"relay/ci-diagnostics.jsonl":   "{\"kind\":\"counter\"}\n",
		"relay/execution.json":         `{"durationNanos":1000000}`,
	}
	unwanted := map[string]string{"relay/dyna-ci": "binary contents", "relay/suite.log": "unrelated output", "relay/unrelated.jsonl": "ignored log", "relay/nested/report.xml/tool": "directory contents", "relay/node_modules/tool/package.json": `{"name":"tool"}`, "orphan.json": `{"unconsumed":true}`}
	var archived map[string]string
	var remoteReports string
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "local", Slots: 1, Enabled: true}}
	legacy := func(ctx context.Context, dir string, _ io.Reader, out, stderr io.Writer, name string, args ...string) error {
		if name == "git" {
			if args[0] == "rev-parse" {
				if args[len(args)-1] == "--git-common-dir" {
					fmt.Fprintln(out, dir)
					return nil
				}
				fmt.Fprintln(out, strings.Repeat("e", 40))
			}
			return nil
		}
		script := args[len(args)-1]
		if !strings.Contains(script, "tar -C") {
			_, err := fmt.Fprintln(out, "DYNA_CI_CHECK_EXIT=0")
			return err
		}
		match := regexp.MustCompile(`cd '([^']*/reports)'`).FindStringSubmatch(script)
		if match == nil {
			return fmt.Errorf("missing archive report root: %s", script)
		}
		remoteReports = match[1]
		t.Cleanup(func() { os.RemoveAll(filepath.Dir(remoteReports)) })
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
	}
	newFakeMachine(t, legacy).attach(&runner)
	result, err := runner.Run(ctx, Request{Definition: testDynaDefinition(t), Repo: t.TempDir(), Suites: []Suite{SuiteRelay}, Timeout: 30 * time.Second, MaxTest: time.Second})
	if err != nil || result.Status != OutcomePassed {
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
	// The bounded diagnostics log stays reachable from the saved result.
	diagnostics := false
	for _, report := range result.Checks[0].Reports {
		diagnostics = diagnostics || filepath.Base(report) == "ci-diagnostics.jsonl"
	}
	if !diagnostics {
		t.Fatalf("saved result lost its diagnostics log: %+v", result.Checks[0])
	}
}

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
