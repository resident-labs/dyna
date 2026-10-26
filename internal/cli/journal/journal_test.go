package journal

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/runstore"
)

func TestJournalCmdOutsideWorker(t *testing.T) {
	t.Setenv(runstore.AgentJournalEnv, "")
	cmd := NewCommand()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"Made progress"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "only available inside a dyna worker") {
		t.Fatalf("journal command error = %v", err)
	}
}

func TestJournalCmdAppendsQuietlyWithDefaults(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_cli-journal")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	run, err := runstore.Create("cli journal", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "null", nil) })
	path, err := run.StartAgentJournal(2, "cli worker", "terra", "", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(runstore.AgentJournalEnv, path)

	cmd := NewCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"Mapped the storage contract", "--next", "Run the tests"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("journal command error = %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("journal command output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	entries, _, err := runstore.ReadAgentJournalPathFrom(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[1].Kind != "update" || entries[1].Message != "Mapped the storage contract" || entries[1].Next != "Run the tests" || entries[1].Source != "agent" {
		t.Fatalf("journal entries = %#v", entries)
	}
}

func sessionGoals(ids []string, notes *[]string) Goals {
	return Goals{
		InSession: func() bool { return true },
		OpenHeld:  func() ([]string, error) { return ids, nil },
		Note: func(_ context.Context, id, text string) error {
			*notes = append(*notes, id+"|"+text)
			return nil
		},
	}
}

func TestJournalSessionWithOneGoalNotesIt(t *testing.T) {
	t.Setenv(runstore.AgentJournalEnv, "")
	var notes []string
	cmd := Command(sessionGoals([]string{"g7"}, &notes))
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"Found the cause", "--kind", "finding", "--next", "Write the fix"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := "g7|finding: Found the cause\nNext: Write the fix"; len(notes) != 1 || notes[0] != want {
		t.Fatalf("notes = %q; want %q", notes, want)
	}
}

func TestJournalSessionWithoutOneGoalNamesGoalNote(t *testing.T) {
	t.Setenv(runstore.AgentJournalEnv, "")
	for _, ids := range [][]string{nil, {"g1", "g2"}} {
		var notes []string
		cmd := Command(sessionGoals(ids, &notes))
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs([]string{"Progress"})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), `dyna goal note <goal> "<text>"`) || len(notes) != 0 {
			t.Fatalf("%d goals: error = %v, notes = %q", len(ids), err, notes)
		}
	}
}

func TestJournalRejectsGoalFlags(t *testing.T) {
	for _, flag := range []string{"--goal", "--until"} {
		cmd := NewCommand()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs([]string{"Waiting", flag, "value"})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("%s error = %v", flag, err)
		}
	}
}
