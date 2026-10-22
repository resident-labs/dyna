package cibudget

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestGoBudgetAndFailureOutput(t *testing.T) {
	input := `{"Action":"output","Package":"example/p","Output":"    broken_test.go:7: expected success\n"}
{"Action":"pass","Package":"example/p","Test":"TestBoundary","Elapsed":10}
{"Action":"pass","Package":"example/p","Test":"TestSlow/child","Elapsed":11.2}
{"Action":"fail","Package":"example/p","Test":"TestBroken","Elapsed":0.01}
{"Action":"fail","Package":"example/p","Elapsed":100}`
	var report Report
	if err := report.ReadGo(strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if report.Write(&output, 10*time.Second, nil) {
		t.Fatal("failure passed")
	}
	for _, want := range []string{"broken_test.go:7: expected success", "OVER BUDGET example/p/TestSlow/child: 11.2s", "3 tests; 1 over budget; 1 failed"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in %s", want, &output)
		}
	}
	if strings.Contains(output.String(), "OVER BUDGET example/p/TestBoundary") {
		t.Fatal("boundary rejected")
	}
	if len(report.Tests) != 3 {
		t.Fatalf("package duration counted: %+v", report.Tests)
	}
}

func TestAllowNamesAndComments(t *testing.T) {
	allowed, err := ReadAllow(strings.NewReader("# reason\n\n p/TestSlow  # measured 11s\nq/TestOther\n"))
	if err != nil {
		t.Fatal(err)
	}
	report := Report{Tests: []Test{{Package: "p", Name: "TestSlow", Duration: 11 * time.Second}, {Package: "q", Name: "TestOther", Duration: 20 * time.Second}}}
	if !report.Write(new(bytes.Buffer), 10*time.Second, allowed) {
		t.Fatal("allowed tests rejected")
	}
	report.Failures = []string{"FAIL p/TestSlow"}
	if report.Write(new(bytes.Buffer), 10*time.Second, allowed) {
		t.Fatal("allow hid test failure")
	}
}

