// Package cibudget checks individual test durations from Go and JUnit reports.
package cibudget

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Test struct {
	Package  string
	Name     string
	Duration time.Duration
	// Isolated marks a time that a run of this test alone replaced with the
	// shorter measurement. A duration over the limit without it is a full-run
	// time that a loaded runner can inflate.
	Isolated bool
	// IsolationRan records that this test's own suite was measured with
	// isolated re-runs. It travels with the test, so merging several reports
	// into one keeps each suite's provenance; a test from a suite without
	// isolation has nothing to wait for and is judged on its time alone.
	IsolationRan bool
}

func (t Test) ID() string {
	if t.Package == "" {
		return t.Name
	}
	return t.Package + "/" + t.Name
}

type Report struct {
	Tests    []Test
	Failures []string
	// IsolationRan records that the report was measured with Isolate, so a
	// reader knows an unmarked time over the limit was not confirmed alone.
	IsolationRan bool
}

// ReadGo reads the event stream from go test -json. Package elapsed times are
// deliberately excluded: the budget applies to individual tests and subtests.
func (r *Report) ReadGo(input io.Reader) error {
	dec := json.NewDecoder(input)
	output := make(map[string]*strings.Builder)
	failed := make(map[string]bool)
	var failedPackages []string
	childTimes := map[string]time.Duration{}
	events := 0
	for {
		var event struct {
			Action, Package, ImportPath, Test, Output string
			Elapsed                                   float64
		}
		err := dec.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read Go test JSON: %w", err)
		}
		if event.Action == "" {
			return fmt.Errorf("Go test event has no Action")
		}
		events++
		if event.Package == "" {
			event.Package = event.ImportPath
		}
		if event.Output != "" {
			if output[event.Package] == nil {
				output[event.Package] = new(strings.Builder)
			}
			output[event.Package].WriteString(event.Output)
		}
		switch event.Action {
		case "pass", "fail", "skip", "build-fail":
			if event.Test != "" {
				duration, err := seconds(event.Elapsed)
				if err != nil {
					return fmt.Errorf("%s/%s: %w", event.Package, event.Test, err)
				}
				// Go reports parent elapsed time including direct children. Charge
				// only the remaining parent work, and keep ordinary JUnit names literal.
				key := event.Package + "\x00" + event.Test
				own := duration
				if children := childTimes[key]; children >= own {
					own = 0
				} else {
					own -= children
				}
				delete(childTimes, key)
				if slash := strings.LastIndex(event.Test, "/"); slash >= 0 {
					parent := event.Package + "\x00" + event.Test[:slash]
					if duration > time.Duration(math.MaxInt64)-childTimes[parent] {
						childTimes[parent] = time.Duration(math.MaxInt64)
					} else {
						childTimes[parent] += duration
					}
				}
				r.Tests = append(r.Tests, Test{Package: event.Package, Name: event.Test, Duration: own})
			}
			if (event.Action == "fail" || event.Action == "build-fail") && !failed[event.Package] {
				failed[event.Package] = true
				failedPackages = append(failedPackages, event.Package)
			}
			if event.Test == "" && event.Action != "fail" && event.Action != "build-fail" && !failed[event.Package] {
				delete(output, event.Package)
			}
		}
	}
	for _, pkg := range failedPackages {
		detail := ""
		if output[pkg] != nil {
			detail = strings.TrimSpace(output[pkg].String())
		}
		r.Failures = append(r.Failures, strings.TrimSpace("FAIL "+pkg+"\n"+detail))
	}
	if events == 0 {
		return fmt.Errorf("Go test JSON input is empty")
	}
	return nil
}

func seconds(value float64) (time.Duration, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value >= float64(math.MaxInt64)/float64(time.Second) {
		return 0, fmt.Errorf("invalid test duration %v", value)
	}
	return time.Duration(value * float64(time.Second)), nil
}

