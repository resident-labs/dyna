package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/subagent"
)

func TestHistoricalAnchorMigrationRefusesChangedTurn(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { database.Close(root) })
	id := "ses_20261021-060000-01234567"
	dir := filepath.Join(root, "sessions", id)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	output := []byte(`{"v":1,"kind":"message","role":"assistant","text":"After steering.","ts":2000}` + "\n")
	outputPath := filepath.Join(dir, "agents", "1", "output.jsonl")
	if err := os.MkdirAll(filepath.Dir(outputPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, output, 0600); err != nil {
		t.Fatal(err)
	}
	turn := subagent.Turn{N: 1, Status: subagent.TurnOK, NormEnd: int64(len(output)), Steering: []subagent.Steer{{ID: "first", TS: 1000, Message: "steer", Delivered: true}}}
	raw, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	turnPath := filepath.Join(dir, "turns", "1.json")
	if err := os.WriteFile(turnPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := subagent.Migrate(root); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise both the revision fence and the content fence. An external
	// writer that reuses a revision must not apply prepared anchors either.
	for _, change := range []string{"revision = revision + 1", "revision = revision"} {
		err := migrateSteeringAnchors(root, func() {
			_, err := db.Exec("UPDATE session_turns SET "+change+", turn=json_set(CAST(turn AS TEXT), '$.steering[0].id', 'changed') WHERE session=? AND n=1", id)
			if err != nil {
				t.Fatal(err)
			}
		})
		if !errors.Is(err, subagent.ErrSteeringChanged) {
			t.Fatalf("changed turn migration = %v", err)
		}
		var count int
		if err := db.QueryRow("SELECT count(*) FROM meta WHERE key=?", steeringAnchorsKey).Scan(&count); err != nil || count != 0 {
			t.Fatalf("migration completion = %d, %v", count, err)
		}
		var got []byte
		if err := db.QueryRow("SELECT turn FROM session_turns WHERE session=? AND n=1", id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		var current subagent.Turn
		if err := json.Unmarshal(got, &current); err != nil {
			t.Fatal(err)
		}
		if current.Steering[0].SourceOffset != nil {
			t.Fatal("stale anchor committed")
		}
		// Restore the input before the next preparation.
		if _, err := db.Exec("UPDATE session_turns SET turn=? WHERE session=? AND n=1", raw, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := MigrateSteeringAnchors(root); err != nil {
		t.Fatal(err)
	}
	var got []byte
	if err := db.QueryRow("SELECT turn FROM session_turns WHERE session=? AND n=1", id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	var current subagent.Turn
	if err := json.Unmarshal(got, &current); err != nil {
		t.Fatal(err)
	}
	if anchor := current.Steering[0].SourceOffset; anchor == nil || *anchor != 0 {
		t.Fatalf("anchor = %v; want known zero boundary", anchor)
	}
	if err := MigrateSteeringAnchors(root); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]byte{outputPath: output, turnPath: raw} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("artifact %s changed: %v", path, err)
		}
	}
}