func TestJUnitNestedSuitesAndErrors(t *testing.T) {
	var report Report
	input := `<testsuites><testsuite><testsuite><testcase classname="widget" name="slow" time="10.001"/><testcase name="broken"><failure message="assert failed">line 7</failure></testcase><testcase name="error"><error message="crashed"/></testcase><testcase name="skipped"><skipped/></testcase></testsuite></testsuite></testsuites>`
	if err := report.ReadJUnit(strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	if len(report.Tests) != 4 || len(report.Failures) != 2 {
		t.Fatalf("report: %+v", report)
	}
	var output bytes.Buffer
	if report.Write(&output, 10*time.Second, nil) {
		t.Fatal("failures passed")
	}
	if !strings.Contains(output.String(), "widget/slow: 10.001s") || !strings.Contains(output.String(), "assert failed\nline 7") {
		t.Fatal(output.String())
	}
}

func TestInvalidInput(t *testing.T) {
	for _, input := range []string{"", "not JSON", `{`, `{"Action":"pass","Test":"Bad","Elapsed":-1}`, `{"Action":"pass","Test":"Bad","Elapsed":1e20}`} {
		t.Run("go:"+input, func(t *testing.T) {
			var r Report
			if r.ReadGo(strings.NewReader(input)) == nil {
				t.Fatal("accepted invalid Go input")
			}
		})
	}
	for _, input := range []string{"", "<wrong/>", "<testsuite>", `<testsuite><testcase name="bad" time="NaN"/></testsuite>`, `<testsuite><testcase/></testsuite>`, `<testsuite/><testsuite/>`, `text<testsuite/>`} {
		t.Run("xml:"+input, func(t *testing.T) {
			var r Report
			if r.ReadJUnit(strings.NewReader(input)) == nil {
				t.Fatal("accepted invalid XML")
			}
		})
	}
}

func TestSlowestLimitAndStableOrder(t *testing.T) {
	var report Report
	for i := 11; i >= 0; i-- {
		report.Tests = append(report.Tests, Test{Name: fmt.Sprintf("Test%02d", i), Duration: time.Second})
	}
	var output bytes.Buffer
	if !report.Write(&output, 10*time.Second, nil) {
		t.Fatal("check failed")
	}
	if strings.Contains(output.String(), "Test10") || strings.Contains(output.String(), "Test11") {
		t.Fatal(output.String())
	}
	if strings.Index(output.String(), "Test00") > strings.Index(output.String(), "Test01") {
		t.Fatal("unstable order")
	}
	if report.Tests[0].Name != "Test11" {
		t.Fatal("report mutated")
	}
}

func TestGoTestFailureWithoutPackageEvent(t *testing.T) {
	var report Report
	if err := report.ReadGo(strings.NewReader(`{"Action":"fail","Package":"p","Test":"TestBroken","Elapsed":0.1}`)); err != nil {
		t.Fatal(err)
	}
	if report.Write(new(bytes.Buffer), 10*time.Second, nil) {
		t.Fatal("test failure passed without a package event")
	}
}

func TestAllowPreservesDuplicateSubtestSuffix(t *testing.T) {
	allowed, err := ReadAllow(strings.NewReader(" # full-line comment\np/TestFoo/#01  # duplicate case\nTestFoo/#00\nTestFoo#literal\np/TestBar\t# tab comment\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"p/TestFoo/#01", "TestFoo/#00", "TestFoo#literal", "p/TestBar"} {
		if !allowed[name] {
			t.Fatalf("missing exact name %q in %v", name, allowed)
		}
	}
	if len(allowed) != 4 {
		t.Fatalf("unexpected allowed names: %v", allowed)
	}
	report := Report{Tests: []Test{{Package: "p", Name: "TestFoo/#01", Duration: 11 * time.Second}}}
	if !report.Write(new(bytes.Buffer), 10*time.Second, allowed) {
		t.Fatal("duplicate subtest name was not allowed")
	}
}

func TestJUnitExportRoundTrip(t *testing.T) {
	original := Report{Tests: []Test{{Package: "pkg&x", Name: "Test/a<b", Duration: 12500 * time.Millisecond}}}
	var output bytes.Buffer
	if err := original.WriteJUnit(&output); err != nil {
		t.Fatal(err)
	}
	var restored Report
	if err := restored.ReadJUnit(&output); err != nil {
		t.Fatal(err)
	}
	if len(restored.Tests) != 1 || restored.Tests[0] != original.Tests[0] {
		t.Fatalf("round trip: %+v", restored)
	}
}

func TestBudgetLeavesAndPackageBoundary(t *testing.T) {
	var report Report
	if err := report.ReadGo(strings.NewReader(`{"Action":"pass","Package":"p","Test":"TestTable/a","Elapsed":1}
{"Action":"pass","Package":"p","Test":"TestTable/b","Elapsed":1}
{"Action":"pass","Package":"p","Test":"TestTable","Elapsed":2}
{"Action":"pass","Package":"q","Test":"TestTable","Elapsed":11}`)); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	report.Write(&out, 10*time.Second, nil)
	if strings.Contains(out.String(), "OVER BUDGET p/TestTable:") || !strings.Contains(out.String(), "OVER BUDGET q/TestTable:") {
		t.Fatal(out.String())
	}
}
func TestAllowLiteralHashAndWarnings(t *testing.T) {
	allowed, err := ReadAllow(strings.NewReader("widget/renders card #1  # debt\nrenders\nmissing/name\n"))
	if err != nil {
		t.Fatal(err)
	}
	report := Report{Tests: []Test{{Package: "widget", Name: "renders card #1", Duration: 11 * time.Second}, {Package: "widget", Name: "renders", Duration: time.Second}}}
	var out bytes.Buffer
	if !report.Write(&out, 10*time.Second, allowed) {
		t.Fatal(out.String())
	}
	for _, want := range []string{"matches no test", "missing/name"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, &out)
		}
	}
}
func TestGoBuildFailureImportPath(t *testing.T) {
	var report Report
	err := report.ReadGo(strings.NewReader(`{"Action":"build-output","ImportPath":"example/p","Output":"broken.go:4: undefined: value\n"}
{"Action":"build-fail","ImportPath":"example/p"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if report.Write(&out, 10*time.Second, nil) || !strings.Contains(out.String(), "FAIL example/p\nbroken.go:4") {
		t.Fatal(out.String())
	}
}

func TestGoParentOwnTimeAndLiteralJUnitNames(t *testing.T) {
	var goReport Report
	if err := goReport.ReadGo(strings.NewReader(`{"Action":"pass","Package":"p","Test":"TestX/a","Elapsed":1}
{"Action":"pass","Package":"p","Test":"TestX/b","Elapsed":1}
{"Action":"pass","Package":"p","Test":"TestX","Elapsed":32}`)); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if goReport.Write(&out, 10*time.Second, nil) || !strings.Contains(out.String(), "OVER BUDGET p/TestX: 30s") {
		t.Fatal(out.String())
	}
	var junit Report
	if err := junit.ReadJUnit(strings.NewReader(`<testsuite><testcase classname="widget" name="a" time="30"/><testcase classname="widget" name="a/b" time="1"/></testsuite>`)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if junit.Write(&out, 10*time.Second, nil) || !strings.Contains(out.String(), "OVER BUDGET widget/a: 30s") {
		t.Fatal(out.String())
	}
}
func TestGoOwnTimeNestedParallelAndRepeated(t *testing.T) {
	var report Report
	input := `{"Action":"pass","Package":"p","Test":"TestNested/child/grandchild","Elapsed":1}
{"Action":"pass","Package":"p","Test":"TestNested/child","Elapsed":2}
{"Action":"pass","Package":"p","Test":"TestNested","Elapsed":3}
{"Action":"pass","Package":"p","Test":"TestParallel/a","Elapsed":4}
{"Action":"pass","Package":"p","Test":"TestParallel/b","Elapsed":4}
{"Action":"pass","Package":"p","Test":"TestParallel","Elapsed":5}
{"Action":"pass","Package":"p","Test":"TestNested/child","Elapsed":1}
{"Action":"pass","Package":"p","Test":"TestNested","Elapsed":2}`
	if err := report.ReadGo(strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{time.Second, time.Second, time.Second, 4 * time.Second, 4 * time.Second, 0, time.Second, time.Second}
	for i, test := range report.BudgetTests() {
		if test.Duration != want[i] {
			t.Fatalf("test %d own time %s; want %s", i, test.Duration, want[i])
		}
	}
}

// A loaded runner reports a full-run time that the isolated re-run replaces.
// The artifact says which times a run alone confirmed, so the tracker can tell
// a slow test from a slow runner.
func TestJUnitMarksTheTimesARunAloneConfirmed(t *testing.T) {
	report := Report{
		IsolationRan: true,
		Tests: []Test{
			{Package: "internal/harness", Name: "TestQuota", Duration: 40 * time.Millisecond, Isolated: true, IsolationRan: true},
			{Package: "internal/harness", Name: "TestLoaded", Duration: 37 * time.Second, IsolationRan: true},
			{Package: "internal/harness", Name: "TestFast", Duration: 20 * time.Millisecond, IsolationRan: true},
		},
	}
	var out bytes.Buffer
	if err := report.WriteJUnit(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `isolation="ran"`) || strings.Count(out.String(), `isolated="true"`) != 1 {
		t.Fatalf("artifact lacks its markers:\n%s", out.String())
	}
	var back Report
	if err := back.ReadJUnit(strings.NewReader(out.String())); err != nil {
		t.Fatal(err)
	}
	if !back.IsolationRan || len(back.Tests) != 3 {
		t.Fatalf("round trip: %+v", back)
	}
	if !back.Tests[0].Isolated || !back.Tests[0].IsolationRan || back.Tests[0].Duration != 40*time.Millisecond {
		t.Fatalf("confirmed time: %+v", back.Tests[0])
	}
	if back.Tests[1].Isolated || !back.Tests[1].IsolationRan || back.Tests[1].Duration != 37*time.Second {
		t.Fatalf("loaded time kept its value but must not look confirmed: %+v", back.Tests[1])
	}
}

// An artifact from a suite that never isolated anything, such as the relay
// one, carries no markers: its times stay the only evidence there is.
func TestPlainJUnitHasNoIsolation(t *testing.T) {
	const artifact = `<testsuites>
  <testsuite name="relay" tests="3">
    <testcase classname="relay" name="round trip" time="1.24"/>
    <testcase classname="relay" name="a slow one" time="12.5"/>
    <testcase classname="relay" name="fast" time="0.01"/>
  </testsuite>
</testsuites>`
	var report Report
	if err := report.ReadJUnit(strings.NewReader(artifact)); err != nil {
		t.Fatal(err)
	}
	if report.IsolationRan {
		t.Fatal("a plain artifact claims isolation ran")
	}
	for _, test := range report.Tests {
		if test.Isolated {
			t.Fatalf("a plain artifact marks %s as isolated", test.ID())
		}
	}
	if len(report.Tests) != 3 || report.Tests[1].Duration != 12500*time.Millisecond {
		t.Fatalf("plain artifact times: %+v", report.Tests)
	}
}

// Isolate marks only the times it replaced, and says the report was measured
// that way even when the budget stops it early.
func TestIsolateMarksOnlyReplacedTimes(t *testing.T) {
	report := Report{Tests: []Test{
		{Package: "p", Name: "TestSlow", Duration: 30 * time.Second},
		{Package: "p", Name: "TestSlow/child", Duration: time.Second},
		{Package: "p", Name: "TestFast", Duration: 20 * time.Millisecond},
	}}
	report.Isolate(context.Background(), time.Second, func(_ context.Context, test Test) (time.Duration, error) {
		if test.Name == "TestSlow" {
			return 300 * time.Millisecond, nil
		}
		return 0, errors.New("cannot run alone")
	}, io.Discard)
	if !report.IsolationRan {
		t.Fatal("isolate did not mark the report")
	}
	if got := report.Tests[0]; !got.Isolated || !got.IsolationRan || got.Duration != 300*time.Millisecond {
		t.Fatalf("replaced time: %+v", got)
	}
	// The subtest's isolated run failed, so it keeps its full-run time and no
	// confirmation: nobody measured it alone, and its suite did isolate.
	if got := report.Tests[1]; got.Isolated || !got.IsolationRan || got.Duration != time.Second {
		t.Fatalf("failed isolated run: %+v", got)
	}
}

func TestBareAllowDoesNotExemptTwoPackages(t *testing.T) {
	report := Report{Tests: []Test{
		{Package: "one", Name: "TestShared", Duration: 2 * time.Second},
		{Package: "two", Name: "TestShared", Duration: 2 * time.Second},
	}}
	var out bytes.Buffer
	if report.Write(&out, time.Second, map[string]bool{"TestShared": true}) ||
		strings.Count(out.String(), "OVER BUDGET") != 2 ||
		!strings.Contains(out.String(), "allow entry \"TestShared\" matches no test") {
		t.Fatalf("bare name exempted qualified tests: %s", &out)
	}
	out.Reset()
	if report.Write(&out, time.Second, map[string]bool{"one/TestShared": true}) ||
		strings.Count(out.String(), "OVER BUDGET") != 1 ||
		!strings.Contains(out.String(), "OVER BUDGET two/TestShared") {
		t.Fatalf("qualified ID crossed package boundary: %s", &out)
	}
	out.Reset()
	literal := Report{Tests: []Test{{Name: "literal/name #1", Duration: 2 * time.Second}}}
	if !literal.Write(&out, time.Second, map[string]bool{"literal/name #1": true}) ||
		strings.Contains(out.String(), "WARNING") {
		t.Fatalf("package-less literal ID was not accepted: %s", &out)
	}
}