// ReadJUnit accepts testsuite and testsuites roots, including nested suites.
func (r *Report) ReadJUnit(input io.Reader) error {
	dec := xml.NewDecoder(input)
	root := false
	depth := 0
	// Each suite says whether it isolated; the innermost one owns its cases.
	var suites []bool
	for {
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read JUnit XML: %w", err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				if root || (token.Name.Local != "testsuite" && token.Name.Local != "testsuites") {
					return fmt.Errorf("expected JUnit testsuite or testsuites root")
				}
				root = true
			}
			depth++
			if token.Name.Local == "testsuite" {
				isolated := false
				for _, attr := range token.Attr {
					if attr.Name.Local == "isolation" && attr.Value == "ran" {
						isolated = true
						r.IsolationRan = true
					}
				}
				suites = append(suites, isolated)
				continue
			}
			if token.Name.Local != "testcase" {
				continue
			}
			var test struct {
				Name     string `xml:"name,attr"`
				Class    string `xml:"classname,attr"`
				Time     string `xml:"time,attr"`
				Isolated string `xml:"isolated,attr"`
				Failures []struct {
					Message string `xml:"message,attr"`
					Text    string `xml:",chardata"`
				} `xml:"failure"`
				Errors []struct {
					Message string `xml:"message,attr"`
					Text    string `xml:",chardata"`
				} `xml:"error"`
			}
			if err := dec.DecodeElement(&test, &token); err != nil {
				return fmt.Errorf("read JUnit testcase: %w", err)
			}
			depth--
			if test.Name == "" {
				return fmt.Errorf("JUnit testcase has no name")
			}
			elapsed := 0.0
			if test.Time != "" {
				elapsed, err = strconv.ParseFloat(test.Time, 64)
				if err != nil {
					return fmt.Errorf("%s: invalid JUnit time %q", test.Name, test.Time)
				}
			}
			duration, err := seconds(elapsed)
			if err != nil {
				return fmt.Errorf("%s: %w", test.Name, err)
			}
			result := Test{Package: test.Class, Name: test.Name, Duration: duration}
			if test.Isolated == "true" {
				result.Isolated = true
			}
			if len(suites) > 0 && suites[len(suites)-1] {
				result.IsolationRan = true
			}
			r.Tests = append(r.Tests, result)
			for _, failure := range append(test.Failures, test.Errors...) {
				r.Failures = append(r.Failures, fmt.Sprintf("FAIL %s\n%s", result.ID(), strings.TrimSpace(failure.Message+"\n"+failure.Text)))
			}
		case xml.EndElement:
			depth--
			if token.Name.Local == "testsuite" && len(suites) > 0 {
				suites = suites[:len(suites)-1]
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(token)) != "" {
				return fmt.Errorf("unexpected text outside JUnit root")
			}
		}
	}
	if !root {
		return fmt.Errorf("JUnit XML input is empty")
	}
	return nil
}

// ReadAllow reads exact IDs. A comment starts at the beginning of a line,
// after two spaces, or after a tab. A single space before # stays literal.
func ReadAllow(input io.Reader) (map[string]bool, error) {
	allowed := make(map[string]bool)
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		name := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(name, "#") {
			continue
		}
		for i := 0; i < len(name); i++ {
			if name[i] == '#' && ((i >= 2 && name[i-2:i] == "  ") || (i >= 1 && name[i-1] == '\t')) {
				name = name[:i]
				break
			}
		}
		if name = strings.TrimSpace(name); name != "" {
			allowed[name] = true
		}
	}
	return allowed, scanner.Err()
}

// BudgetTests returns independent test durations. ReadGo has already removed
// child elapsed time from each parent's duration. JUnit case names stay literal.
func (r Report) BudgetTests() []Test {
	return append([]Test(nil), r.Tests...)
}

// Allowed checks the exact test ID.
func (t Test) Allowed(allowed map[string]bool) bool {
	return allowed[t.ID()]
}

