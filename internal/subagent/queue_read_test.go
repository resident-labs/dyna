package subagent

import (
	"os"
	"path/filepath"
	"testing"
)

// A queue read never writes an older copy over the row. A claim has
// committed while queue.json is an older copy that looks foreign, as when
// another binary's copy lands before the writer's own copy. A read without
// the gate lock takes the row as it is, so the claim stays and the post is
// not delivered again.
func TestQueueReadNeverWritesAnOlderCopyOverTheRow(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	dir, err := e.m.Store.Dir(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeQueue(dir, queueClaims{Claimed: map[string]int{}, Cancelled: map[string]bool{}, Dismissed: map[string]bool{}}); err != nil {
		t.Fatal(err)
	}
	stale := []byte(`{"claimed":{}}`)
	if err := writeQueue(dir, queueClaims{Claimed: map[string]int{"msg_kept": 1}, Cancelled: map[string]bool{}, Dismissed: map[string]bool{}}); err != nil {
		t.Fatal(err)
	}
	olderCopy(t, filepath.Join(dir, queueFile), stale)
	for range 2 {
		if e.m.Store.QueueSignature(info.ID) == "" {
			t.Fatal("no queue version")
		}
		q, err := readQueue(dir)
		if err != nil || q.Claimed["msg_kept"] != 1 {
			t.Fatalf("read queue claims %v, %v; want the committed claim msg_kept", q.Claimed, err)
		}
	}
}

// A retained file cannot refill an intentionally empty canonical queue.
func TestQueueReadKeepsTheEmptyCanonicalRow(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	dir, err := e.m.Store.Dir(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeQueue(dir, queueClaims{Claimed: map[string]int{}, Cancelled: map[string]bool{}, Dismissed: map[string]bool{}}); err != nil {
		t.Fatal(err)
	}
	olderCopy(t, filepath.Join(dir, queueFile), []byte(`{"claimed":{"msg_old":1}}`))
	if q, err := readQueue(dir); err != nil || len(q.Claimed) != 0 {
		t.Fatalf("the queue claims %v, %v; want the canonical empty queue", q.Claimed, err)
	}
}

// olderCopy puts raw at path as a new file, as another binary writes it, so
// the stamp does not own it.
func olderCopy(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path+".older", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".older", path); err != nil {
		t.Fatal(err)
	}
}
