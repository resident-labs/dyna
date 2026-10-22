package transcript

import (
	"testing"

	"dyna-agent/internal/workeroutput"
)

func TestMCPTextEnvelopeKeepsShellJobLink(t *testing.T) {
	const jobID = "sh_20261022-120000-1234abcd"
	for _, output := range []string{
		"Job " + jobID + ". Exit code 0.\nfinished",
		`{"content":[{"type":"image","data":"ignored"},{"type":"text","text":"Job ` + jobID + `. Exit code 0.\nfinished"},{"type":"text","text":"other"}]}`,
	} {
		got := Summarize(workeroutput.Event{Name: "mcp__dyna__shell", State: "completed", Input: `{"command":"true"}`, Output: output})
		if got.JobID != jobID || got.JobOp != "run" || got.Output != output {
			t.Fatalf("summary = %+v; want job %s, run, and unchanged output %q", got, jobID, output)
		}
	}
}
