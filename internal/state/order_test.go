package state

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/database/databasetest"
	"dyna-agent/internal/hub"
)

// Events follow commit order (item 14): a write that commits after another
// publishes after it, even when the first publish is slow.
func TestWriterPublishesInCommitOrder(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if _, err := db.Exec("CREATE TABLE things(name TEXT)"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var order []string
	publishing, release := make(chan struct{}), make(chan struct{})
	w := newWriter(db)
	w.SetPublish(func(ev hub.Event) {
		if ev.Key == "a" {
			close(publishing)
			<-release
		}
		mu.Lock()
		order = append(order, ev.Key)
		mu.Unlock()
	})
	first := make(chan error, 1)
	go func() { first <- w.Write(context.Background(), hub.Event{Topic: "things", Key: "a"}, insertThing) }()
	<-publishing
	second := make(chan error, 1)
	go func() { second <- w.Write(context.Background(), hub.Event{Topic: "things", Key: "b"}, insertThing) }()
	// b must wait for the writer lock while a publishes.
	databasetest.WaitForWriter(t, 0, second)

	close(release)
	for _, done := range []chan error{first, second} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("published %v; want [a b]", order)
	}
}

// Appends to one log publish in file order (item 14): Append publishes while
// it holds the append lock, so a second append cannot write its line between
// the first one's write and its event. It does not take the SQLite writer
// lock, so posts never queue behind SQL writes.
func TestAppendsPublishInFileOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	var order []string
	w := newWriter(nil)
	w.SetPublish(func(ev hub.Event) {
		if w.appendMu.TryLock() {
			w.appendMu.Unlock()
			t.Errorf("%s published without the append lock", ev.Key)
		}
		if database.Waiting() != 0 {
			t.Errorf("%s waits for the SQLite writer lock", ev.Key)
		}
		order = append(order, ev.Key)
	})
	var wg sync.WaitGroup
	for _, key := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.Append(context.Background(), hub.Event{Topic: "posts", Key: key}, path, []byte(`"`+key+`"`)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if file := strings.ReplaceAll(strings.TrimSpace(string(got)), "\"", ""); file != strings.Join(order, "\n") {
		t.Fatalf("file lines %q, published %v; want the same order", file, order)
	}
}
