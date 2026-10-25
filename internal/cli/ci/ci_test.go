package ci

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"regexp"
	"time"

	"dyna-agent/internal/cibudget"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func execute(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(append([]string{"tests"}, args...))
	err := cmd.Execute()
	return output.String(), err
}

func TestSlowTestOnlyReported(t *testing.T) {
	output, err := execute(t, `{"Action":"pass","Package":"p","Test":"TestSlow","Elapsed":12}`)
	if err != nil || strings.Contains(output, "OVER BUDGET") || !strings.Contains(output, "p/TestSlow") {
		t.Fatalf("%v: %s", err, output)
	}
	// The command reports durations only: the room's limit is the PR watcher's
	// verdict on the uploaded JUnit report. A summary that names a budget verdict
	// of its own reads as a pass next to a check the watcher then fails.
	if strings.Contains(strings.ToLower(output), "over budget") {
		t.Fatalf("the report claimed a budget verdict it does not make: %s", output)
	}
}

func TestFailedTestStillFails(t *testing.T) {
	if _, err := execute(t, `{"Action":"fail","Package":"p","Test":"broken","Elapsed":1}`); err == nil {
		t.Fatal("failed test passed")
	}
}

func TestJUnitGlobs(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.xml", "b.xml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`<testsuite><testcase name="ok" time="0.01"/></testsuite>`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pattern := filepath.Join(dir, "*.xml")
	output, err := execute(t, "", "--junit", pattern, "--junit", pattern)
	if err != nil || !strings.Contains(output, "2 tests;") {
		t.Fatalf("%v: %s", err, output)
	}
	if _, err := execute(t, "", "--junit", filepath.Join(dir, "missing*")); err == nil {
		t.Fatal("missing reports accepted")
	}
}

