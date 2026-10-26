package workflows

import (
	"bytes"
	"strings"
	"testing"

	"dyna-agent/internal/runstore"
)

// A run() command prints its own steps, one short line per event, with the
// command line, duration and exit code; it never prints nothing.
func TestPrintEventCommandSteps(t *testing.T) {
	exit := 2
	var b bytes.Buffer
	for _, e := range []runstore.Event{
		{T: "exec_start", ID: 1, Label: "make", Command: "make test"},
		{T: "exec_run", ID: 1, Label: "make"},
		{T: "exec_end", ID: 1, Label: "make", Status: "ok", DurMs: 1600, ExitCode: &exit},
		{T: "exec_end", ID: 2, Label: "up", Status: "error", Error: "exit status 2"},
	} {
		printEvent(&b, e)
	}
	out := b.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("printEvent wrote %d lines, want one per event:\n%s", len(lines), out)
	}
	for _, want := range []string{"make", "$ make test", "2s", "exit 2", "exit status 2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("progress lines lack %q:\n%s", want, out)
		}
	}
	if !strings.Contains(lines[1], "make") {
		t.Fatalf("the exec_run line does not name the command: %q", lines[1])
	}
}
