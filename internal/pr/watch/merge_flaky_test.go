package watch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

// A flaky publish takes only failures that root named. Any other failing test
// keeps the batch frozen.
func TestFlakyPublishRefusesAnUnnamedFailure(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2)
	failTrial(t, f, "--- FAIL: TestFlaky (0.02s)\n--- FAIL: TestBroken (0.01s)\nFAIL\tdyna-agent/internal/x\t0.3s\n")
	err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "publish", []string{"1"}, nil, []string{"TestFlaky: races on the temp dir"})
	if err == nil || !strings.Contains(err.Error(), "TestBroken") {
		t.Fatalf("flaky publish error = %v", err)
	}
	if len(*f.merged) != 0 {
		t.Fatalf("published with an unnamed failure: %v", *f.merged)
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if b := st.MergeTrials["github.com/owner/repo"]; b == nil || b.Phase != "awaiting-root" {
		t.Fatalf("trial = %+v", b)
	}
}

// A flaky publish merges the batch, clears the batch pause, and files one room
// issue for each named test.
func TestFlakyPublishMergesAndFilesTheFix(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	failTrial(t, f, "--- FAIL: TestFlaky (0.02s)\nFAIL\tdyna-agent/internal/x\t0.3s\n")
	if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "publish", []string{"1"}, nil, []string{"TestFlaky: races on the shared temp dir"}); err != nil {
		t.Fatalf("flaky publish: %v", err)
	}
	if len(*f.merged) != 1 {
		t.Fatalf("merged = %v", *f.merged)
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.MergePauses[f.e.author.RoomID] != "" {
		t.Fatalf("flaky publish kept the batch pause: %v", st.MergePauses)
	}
	issues, err := f.e.s.Notify.Rooms.Issues(f.e.author.RoomID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Title != "Flaky test: TestFlaky" || !strings.Contains(issues[0].Filer, ":root") {
		t.Fatalf("issues = %+v", issues)
	}
	history, err := f.e.s.RoomMergeHistory(f.e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range history {
		if entry.Kind == "decision" && strings.Contains(entry.Detail, "TestFlaky") {
			found = true
		}
	}
	if !found {
		t.Fatalf("history = %+v", history)
	}
}

// failTrial runs the saved batch to a failed trial whose logs hold logs.
func failTrial(t *testing.T, f *trialFixture, logs string) {
	t.Helper()
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	run := f.e.s.GH.run
	f.e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if strings.Contains(strings.Join(cmd.Args, " "), "run view") {
			if _, err := fmt.Fprint(cmd.Stdout, logs); err != nil {
				return err
			}
			return nil
		}
		return run(ctx, cmd)
	}
	f.ci, f.conclusion = "completed", "failure"
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	b := st.MergeTrials["github.com/owner/repo"]
	if b == nil || b.Phase != "awaiting-root" || !strings.Contains(b.Logs, "TestFlaky") {
		t.Fatalf("trial = %+v", b)
	}
	if failed := mergeNotices(f.e.sent, room.MergeFailed); len(failed) != 1 || !failed[0].NeedsRoot {
		t.Fatalf("notices = %+v", f.e.sent)
	}
}

func TestFlakyCoverageNamesEveryFailure(t *testing.T) {
	logs := "--- FAIL: TestA (0.01s)\n--- FAIL: TestB/sub (0.01s)\nFAIL\tdyna-agent/internal/x\t0.3s\n"
	for _, tc := range []struct {
		name  string
		flaky []FlakyTest
		ok    bool
	}{
		{"both named", []FlakyTest{{Name: "TestA", Reason: "timer"}, {Name: "TestB", Reason: "port"}}, true},
		{"one unnamed", []FlakyTest{{Name: "TestA", Reason: "timer"}}, false},
		{"a test that did not fail", []FlakyTest{{Name: "TestA", Reason: "timer"}, {Name: "TestB", Reason: "port"}, {Name: "TestC", Reason: "why"}}, false},
		{"no failing test", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := flakyCoverage(logs, tc.flaky); (err == nil) != tc.ok {
				t.Fatalf("coverage = %v", err)
			}
		})
	}
	if err := flakyCoverage("FAIL\tdyna-agent/internal/x\t[build failed]\n", []FlakyTest{{Name: "TestA", Reason: "timer"}}); err == nil {
		t.Fatal("a build failure looked like a flake")
	}
	if _, err := ParseFlakyTests([]string{"TestA"}); err == nil {
		t.Fatal("a flaky entry without a reason was accepted")
	}
	if _, err := ParseFlakyTests([]string{"TestA: "}); err == nil {
		t.Fatal("a flaky entry without a test name was accepted")
	}
	tests, err := ParseFlakyTests([]string{" TestA: races "})
	if err != nil || tests[0].Name != "TestA" || tests[0].Reason != "races" {
		t.Fatalf("parsed = %+v, %v", tests, err)
	}
}