func TestInvalidOptionsAndEmptyReports(t *testing.T) {
	for _, args := range [][]string{{"a", "b"}, nil} {
		if _, err := execute(t, "", args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := execute(t, `{"Action":"pass","Package":"p"}`); err == nil {
		t.Fatal("report without completed tests accepted")
	}
}

func TestCombinedGoAndJUnitReports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tests.xml")
	if err := os.WriteFile(path, []byte(`<testsuites><testsuite><testcase classname="ui" name="fast" time="0.02"/></testsuite></testsuites>`), 0600); err != nil {
		t.Fatal(err)
	}
	input := `{"Action":"pass","Package":"p","Test":"TestGo","Elapsed":0.01}`
	output, err := execute(t, input, "--junit", path, "-")
	if err != nil || !strings.Contains(output, "2 tests;") || !strings.Contains(output, "p/TestGo") || !strings.Contains(output, "ui/fast") {
		t.Fatalf("%v: %s", err, output)
	}
}

func TestJUnitArtifactWrittenForSlowTest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go.xml")
	if _, err := execute(t, `{"Action":"pass","Package":"p","Test":"slow","Elapsed":12}`, "--junit-output", path); err != nil {
		t.Fatalf("slow test failed the check: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `name="slow"`) || !strings.Contains(string(data), `time="12"`) {
		t.Fatalf("artifact: %s %v", data, err)
	}
}

// A web test flagged slow under load is run alone with node. Node 22's JUnit
// reporter (the CI version) does not name the test file, so the file comes from
// the title and file lines that the test reporter wrote, which also cover titles
// generated at run time. The report is rewritten with the isolated time.
func TestIsolateRunsSlowWebTestsAloneWithNode(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	fake := "#!/bin/sh\necho \"$*\" >> " + calls + "\n" +
		"case \"$*\" in\n" +
		"*generated*) echo '<testsuites><testcase name=\"room 7 cannot send\" time=\"0.3\" classname=\"test\"/></testsuites>';;\n" +
		"*Whole*) echo '<testsuites><testcase name=\"src/Whole.test.js\" time=\"0.4\" classname=\"test\"/></testsuites>';;\n" +
		"*) echo '<testsuites><testcase name=\"the sidebar renders\" time=\"0.2\" classname=\"test\"/></testsuites>';;\n" +
		"esac\n"
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "node"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Chdir(dir)
	if err := os.MkdirAll("src", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("src/Whole.test.js", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	files := filepath.Join(dir, "files.jsonl")
	// "room 7 cannot send" never appears in a test file: its title is built from a template.
	recorded := `{"name":"the sidebar renders","file":"src/Sidebar.test.js"}
{"name":"room 7 cannot send","file":"src/generated.test.js"}
{"name":"fast","file":"src/Fast.test.js"}
`
	if err := os.WriteFile(files, []byte(recorded), 0o644); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(dir, "frontend.xml")
	// Shaped like the report of node 22, with no file attribute.
	loaded := `<testsuites>
<testcase name="the sidebar renders" time="4.5" classname="test"/>
<testcase name="room 7 cannot send" time="2.5" classname="test"/>
<testcase name="src/Whole.test.js" time="3.2" classname="test"/>
<testcase name="not recorded" time="2" classname="test"/>
<testcase name="fast" time="0.1" classname="test"/>
</testsuites>`
	if err := os.WriteFile(report, []byte(loaded), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := execute(t, "", "--junit", report, "--test-files", files, "--isolate-over", "1s", "--junit-output", report)
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	for _, want := range []string{
		"ISOLATED test/the sidebar renders: 4.5s in the full run, 200ms alone",
		"ISOLATED test/room 7 cannot send: 2.5s in the full run, 300ms alone",
		"ISOLATED test/src/Whole.test.js: 3.2s in the full run, 400ms alone",
		"ISOLATED test/not recorded: 2s in the full run; isolated run failed: the test reporter did not record a file for this test",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output lacks %q:\n%s", want, output)
		}
	}
	log, _ := os.ReadFile(calls)
	if got := string(log); !strings.Contains(got, "--test-name-pattern=^the sidebar renders$ src/Sidebar.test.js\n") ||
		!strings.Contains(got, "--test-name-pattern=^room 7 cannot send$ src/generated.test.js\n") ||
		!strings.Contains(got, "--test --test-reporter=junit src/Whole.test.js\n") || strings.Contains(got, "Fast") {
		t.Fatalf("node calls:\n%s", got)
	}
	rewritten, _ := os.ReadFile(report)
	if !strings.Contains(string(rewritten), `time="0.2"`) || !strings.Contains(string(rewritten), `time="0.3"`) || !strings.Contains(string(rewritten), `time="0.4"`) || strings.Contains(string(rewritten), `time="4.5"`) {
		t.Fatalf("report keeps the loaded times:\n%s", rewritten)
	}
}

func fakeGo(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestGoTestAloneFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, stdout, stderr string
		want                 []string
	}{
		{
			name: "assertion",
			stdout: `{"Action":"output","Package":"p","Test":"TestSlow","Output":"    slow_test.go:7: expected success\n"}
{"Action":"fail","Package":"p","Test":"TestSlow","Elapsed":0.01}
{"Action":"fail","Package":"p"}`,
			want: []string{"FAIL p", "slow_test.go:7: expected success"},
		},
		{
			name: "timeout",
			stdout: `{"Action":"output","Package":"p","Output":"panic: test timed out after 30s\nTestSlow blocked at slow_test.go:12\n"}
{"Action":"fail","Package":"p"}`,
			want: []string{"panic: test timed out after 30s", "slow_test.go:12"},
		},
		{
			name:   "stderr",
			stderr: "slow_test.go:3: undefined: missing\n",
			want:   []string{"slow_test.go:3: undefined: missing"},
		},
		{
			name:   "missing failure event",
			stdout: `{"Action":"output","Package":"p","Output":"process stopped at slow_test.go:19\n"}`,
			want:   []string{"process stopped at slow_test.go:19"},
		},
		{
			name: "partial JSON",
			stdout: `{"Action":"output","Package":"p","Output":"starting test\n"}
{"Action":`,
			stderr: "test process stopped\n",
			want:   []string{"read Go test JSON", "test process stopped"},
		},
		{
			name: "bounded output",
			stdout: `{"Action":"output","Package":"p","Output":"` + strings.Repeat("x", 10000) + ` assertion at tail\n"}
{"Action":"fail","Package":"p"}`,
			stderr: strings.Repeat("y", 10000) + " stderr at tail\n",
			want:   []string{"assertion at tail", "stderr at tail", "[earlier stdout omitted]", "[earlier stderr omitted]"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeGo(t, "cat <<'STDOUT'\n"+tc.stdout+"\nSTDOUT\ncat >&2 <<'STDERR'\n"+tc.stderr+"\nSTDERR\nexit 1\n")
			duration, err := goTestAlone(context.Background(), cibudget.Test{Package: "p", Name: "TestSlow"}, false)
			var exitErr *exec.ExitError
			if duration != 0 || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("duration %s; error %v", duration, err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error lacks %q: %v", want, err)
				}
			}
			if len(err.Error()) > 9000 {
				t.Fatalf("diagnostics are unbounded: %d bytes", len(err.Error()))
			}
		})
	}
}

func TestGoTestAlonePreservesContextError(t *testing.T) {
	fakeGo(t, "exit 0\n")
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"canceled", func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}(), context.Canceled},
		{"deadline", func() context.Context {
			ctx, cancel := context.WithDeadline(context.Background(), time.Time{})
			t.Cleanup(cancel)
			return ctx
		}(), context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := goTestAlone(tc.ctx, cibudget.Test{Package: "p", Name: "TestSlow"}, false)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error %v does not preserve %v", err, tc.want)
			}
		})
	}
}

