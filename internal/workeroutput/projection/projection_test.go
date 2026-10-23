package projection_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
	"dyna-agent/internal/workeroutput"
	"dyna-agent/internal/workeroutput/projection"
)

func TestProjectionRetainsCursorAndRejectsChangedSource(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := store.DataDir()
	defer database.Close(root)
	const id = "wf_projection"
	path := filepath.Join(root, "runs", id, "agents", "1", "output.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("{\"kind\":\"text\",\"text\":\"old\"}\n")
	partial := []byte("malformed\n{\"kind\":")
	source := append(append([]byte(nil), raw...), partial...)
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	if err := projection.Migrate(root); err != nil {
		t.Fatal(err)
	}
	pack, err := projection.NewPack("runs/"+id+"/agents/1", path)
	if err != nil {
		t.Fatal(err)
	}
	defer pack.Abort()
	if err := pack.Add([]projection.Line{{Start: 0, End: int64(len(raw)), Raw: raw}}, func(events []workeroutput.Event) []int {
		events[0].Text = "canonical text is longer"
		return []int{0}
	}); err != nil {
		t.Fatal(err)
	}
	prepared, err := pack.Finish(int64(len(source)), int64(len(raw)+len("malformed\n")))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "test.projection", 1, func(conn *sql.Conn, _ int) error { return prepared.Commit(context.Background(), conn) }); err != nil {
		t.Fatal(err)
	}
	events, next, err := runstore.ReadAgentNormalizedOutputFrom(id, 1, 0)
	if err != nil || len(events) != 1 || events[0].Text != "canonical text is longer" || next != int64(len(raw)+len("malformed\n")) {
		t.Fatalf("projected read = %+v, cursor %d, error %v", events, next, err)
	}
	// Moving the artifact and its pack must preserve the stable key.
	moved := filepath.Join(root, "archive", "runs", id)
	if err := os.MkdirAll(filepath.Dir(moved), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "runs", id), moved); err != nil {
		t.Fatal(err)
	}
	events, next, err = runstore.ReadAgentNormalizedOutputFrom(id, 1, 0)
	if err != nil || len(events) != 1 || events[0].Text != "canonical text is longer" || next != int64(len(raw)+len("malformed\n")) {
		t.Fatalf("archived read = %+v, %d, %v", events, next, err)
	}
	source[0] = ' '
	if err := os.WriteFile(filepath.Join(moved, "agents", "1", "output.jsonl"), source, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runstore.ReadAgentNormalizedOutputFrom(id, 1, 0); err == nil {
		t.Fatal("changed source returned stale converted output")
	}
}
