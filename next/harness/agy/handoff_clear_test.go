package agy

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// MarkCleared leaves every conversation file but the kept one behind, keeps what was already left, and touches nothing else.
func TestMarkClearedLeavesEveryOtherConversationFileBehind(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.jsonl", "agy-1.jsonl", "agy-2.jsonl", "note.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := MarkCleared(dir, "agy-2.jsonl"); err != nil {
		t.Fatal(err)
	}
	if got := ClearedFiles(dir); !slices.Equal(got, []string{"a.jsonl", "agy-1.jsonl"}) {
		t.Fatalf("cleared %v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.jsonl"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MarkCleared(dir, "b.jsonl"); err != nil {
		t.Fatal(err)
	}
	if got := ClearedFiles(dir); !slices.Equal(got, []string{"a.jsonl", "agy-1.jsonl", "agy-2.jsonl"}) {
		t.Fatalf("cleared %v", got)
	}
}