func TestGoTestAloneArgumentsAndTiming(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "race"}[race], func(t *testing.T) {
			calls := filepath.Join(t.TempDir(), "calls")
			t.Setenv("GO_CALLS", calls)
			fakeGo(t, `printf "%s\n" "$@" > "$GO_CALLS"
cat <<'STDOUT'
{"Action":"pass","Package":"p","Test":"TestSlow/a+b","Elapsed":0.125}
{"Action":"pass","Package":"p"}
STDOUT
`)
			duration, err := goTestAlone(context.Background(), cibudget.Test{Package: "p", Name: "TestSlow/a+b"}, race)
			if err != nil || duration != 125*time.Millisecond {
				t.Fatalf("duration %s; error %v", duration, err)
			}
			args, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			want := "test\n-json\n-count=1\n-timeout\n30s\n-run\n^TestSlow$/^" + regexp.QuoteMeta("a+b") + "$\n"
			if race {
				want += "-race\n"
			}
			want += "p\n"
			if string(args) != want {
				t.Fatalf("arguments:\n%s\nwant:\n%s", args, want)
			}
			if isolateBudget != 30*time.Second || isolateTimeout < 30*time.Second {
				t.Fatalf("isolation budget %s; watchdog %s", isolateBudget, isolateTimeout)
			}
		})
	}
}

func TestIsolateGoFailureKeepsLoadedTime(t *testing.T) {
	fakeGo(t, `cat <<'STDOUT'
{"Action":"output","Package":"p","Output":"slow_test.go:7: expected success\n"}
{"Action":"fail","Package":"p","Test":"TestSlow","Elapsed":0.01}
{"Action":"fail","Package":"p"}
STDOUT
exit 1
`)
	output, err := execute(t, `{"Action":"pass","Package":"p","Test":"TestSlow","Elapsed":2}`, "--isolate-over", "1s")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"isolated run failed: exit status 1", "slow_test.go:7: expected success", "2s  p/TestSlow"} {
		if !strings.Contains(output, want) {
			t.Errorf("output lacks %q:\n%s", want, output)
		}
	}
}
