package subagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

func TestPromptMigrationResumesWithoutChangingLiteralHashes(t *testing.T) {
	s, id := turnStore(t)
	db, err := s.turnsDB()
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	shared := "shared retained prompt"
	hash := func(text string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text))) }
	literal := hash(shared)
	texts := [][2]string{{shared, literal}, {shared, ""}, {literal, shared}, {shared, literal}}
	original := make([][]byte, len(texts))
	for i, text := range texts {
		n := i + 1
		raw, err := json.Marshal(map[string]any{
			"n": n, "prompt": text[0], "heldPrompt": text[1],
			"usageNormalization": map[string]int{"version": 1},
			"future":             map[string]any{"nested": []any{n, "keep"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		original[i] = raw
		if n == 4 {
			// A row already written by format 3 must keep its references.
			if _, err := saveTurnRow(ctx, db, id, n, raw); err != nil {
				t.Fatal(err)
			}
		} else if _, err := db.Exec("INSERT INTO session_turns(session,n,revision,turn,mirror) VALUES (?,?,?,?,?)", id, n, n, raw, "retained-copy"); err != nil {
			t.Fatal(err)
		}
	}
	cursor := func() int64 {
		t.Helper()
		var value int64
		if err := db.QueryRow("SELECT value FROM meta WHERE key=?", promptMigrationKey).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if more, err := migratePromptBatch(db, 1, 1<<20); err != nil || !more {
		t.Fatalf("first batch has more = %v, error = %v; want a committed partial batch", more, err)
	}
	if got := cursor(); got != 1 {
		t.Fatalf("first cursor = %d; want 1", got)
	}
	var untouched []byte
	if err := db.QueryRow("SELECT turn FROM session_turns WHERE session=? AND n=2", id).Scan(&untouched); err != nil || !bytes.Equal(untouched, original[1]) {
		t.Fatalf("first batch changed the next row: %s, %v", untouched, err)
	}
	// A byte bound below one row still permits that row and then commits.
	if more, err := migratePromptBatch(db, 250, 1); err != nil || !more {
		t.Fatalf("byte-bounded batch has more = %v, error = %v", more, err)
	}
	if got := cursor(); got != 2 {
		t.Fatalf("byte-bounded cursor = %d; want 2", got)
	}
	// Startup resumes from the durable cursor and reuses the retained importer.
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	if got := cursor(); got != 4 {
		t.Fatalf("completed cursor = %d; want 4", got)
	}
	storedRows := make([][]byte, len(texts))
	for i, text := range texts {
		n := i + 1
		stored, revision, err := storedTurn(ctx, db, id, n)
		if err != nil || revision != int64(n) {
			t.Fatalf("turn %d revision = %d, error = %v", n, revision, err)
		}
		storedRows[i] = stored
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(stored, &fields); err != nil {
			t.Fatal(err)
		}
		for j, name := range []string{"prompt", "heldPrompt"} {
			var got string
			if err := json.Unmarshal(fields[name], &got); err != nil {
				t.Fatal(err)
			}
			want := text[j]
			if want != "" {
				want = hash(want)
			}
			if got != want {
				t.Fatalf("stored turn %d %s = %q; want %q", n, name, got, want)
			}
		}
		resolved, _, err := s.readTurnRow(id, n)
		if err != nil || !bytes.Equal(resolved, original[i]) {
			t.Fatalf("resolved turn %d = %s, error = %v; want %s", n, resolved, err, original[i])
		}
	}
	for query, want := range map[string]int{
		"SELECT count(*) FROM prompt_blobs":                                 2,
		"SELECT count(*) FROM prompt_refs":                                  7,
		"SELECT count(*) FROM session_turns WHERE mirror = 'retained-copy'": 3,
	} {
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s = %d, error = %v; want %d", query, got, err, want)
		}
	}
	// A later format 3 turn must not restart the completed conversion.
	if _, err := saveTurnRow(ctx, db, id, 5, []byte(`{"n":5,"prompt":"new format 3 turn"}`)); err != nil {
		t.Fatal(err)
	}
	newRow, _, err := storedTurn(ctx, db, id, 5)
	if err != nil {
		t.Fatal(err)
	}
	storedRows = append(storedRows, newRow)
	dataDir := filepath.Dir(s.Root)
	if err := database.Close(dataDir); err != nil {
		t.Fatal(err)
	}
	db, err = database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	// Reopening clears BackUpOnce. Block backups to prove startup skips them.
	backupDir := filepath.Join(dataDir, "backups")
	if err := os.RemoveAll(backupDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupDir, []byte("backups blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	// total_changes is connection-local; keep all checks on one connection.
	db.SetMaxOpenConns(1)
	var before, after int64
	if err := db.QueryRow("SELECT total_changes()").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT total_changes()").Scan(&after); err != nil || before != after {
		t.Fatalf("completed migration changed storage: %d -> %d, %v", before, after, err)
	}
	if got := cursor(); got != 4 {
		t.Fatalf("completed migration advanced cursor to %d; want 4", got)
	}
	for i, want := range storedRows {
		got, revision, err := storedTurn(ctx, db, id, i+1)
		if err != nil || revision != int64(i+1) || !bytes.Equal(got, want) {
			t.Fatalf("completed migration changed turn %d: %s, %v", i+1, got, err)
		}
	}
}
