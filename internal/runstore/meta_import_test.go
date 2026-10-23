package runstore

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func TestMigrateImportsRunBeforeAnyRead(t *testing.T) {
	root := t.TempDir()
	t.Setenv(ArchiveDirEnv, t.TempDir())
	defer database.Close(root)
	for _, dir := range []string{filepath.Join(root, "runs"), filepath.Join(root, "archive", "runs"), ArchiveDir()} {
		id := "wf_retained"
		if dir == filepath.Join(root, "archive", "runs") {
			id = "wf_archived"
		}
		if dir == ArchiveDir() {
			id = "wf_custom"
		}
		dir = filepath.Join(dir, id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"id":"`+id+`","status":"running","future":{"keep":true}}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "paused"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(root); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"wf_retained", "wf_archived", "wf_custom"} {
		var future, paused bool
		if err := db.QueryRow(`SELECT json_extract(body,'$.future.keep'),json_extract(body,'$.paused') FROM runs WHERE id=?`, id).Scan(&future, &paused); err != nil || !future || !paused {
			t.Fatalf("startup import %s: future=%v paused=%v err=%v", id, future, paused, err)
		}
	}
	if err := state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), "DELETE FROM runs")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(root); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(root); err != nil {
		t.Fatal(err)
	}
	db, err = database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := metaAt(db, filepath.Join(root, "runs", "wf_retained"), "wf_retained"); !os.IsNotExist(err) {
		t.Fatalf("deleted run resurrected: %v", err)
	}
}
