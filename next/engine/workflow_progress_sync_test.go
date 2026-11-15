package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestWorkflowProgressPartialReplayAndResync(t *testing.T) {
	w := setupWorkflow(t)
	started, crew := w.start(w.owner, workflowScript)
	w.turn(crew)
	w.step(crew, "worker", contract.WorkflowStepRunning, "")
	line := `{"type":"message","timestamp":"2026-11-14T01:00:01Z","message":{"role":"assistant","usage":{"input":12,"output":5,"cacheRead":2,"cacheWrite":3},"content":[{"type":"toolCall","id":"c1","name":"bash","arguments":{}}]}}` + "\n"
	// Transcript data can arrive before the worker registers its filename.
	cut := len(line) / 2
	if _, e := appendTranscript(w.Service, w.ctx, crew, "worker.jsonl", 0, line[:cut]); e != nil {
		t.Fatal(e)
	}
	if _, e := w.WorkflowStepTranscript(w.ctx, crew, contract.WorkflowStepTranscriptRequest{Key: "worker", Transcript: "worker.jsonl"}); e != nil {
		t.Fatal(e)
	}
	p, err := w.store.WorkflowProgress(w.ctx, crew.Session, "worker.jsonl")
	if err != nil || p.Offset != 0 || p.ToolCalls != 0 {
		t.Fatalf("partial: %+v %v", p, err)
	}
	if _, e := appendTranscript(w.Service, w.ctx, crew, "worker.jsonl", int64(cut), line[cut:]); e != nil {
		t.Fatal(e)
	}
	// Overlapping retransmission and a full replay both leave the totals unchanged.
	if _, e := appendTranscript(w.Service, w.ctx, crew, "worker.jsonl", int64(cut), line[cut:]); e != nil {
		t.Fatal(e)
	}
	if _, e := appendTranscript(w.Service, w.ctx, crew, "worker.jsonl", 0, line); e != nil {
		t.Fatal(e)
	}
	p, err = w.store.WorkflowProgress(w.ctx, crew.Session, "worker.jsonl")
	if err != nil || p.Offset != int64(len(line)) || p.ToolCalls != 1 || p.Usage.InputTokens != 12 {
		t.Fatalf("replay: %+v %v", p, err)
	}
	name := filepath.Join(w.layout.Transcripts(w.room, crew.Session), "worker.jsonl")
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	// Status is a database snapshot, even while the engine's file is missing.
	status, e := w.WorkflowStatus(w.ctx, w.owner, contract.WorkflowStatusRequest{Run: started.Run})
	if e != nil || status.Steps[0].ToolCalls != 1 {
		t.Fatalf("missing file: %+v %v", status, e)
	}
	if response, e := appendTranscript(w.Service, w.ctx, crew, "worker.jsonl", int64(len(line)), line); e != nil || response.AckedOffset != 0 {
		t.Fatalf("rewind: %+v %v", response, e)
	}
	// Replay the missing copy in chunks, including an incomplete trailing record.
	if _, e := appendTranscript(w.Service, w.ctx, crew, "worker.jsonl", 0, line[:cut]); e != nil {
		t.Fatal(e)
	}
	if _, e := appendTranscript(w.Service, w.ctx, crew, "worker.jsonl", int64(cut), line[cut:]+line[:cut]); e != nil {
		t.Fatal(e)
	}
	p, err = w.store.WorkflowProgress(w.ctx, crew.Session, "worker.jsonl")
	if err != nil || p.ToolCalls != 1 || p.Offset != int64(len(line)) {
		t.Fatalf("resync partial: %+v %v", p, err)
	}
	if _, e := appendTranscript(w.Service, w.ctx, crew, "worker.jsonl", int64(len(line)+cut), line[cut:]); e != nil {
		t.Fatal(e)
	}
	p, err = w.store.WorkflowProgress(w.ctx, crew.Session, "worker.jsonl")
	if err != nil || p.ToolCalls != 2 || p.Offset != int64(2*len(line)) || p.Usage.InputTokens != 24 || p.Usage.OutputTokens != 10 || p.Usage.CacheReadTokens != 4 || p.Usage.CacheWriteTokens != 6 {
		t.Fatalf("resync extended: %+v %v", p, err)
	}
}

func TestWorkflowProgressRegistrationAfterSync(t *testing.T) {
	w := setupWorkflow(t)
	_, crew := w.start(w.owner, workflowScript)
	w.turn(crew)
	w.step(crew, "worker", contract.WorkflowStepRunning, "")
	line := `{"type":"message","timestamp":"2026-11-14T01:00:02Z","message":{"role":"toolResult","nestedCalls":{"calls":[{"id":"outer/1"},{"id":"outer/2"}],"complete":true}}}` + "\n"
	if _, e := appendTranscript(w.Service, w.ctx, crew, "worker.jsonl", 0, line); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if _, e := w.WorkflowStepTranscript(w.ctx, crew, contract.WorkflowStepTranscriptRequest{Key: "worker", Transcript: "worker.jsonl"}); e != nil {
			t.Fatal(e)
		}
	}
	p, err := w.store.WorkflowProgress(w.ctx, crew.Session, "worker.jsonl")
	if err != nil || p.ToolCalls != 2 || p.Offset != int64(len(line)) {
		t.Fatalf("late registration: %+v %v", p, err)
	}
}
