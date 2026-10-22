// Package ci exposes test duration checks for continuous integration.
package ci

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/boundedio"
	"dyna-agent/internal/cibudget"
	"github.com/spf13/cobra"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "ci", Short: "Check continuous integration reports"}
	var junitOutput string
	var junit []string
	var isolateOver time.Duration
	var isolateRace bool
	var testFilesPath string
	tests := &cobra.Command{
		Use:   "tests [go-json-file|-]",
		Short: "Check test failures and report test durations",
		Long: "Read go test -json from stdin or a file, or JUnit XML from --junit globs.\n" +
			"Test failures fail the check. Durations are reported only; the room's test\n" +
			"budget is enforced by the PR watcher on the uploaded JUnit reports.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var report cibudget.Report
			if len(args) > 0 || len(junit) == 0 {
				if len(args) == 0 || args[0] == "-" {
					if err := report.ReadGo(c.InOrStdin()); err != nil {
						return err
					}
				} else {
					file, err := os.Open(args[0])
					if err != nil {
						return err
					}
					err = report.ReadGo(file)
					file.Close()
					if err != nil {
						return fmt.Errorf("%s: %w", args[0], err)
					}
				}
			}
			seen := make(map[string]bool)
			for _, pattern := range junit {
				matches, err := filepath.Glob(pattern)
				if err != nil {
					return fmt.Errorf("JUnit glob %q: %w", pattern, err)
				}
				if len(matches) == 0 {
					return fmt.Errorf("JUnit glob %q matched no files", pattern)
				}
				for _, path := range matches {
					if seen[path] {
						continue
					}
					seen[path] = true
					file, err := os.Open(path)
					if err != nil {
						return err
					}
					err = report.ReadJUnit(file)
					file.Close()
					if err != nil {
						return fmt.Errorf("%s: %w", path, err)
					}
				}
			}
			if isolateOver > 0 {
				var testFiles map[string][]string
				if testFilesPath != "" {
					var err error
					if testFiles, err = readTestFiles(testFilesPath); err != nil {
						return err
					}
				}
				ctx, cancel := context.WithTimeout(c.Context(), isolateBudget)
				defer cancel()
				report.Isolate(ctx, isolateOver, func(ctx context.Context, test cibudget.Test) (time.Duration, error) {
					return testAlone(ctx, test, isolateRace, testFiles)
				}, c.OutOrStdout())
			}
			if junitOutput != "" {
				file, err := os.Create(junitOutput)
				if err != nil {
					return err
				}
				err = report.WriteJUnit(file)
				closeErr := file.Close()
				if err != nil {
					return err
				}
				if closeErr != nil {
					return closeErr
				}
			}
			if !report.Write(c.OutOrStdout(), time.Duration(math.MaxInt64), nil) {
				return fmt.Errorf("test budget check failed")
			}
			if len(report.Tests) == 0 {
				return fmt.Errorf("report contains no completed tests")
			}
			return nil
		},
	}
	tests.Flags().StringVar(&junitOutput, "junit-output", "", "write parsed test durations as a JUnit XML artifact")
	tests.Flags().StringArrayVar(&junit, "junit", nil, "JUnit XML file glob (repeatable)")
	tests.Flags().DurationVar(&isolateOver, "isolate-over", 0, "run each Go or web test slower than this alone, and report its isolated time instead")
	tests.Flags().StringVar(&testFilesPath, "test-files", "", "JSON lines of test title and file from the web test reporter, used to re-run web tests alone")
	tests.Flags().BoolVar(&isolateRace, "isolate-race", false, "run isolated tests with -race, like the full run")
	cmd.AddCommand(tests)
	cmd.AddCommand(localCommands()...)
	return cmd
}

// Isolated runs share one deadline, and each run has its own, so a test that
// blocks only when alone cannot hold the job past the room's CI budget.
const (
	isolateBudget  = 30 * time.Second
	isolateTimeout = 30 * time.Second
)

// nodeClass is the classname of every case in the JUnit report of node's test runner.
const nodeClass = "test"

