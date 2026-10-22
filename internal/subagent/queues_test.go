package subagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func queueDir(t *testing.T) string {
	t.Helper()
	sessions := filepath.Join(t.TempDir(), "sessions")
	t.Cleanup(func() { database.Close(filepath.Dir(sessions)) })
	dir := filepath.Join(sessions, NewID())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func claims(n int) queueClaims {
	return queueClaims{Claimed: map[string]int{"msg_a": n}, Cancelled: map[string]bool{}, Dismissed: map[string]bool{}}
}

func claimedTurn(t *testing.T, dir string) int {
	t.Helper()
	q, err := readQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	return q.Claimed["msg_a"]
}

func queueVersion(t *testing.T, dir string) string {
	t.Helper()
	_, version, err := readQueueRow(dir)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

// Queue writes and reads use SQLite without creating a retained file.
func TestQueueRowIsTheRecord(t *testing.T) {
	dir := queueDir(t)
	if q, err := readQueue(dir); err != nil || len(q.Claimed) != 0 {
		t.Fatalf("a new session's queue = %+v, %v", q, err)
	}
	if err := writeQueue(dir, claims(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, queueFile)); !os.IsNotExist(err) {
		t.Fatalf("queue file exists or cannot be checked: %v", err)
	}
	if got := claimedTurn(t, dir); got != 1 {
		t.Fatalf("claim = %d", got)
	}
	before := queueVersion(t, dir)
	if again := queueVersion(t, dir); again != before {
		t.Fatalf("read changed revision: %s then %s", before, again)
	}
}

// Current updates keep canonical claims even when a stale file is foreign.
func TestQueueUpdateIgnoresForeignFileWhenTheRowExists(t *testing.T) {
	dir := queueDir(t)
	if err := writeQueue(dir, claims(1)); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(claims(2))
	tmp := filepath.Join(dir, ".old-binary")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, queueFile)); err != nil {
		t.Fatal(err)
	}
	if got := claimedTurn(t, dir); got != 1 {
		t.Fatalf("a read after an old binary's write reads turn %d; want the row, 1", got)
	}
	if _, err := readQueueForUpdate(dir); err != nil {
		t.Fatal(err)
	}
	if got := claimedTurn(t, dir); got != 1 {
		t.Fatalf("after a read for an update, the claim reads turn %d; want canonical turn 1", got)
	}
	after := queueVersion(t, dir)
	if again := queueVersion(t, dir); again != after {
		t.Fatalf("an imported file was imported again: %s then %s", after, again)
	}
}

// A direct transactional update is visible to queue readers.
func TestQueueReadSeesCommittedRow(t *testing.T) {
	dir := queueDir(t)
	if err := writeQueue(dir, claims(1)); err != nil {
		t.Fatal(err)
	}
	db, err := queuesDB(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(claims(2))
	if err := state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := saveQueueRow(context.Background(), conn, filepath.Base(dir), b)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := claimedTurn(t, dir); got != 2 {
		t.Fatalf("the stale copy won over the row: turn %d", got)
	}
}

// A read-modify-write starts from the canonical row and keeps its own
// change without reviving a stale file's cancellation.
func TestQueueWriteIgnoresStaleFileCancellations(t *testing.T) {
	dir := queueDir(t)
	if err := writeQueue(dir, claims(1)); err != nil {
		t.Fatal(err)
	}
	old := claims(2)
	old.Cancelled["msg_old"] = true
	b, _ := json.Marshal(old)
	tmp := filepath.Join(dir, ".old-binary")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, queueFile)); err != nil {
		t.Fatal(err)
	}
	q, err := readQueueForUpdate(dir)
	if err != nil {
		t.Fatal(err)
	}
	q.claim("msg_a", 3)
	if err := writeQueue(dir, q); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		q, err := readQueue(dir)
		if err != nil || q.Claimed["msg_a"] != 3 || q.Cancelled["msg_old"] {
			t.Fatalf("queue = %+v, %v; want our claim (turn 3) without a stale cancellation", q, err)
		}
	}
}

// The first start of this release takes one backup before queue claims move
// into dyna.db. The eager import stays readable after its file is gone.
func TestQueuesBackUpOnceAndImportRetainedFiles(t *testing.T) {
	dir := queueDir(t)
	sessions := filepath.Dir(dir)
	other := filepath.Join(sessions, NewID())
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	for i, d := range []string{dir, other} {
		b, _ := json.Marshal(claims(i + 1))
		if err := os.WriteFile(filepath.Join(d, queueFile), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dataDir := filepath.Dir(sessions)
	for range 2 {
		if err := migrateQueues(dataDir); err != nil {
			t.Fatal(err)
		}
	}
	if backups, _ := filepath.Glob(filepath.Join(dataDir, "backups", database.PreMigrationFolder, "dyna-*.db")); len(backups) != 1 {
		t.Fatalf("backups: %v; want one, before the first migration", backups)
	}
	for i, d := range []string{dir, other} {
		if got := claimedTurn(t, d); got != i+1 {
			t.Fatalf("queue %d reads turn %d on its first read", i, got)
		}
		if err := os.Remove(filepath.Join(d, queueFile)); err != nil {
			t.Fatal(err)
		}
		if got := claimedTurn(t, d); got != i+1 {
			t.Fatalf("imported queue %d reads turn %d without its file", i, got)
		}
	}
}

// Malformed and valid foreign files cannot replace a canonical queue.
func TestDamagedQueueFileKeepsTheStoredQueue(t *testing.T) {
	dir := queueDir(t)
	if err := writeQueue(dir, claims(1)); err != nil {
		t.Fatal(err)
	}
	oldBinaryWrites := func(b []byte) {
		t.Helper()
		tmp := filepath.Join(dir, ".old-binary")
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, queueFile)); err != nil {
			t.Fatal(err)
		}
	}
	oldBinaryWrites([]byte(`{"claimed": {"msg_a": 2`))
	for range 2 {
		if got := claimedTurn(t, dir); got != 1 {
			t.Fatalf("with a damaged file, the claim reads turn %d; want the stored 1", got)
		}
	}
	if _, err := readQueueForUpdate(dir); err != nil {
		t.Fatal(err)
	}
	if got := claimedTurn(t, dir); got != 1 {
		t.Fatalf("after the damaged file was skipped, the claim reads turn %d; want the stored 1", got)
	}
	valid, _ := json.Marshal(claims(3))
	oldBinaryWrites(valid)
	if _, err := readQueueForUpdate(dir); err != nil {
		t.Fatal(err)
	}
	if got := claimedTurn(t, dir); got != 1 {
		t.Fatalf("a later valid file replaced canonical turn 1 with %d", got)
	}
}
