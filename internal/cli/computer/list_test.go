package computer

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/computers"
)

func listOutput(t *testing.T, pending func() (time.Time, bool)) string {
	t.Helper()
	original := restartPendingSince
	t.Cleanup(func() { restartPendingSince = original })
	restartPendingSince = pending
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	rows := []computers.Status{{SessionID: "ses_a", Room: "dyna", State: computers.StateRunning}}
	if err := printComputerList(cmd, rows, false, false); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// The list says when a deferred restart became pending, so the owner sees why
// the service restarts on its own.
func TestComputerListShowsAPendingRestart(t *testing.T) {
	at := time.Date(2026, 10, 26, 11, 38, 0, 0, time.Local)
	got := listOutput(t, func() (time.Time, bool) { return at, true })
	if !strings.Contains(got, "restart pending since 2026-10-26 11:38") {
		t.Fatalf("list lacks the pending restart:\n%s", got)
	}
	if none := listOutput(t, func() (time.Time, bool) { return time.Time{}, false }); strings.Contains(none, "restart pending") {
		t.Fatalf("list shows a restart that is not pending:\n%s", none)
	}
}
