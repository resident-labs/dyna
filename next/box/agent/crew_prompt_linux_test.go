package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// A crew worker's prompt is the core box.hello sends as WorkerInstructions, then its role as a workflow worker, in the unified prompt's rendering.
// The session's own Instructions, which hold its identity and root's host paths, never reach a worker.
func TestACrewWorkersPromptIsTheCoreAndItsRole(t *testing.T) {
	rendering, err := os.ReadFile("testdata/render-worker.txt")
	if err != nil {
		t.Fatal(err)
	}
	core, _, _ := strings.Cut(string(rendering), "\n\n# Your role: workflow worker")
	hello := contract.BoxHelloResponse{Instructions: "You are root of room r. The session's own prompt.", WorkerInstructions: core, Workflow: &contract.BoxWorkflow{Name: "sweep"}}
	if got := crewInstructions(hello, "/box/work/sweep/w3"); got != string(rendering) {
		t.Fatalf("prompt:\n%s\nwant:\n%s", got, rendering)
	}
}