// AllowWarnings identifies stale entries without hiding test failures.
func (r Report) AllowWarnings(allowed map[string]bool) []string {
	ids := map[string]bool{}
	for _, test := range r.Tests {
		ids[test.ID()] = true
	}
	var warnings []string
	for entry, enabled := range allowed {
		if enabled && !ids[entry] {
			warnings = append(warnings, fmt.Sprintf("allow entry %q matches no test", entry))
		}
	}
	sort.Strings(warnings)
	return warnings
}

// WriteTimings prints the failures and the slowest tests without judging the
// budget. The CI runner owns the room's test limit and compares every duration
// itself, so this report must not name a verdict of its own.
func (r Report) WriteTimings(output io.Writer) {
	tests := r.BudgetTests()
	sort.SliceStable(tests, func(i, j int) bool {
		if tests[i].Duration == tests[j].Duration {
			return tests[i].ID() < tests[j].ID()
		}
		return tests[i].Duration > tests[j].Duration
	})
	for _, failure := range r.Failures {
		fmt.Fprintln(output, failure)
	}
	fmt.Fprintln(output, "Slowest tests:")
	for i, test := range tests {
		if i == 10 {
			break
		}
		fmt.Fprintf(output, "  %s  %s\n", test.Duration, test.ID())
	}
	fmt.Fprintf(output, "%d tests; %d failed packages or cases\n", len(tests), len(r.Failures))
}

// Write prints failures and the slowest tests, and returns whether the check passes.
func (r Report) Write(output io.Writer, max time.Duration, allowed map[string]bool) bool {
	tests := r.BudgetTests()
	for _, warning := range r.AllowWarnings(allowed) {
		fmt.Fprintln(output, "WARNING:", warning)
	}
	sort.SliceStable(tests, func(i, j int) bool {
		if tests[i].Duration == tests[j].Duration {
			return tests[i].ID() < tests[j].ID()
		}
		return tests[i].Duration > tests[j].Duration
	})
	for _, failure := range r.Failures {
		fmt.Fprintln(output, failure)
	}
	violations := 0
	for _, test := range tests {
		if test.Duration > max && !test.Allowed(allowed) {
			fmt.Fprintf(output, "OVER BUDGET %s: %s (max %s)\n", test.ID(), test.Duration, max)
			violations++
		}
	}
	fmt.Fprintln(output, "Slowest tests:")
	for i, test := range tests {
		if i == 10 {
			break
		}
		suffix := ""
		if test.Allowed(allowed) {
			suffix = " (allowed)"
		}
		fmt.Fprintf(output, "  %s  %s%s\n", test.Duration, test.ID(), suffix)
	}
	fmt.Fprintf(output, "%d tests; %d over budget; %d failed packages or cases\n", len(tests), violations, len(r.Failures))
	return violations == 0 && len(r.Failures) == 0
}

// WriteJUnit writes test durations for artifact consumers. Test execution failures
// remain in the original report and in the CI command's exit status.
func (r Report) WriteJUnit(output io.Writer) error {
	type testCase struct {
		Name     string `xml:"name,attr"`
		Class    string `xml:"classname,attr,omitempty"`
		Time     string `xml:"time,attr"`
		Isolated string `xml:"isolated,attr,omitempty"`
	}
	suite := struct {
		XMLName     xml.Name   `xml:"testsuite"`
		Name        string     `xml:"name,attr"`
		IsolateRuns string     `xml:"isolation,attr,omitempty"`
		Tests       []testCase `xml:"testcase"`
	}{Name: "go test"}
	if r.IsolationRan {
		// A reader tells a confirmed time from a full-run one only when it
		// knows isolation ran; a report from a suite without it has neither.
		suite.IsolateRuns = "ran"
	}
	for _, test := range r.Tests {
		row := testCase{Name: test.Name, Class: test.Package, Time: strconv.FormatFloat(test.Duration.Seconds(), 'f', -1, 64)}
		if test.Isolated {
			row.Isolated = "true"
		}
		suite.Tests = append(suite.Tests, row)
	}
	encoder := xml.NewEncoder(output)
	encoder.Indent("", "  ")
	return encoder.Encode(suite)
}
