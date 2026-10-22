package subagent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

func TestCompletedQueueImportDoesNotResurrectDeletedRow(t *testing.T) {
	dir := queueDir(t)
	dataDir := filepath.Dir(filepath.Dir(dir))
	retained := []byte(`{"claimed":{"msg_a":1}}`)
	path := filepath.Join(dir, queueFile)
	if err := os.WriteFile(path, retained, 0600); err != nil {
		t.Fatal(err)
	}
	if err := migrateQueues(dataDir); err != nil {
		t.Fatal(err)
	}
	if got := claimedTurn(t, dir); got != 1 {
		t.Fatalf("imported claim = %d", got)
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM session_queues WHERE session=?", filepath.Base(dir)); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		if pass == 1 {
			database.Close(dataDir)
			if err := migrateQueues(dataDir); err != nil {
				t.Fatal(err)
			}
			db, err = database.Open(dataDir)
			if err != nil {
				t.Fatal(err)
			}
		}
		q, err := readQueue(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(q.Claimed) != 0 {
			t.Fatalf("deleted queue resurrected: %+v", q)
		}
		var count int
		if err := db.QueryRow("SELECT count(*) FROM session_queues WHERE session=?", filepath.Base(dir)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("canonical queue rows = %d", count)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, retained) {
			t.Fatalf("retained file changed: %q, %v", got, err)
		}
	}
}
