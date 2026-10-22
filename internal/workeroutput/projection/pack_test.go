package projection

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/workeroutput"
)

func TestConflictingOverlapCannotPublishPack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.jsonl")
	source := []byte("{\"v\":1,\"kind\":\"message\",\"text\":\"original\"}\n")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := NewPack("runs/wf_test/agents/1", path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Abort()
	lines := []Line{{Start: 0, End: int64(len(source)), Raw: source}}
	conversion := func(text string) func([]workeroutput.Event) []int {
		return func(events []workeroutput.Event) []int { events[0].Text = text; return []int{0} }
	}
	if err := p.Add(lines, conversion("first")); err != nil {
		t.Fatal(err)
	}
	if err := p.Add(lines, conversion("first")); err != nil {
		t.Fatalf("consistent repeat: %v", err)
	}
	if err := p.Add(lines, conversion("second")); err == nil {
		t.Fatal("conflicting overlap was accepted")
	}
	if _, err := p.Finish(int64(len(source)), int64(len(source))); err == nil {
		t.Fatal("failed pack could be finished")
	}
	if err := p.Abort(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "output.jsonl" {
		t.Fatalf("failed preparation published files: %v", entries)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(source, after) {
		t.Fatalf("source changed: %q, %v", after, err)
	}
}
