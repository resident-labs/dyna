package engine

import (
	"fmt"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestWorkflowLiveAndFinishedProgress(t *testing.T) {
	w := setupWorkflow(t)
	first, crew := w.start(contract.Principal{Kind: contract.PrincipalSession, Session: w.root}, workflowScript)
	turn := w.turn(crew)
	w.step(crew, "alpha-key", contract.WorkflowStepRunning, "")
	_, e := w.WorkflowStepTranscript(w.ctx, crew, contract.WorkflowStepTranscriptRequest{Key: "alpha-key", Transcript: "worker.jsonl"})
	if e != nil {
		t.Fatal(e)
	}
	data := fmt.Sprintf("{\"type\":\"message\",\"id\":\"u1\",\"timestamp\":\"2026-11-14T01:00:00Z\",\"message\":{\"role\":\"user\",\"deliveryId\":\"%d\",\"content\":[{\"type\":\"text\",\"text\":\"work\"}]}}\n", turn.Inputs[0].Delivery)
	data += "{\"type\":\"message\",\"id\":\"a1\",\"timestamp\":\"2026-11-14T01:00:01Z\",\"message\":{\"role\":\"assistant\",\"usage\":{\"input\":12,\"output\":5},\"content\":[{\"type\":\"toolCall\",\"id\":\"c1\",\"name\":\"bash\",\"arguments\":{}}]}}\n"
	if _, e := appendTranscript(w.Service, w.ctx, crew, "worker.jsonl", 0, data); e != nil {
		t.Fatal(e)
	}
	status, e := w.WorkflowStatus(w.ctx, w.owner, contract.WorkflowStatusRequest{Run: first.Run})
	if e != nil {
		t.Fatal(e)
	}
	step := status.Steps[0]
	if step.State != contract.WorkflowStepRunning || step.ToolCalls != 1 || step.Usage.InputTokens != 12 || step.LastActivity.IsZero() {
		t.Fatalf("%+v", step)
	}
	if _, e := w.WorkflowStepEnd(w.ctx, crew, contract.WorkflowStepEndRequest{Key: "alpha-key", State: contract.WorkflowStepDone, Transcript: "worker.jsonl", Usage: contract.Usage{InputTokens: 20, OutputTokens: 8}, Result: "null"}); e != nil {
		t.Fatal(e)
	}
	status, e = w.WorkflowStatus(w.ctx, w.owner, contract.WorkflowStatusRequest{Run: first.Run})
	if e != nil || status.Steps[0].ToolCalls != 1 || status.Steps[0].Usage.InputTokens != 20 || status.Steps[0].Usage.OutputTokens != 8 {
		t.Fatalf("finished: %+v %v", status, e)
	}
}
