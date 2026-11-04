package agent

import (
	"os"
	"strings"
	"testing"
)

// A crew worker's prompt is the core box.hello sends, then its role as a workflow worker, in the unified prompt's rendering.
func TestACrewWorkersPromptIsTheCoreAndItsRole(t *testing.T) {
	rendering, err := os.ReadFile("testdata/render-worker.txt")
	if err != nil {
		t.Fatal(err)
	}
	core, _, _ := strings.Cut(string(rendering), "\n\n# Your role: workflow worker")
	if got := crewInstructions(core, "sweep", "/box/work/sweep/w3"); got != string(rendering) {
		t.Fatalf("prompt:\n%s\nwant:\n%s", got, rendering)
	}
}