// testAlone runs one test by itself: a web test when it comes from node's test
// runner, a Go test otherwise.
func testAlone(ctx context.Context, test cibudget.Test, race bool, testFiles map[string][]string) (time.Duration, error) {
	if test.Package == nodeClass {
		return nodeTestAlone(ctx, test, testFiles)
	}
	return goTestAlone(ctx, test, race)
}

var webTestFile = regexp.MustCompile(`\.test\.(js|mjs|ts)$`)

// readTestFiles reads the JSON lines of web/scripts/test-files-reporter.ts into
// a map from test title to the test files that hold it. Node 22's JUnit report
// does not name the file.
func readTestFiles(path string) (map[string][]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	files := map[string][]string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		var line struct{ Name, File string }
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if !slices.Contains(files[line.Name], line.File) {
			files[line.Name] = append(files[line.Name], line.File)
		}
	}
	return files, scanner.Err()
}

// nodeTestAlone runs one web test from the current folder, which is web/. A case
// named like its file is the whole file, so it runs without a name pattern.
func nodeTestAlone(ctx context.Context, test cibudget.Test, testFiles map[string][]string) (time.Duration, error) {
	files := testFiles[test.Name]
	if webTestFile.MatchString(test.Name) {
		files = []string{test.Name}
	}
	if len(files) == 0 {
		return 0, fmt.Errorf("the test reporter did not record a file for this test")
	}
	ctx, cancel := context.WithTimeout(ctx, isolateTimeout)
	defer cancel()
	args := []string{"--test", "--test-reporter=junit"}
	if !webTestFile.MatchString(test.Name) {
		args = append(args, "--test-name-pattern=^"+regexp.QuoteMeta(test.Name)+"$")
	}
	stdout, err := exec.CommandContext(ctx, "node", append(args, files...)...).Output()
	var alone cibudget.Report
	if readErr := alone.ReadJUnit(strings.NewReader(string(stdout))); readErr != nil {
		return 0, readErr
	}
	if err != nil {
		return 0, err
	}
	for _, found := range alone.Tests {
		if found.Name == test.Name {
			return found.Duration, nil
		}
	}
	return 0, fmt.Errorf("test did not run alone")
}

// goTestAlone runs one Go test or subtest by itself, from the current module.
func goTestAlone(ctx context.Context, test cibudget.Test, race bool) (time.Duration, error) {
	parts := strings.Split(test.Name, "/")
	for i, part := range parts {
		parts[i] = "^" + regexp.QuoteMeta(part) + "$"
	}
	args := []string{"test", "-json", "-count=1", "-timeout", isolateTimeout.String(), "-run", strings.Join(parts, "/")}
	if race {
		args = append(args, "-race")
	}
	command := exec.CommandContext(ctx, "go", append(args, test.Package)...)
	stderr := boundedio.NewTail(4096)
	command.Stderr = stderr
	stdout, err := command.Output()
	var alone cibudget.Report
	readErr := alone.ReadGo(strings.NewReader(string(stdout)))
	if err != nil {
		err = errors.Join(err, ctx.Err(), readErr)
		detail := boundedio.NewTail(4096)
		for _, failure := range alone.Failures {
			fmt.Fprintln(detail, failure)
		}
		// An interrupted process may not emit a final failure event. Keep a
		// bounded tail of its output when the parser has no failure to render.
		if detail.Len() == 0 {
			detail.Write(stdout)
		}
		if detail.Truncated() {
			err = fmt.Errorf("%w\n[earlier stdout omitted]", err)
		}
		if detail.Len() > 0 {
			err = fmt.Errorf("%w\n%s", err, strings.TrimSpace(strings.ToValidUTF8(string(detail.Bytes()), "")))
		}
		if stderr.Truncated() {
			err = fmt.Errorf("%w\n[earlier stderr omitted]", err)
		}
		if stderr.Len() > 0 {
			err = fmt.Errorf("%w\nstderr:\n%s", err, strings.TrimSpace(strings.ToValidUTF8(string(stderr.Bytes()), "")))
		}
		return 0, err
	}
	if readErr != nil {
		return 0, readErr
	}
	for _, found := range alone.Tests {
		if found.Name == test.Name {
			return found.Duration, nil
		}
	}
	return 0, fmt.Errorf("test did not run alone")
}
