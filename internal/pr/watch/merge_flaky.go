package watch

import (
	"context"
	"fmt"
	"strings"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
)

// FlakyTest names one failing test and why root treats it as a flake.
type FlakyTest struct {
	Name   string
	Reason string
}

// flakyPublish carries the tests root accepted for one failed trial.
type flakyPublishKey struct{}

type flakyPublish struct{ tests []FlakyTest }

func withFlakyPublish(ctx context.Context, tests []FlakyTest) context.Context {
	return context.WithValue(ctx, flakyPublishKey{}, flakyPublish{tests: tests})
}

func flakyPublishFor(ctx context.Context) []FlakyTest {
	granted, _ := ctx.Value(flakyPublishKey{}).(flakyPublish)
	return granted.tests
}

// ParseFlakyTests reads the "<test name>: <reason>" entries of --flaky.
func ParseFlakyTests(specs []string) ([]FlakyTest, error) {
	out := make([]FlakyTest, 0, len(specs))
	for _, spec := range specs {
		name, reason, ok := strings.Cut(spec, ": ")
		name, reason = strings.TrimSpace(name), strings.TrimSpace(reason)
		if !ok || name == "" || reason == "" {
			return nil, fmt.Errorf("--flaky needs \"<test name>: <reason>\", got %q", spec)
		}
		out = append(out, FlakyTest{Name: name, Reason: reason})
	}
	return out, nil
}

// failingTest is one test the trial logs report as failed. A frontend line is
// matched loosely because it carries a suite path, not just a test name.
type failingTest struct {
	name  string
	loose bool
}

// flakyPublicationAllowed reports whether root named every failure in logs as
// flaky for this one publication.
func flakyPublicationAllowed(ctx context.Context, logs string) bool {
	granted := flakyPublishFor(ctx)
	return len(granted) > 0 && flakyCoverage(logs, granted) == nil
}

// flakyCoverage refuses unless root named every failing test in logs.
func flakyCoverage(logs string, flaky []FlakyTest) error {
	packages, tests, frontend, _ := mergeFailureLines(logs)
	var failing []failingTest
	for _, test := range tests {
		failing = append(failing, failingTest{name: test})
	}
	for _, line := range frontend {
		failing = append(failing, failingTest{name: line, loose: true})
	}
	if len(failing) == 0 {
		return fmt.Errorf("the saved trial logs name no failing test; return the PR or retry instead")
	}
	if strings.Contains(logs, "[build failed]") {
		return fmt.Errorf("the trial has a build failure, which is not a flaky test; return the PR")
	}
	if len(packages) > 0 && len(tests) == 0 {
		return fmt.Errorf("a package failed without a failing test; return the PR")
	}
	for _, test := range flaky {
		if !namesAny(test.Name, failing) {
			return fmt.Errorf("%q is not a failing test in this trial", test.Name)
		}
	}
	for _, got := range failing {
		named := false
		for _, test := range flaky {
			if testNames(test.Name, got) {
				named = true
				break
			}
		}
		if !named {
			return fmt.Errorf("%q failed and is not named in --flaky; fix it or return the PR", boundedTestName(got.name))
		}
	}
	return nil
}

func namesAny(name string, failing []failingTest) bool {
	for _, got := range failing {
		if testNames(name, got) {
			return true
		}
	}
	return false
}

func testNames(name string, got failingTest) bool {
	if got.loose {
		return strings.Contains(got.name, name)
	}
	return got.name == name || strings.HasPrefix(got.name, name+"/")
}

// finishFlakyPublish records a flaky decision after the trial published: it
// clears the batch pause, adds one merge-history line, and files one issue for
// each named test so the flake is fixed separately.
func (s *Service) finishFlakyPublish(ctx context.Context, roomID, repo string, b state.MergeTrial, flaky []FlakyTest) error {
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	if current := st.MergeTrials[repo]; current != nil && current.Branch == b.Branch && current.Phase != "published" {
		return fmt.Errorf("trial %s did not publish; it is %s: %s", b.Branch, current.Phase, current.Reason)
	}
	if err := s.Store.update(func(st *State) error {
		if st.MergePauses[roomID] == mergeBatchFailedReason {
			delete(st.MergePauses, roomID)
		}
		return nil
	}); err != nil {
		return err
	}
	db, err := s.mergeHistoryDB()
	if err != nil {
		return err
	}
	if err := s.saveMergeHistory(db, MergeHistoryEntry{
		Kind: "decision", Outcome: "recorded", Repo: repo, Branch: b.Branch, Head: b.Head,
		Changes: []string{}, Detail: flakyDecision(b, flaky), At: s.now(),
	}, []string{roomID}); err != nil {
		return err
	}
	rm, err := s.Notify.Rooms.Resolve(roomID)
	if err != nil {
		return err
	}
	for _, test := range flaky {
		_, err := s.Notify.Rooms.FileIssue(roomID, "Flaky test: "+test.Name, flakyIssueNote(b, test), "", 0, rm.Name+":"+room.RootAgent)
		if err != nil {
			return fmt.Errorf("the trial published, but its flaky issue could not be filed: %w", err)
		}
	}
	return nil
}

func flakyDecision(b state.MergeTrial, flaky []FlakyTest) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Published failed trial %s at %s because every failing test is flaky:", b.Branch, b.Head)
	for _, test := range flaky {
		fmt.Fprintf(&out, " %s (%s);", test.Name, test.Reason)
	}
	text := strings.TrimSuffix(out.String(), ";")
	return text + "."
}

func flakyIssueNote(b state.MergeTrial, test FlakyTest) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Batch trial %s failed at %s, and root published it because this test is flaky.\n", b.Branch, b.Head)
	fmt.Fprintf(&out, "Reason: %s\n", test.Reason)
	for _, line := range failingLines(b.Logs, test.Name) {
		out.WriteString(line + "\n")
	}
	out.WriteString("Fix the flake on its own; the batch did not wait for it.")
	return out.String()
}

// failingLines keeps the saved log lines that name one test.
func failingLines(logs, name string) []string {
	var out []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.TrimSpace(line) == "" || !strings.Contains(line, name) {
			continue
		}
		if len(out) == 5 {
			break
		}
		out = append(out, boundedTestName(line))
	}
	return out
}
