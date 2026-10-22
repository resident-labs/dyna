package projection

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/workeroutput"
)

func TestConflictingOverlapCannotPublishPack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.jsonl")
	first := []byte("{\"v\":1,\"kind\":\"message\",\"text\":\"original\"}\n")
	unchanged := []byte("{\"v\":1,\"kind\":\"status\"}\n")
	source := append(append([]byte(nil), first...), unchanged...)
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := NewPack("runs/wf_test/agents/1", path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Abort()
	lines := []Line{{Start: 0, End: int64(len(first)), Raw: first}}
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

	// A failed preparation must leave the artifact usable for a fresh retry.
	retry, err := NewPack("runs/wf_test/agents/1", path)
	if err != nil {
		t.Fatal(err)
	}
	defer retry.Abort()
	lines = append(lines, Line{Start: int64(len(first)), End: int64(len(source)), Raw: unchanged})
	if err := retry.Add(lines, conversion("first")); err != nil {
		t.Fatal(err)
	}
	prepared, err := retry.Finish(int64(len(source)), int64(len(source)))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	canonical := []byte("{\"v\":1,\"kind\":\"message\",\"text\":\"first\"}\n")
	blob, err := os.ReadFile(filepath.Join(dir, prepared.Pack))
	if err != nil || !bytes.Equal(blob, canonical) {
		t.Fatalf("published bytes = %q, %v", blob, err)
	}
	var rows []Row
	if err := prepared.EachRow(func(row Row) error { rows = append(rows, row); return nil }); err != nil {
		t.Fatal(err)
	}
	want := Row{Start: 0, End: int64(len(first)), SourceDigest: sha256.Sum256(first), CanonicalDigest: sha256.Sum256(canonical), CanonicalStart: 0, CanonicalEnd: int64(len(canonical))}
	if len(rows) != 1 || rows[0] != want {
		t.Fatalf("retry mappings = %#v; want %#v", rows, want)
	}
	after, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(source, after) {
		t.Fatalf("successful retry changed source: %q, %v", after, err)
	}
}
