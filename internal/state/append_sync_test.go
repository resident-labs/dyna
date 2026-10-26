package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/hub"
)

// replaceSync swaps the fsync seam for the test.
func replaceSync(t *testing.T, fn func(*os.File) error) {
	t.Helper()
	old := syncFile
	syncFile = fn
	t.Cleanup(func() { syncFile = old })
}

// published records the topics a Writer publishes.
type published struct {
	mu     sync.Mutex
	topics []string
}

func (p *published) add(ev hub.Event) {
	p.mu.Lock()
	p.topics = append(p.topics, ev.Topic)
	p.mu.Unlock()
}
func (p *published) list() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.topics...)
}

// AppendSynced fsyncs its line once, before it publishes; Append never fsyncs.
func TestAppendSyncedSyncsOnceAndAppendNever(t *testing.T) {
	syncs := 0
	replaceSync(t, func(*os.File) error { syncs++; return nil })
	var got published
	w := newWriter(nil)
	w.SetPublish(got.add)
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := w.Append(context.Background(), hub.Event{Topic: "plain"}, path, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if syncs != 0 {
		t.Fatalf("Append fsynced %d times; want none", syncs)
	}
	if err := w.AppendSynced(context.Background(), hub.Event{Topic: "durable"}, path, []byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	if syncs != 1 {
		t.Fatalf("AppendSynced fsynced %d times; want once", syncs)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "{\"a\":1}\n{\"a\":2}\n" {
		t.Fatalf("log = %q, %v", b, err)
	}
	if topics := got.list(); len(topics) != 2 || topics[1] != "durable" {
		t.Fatalf("published %v; want plain, durable", topics)
	}
}

// A failed fsync returns its error and publishes nothing: the line may not
// be durable.
func TestAppendSyncedErrorPublishesNothing(t *testing.T) {
	failed := errors.New("disk gone")
	replaceSync(t, func(*os.File) error { return failed })
	var got published
	w := newWriter(nil)
	w.SetPublish(got.add)
	err := w.AppendSynced(context.Background(), hub.Event{Topic: "durable"}, filepath.Join(t.TempDir(), "log.jsonl"), []byte(`{}`))
	if !errors.Is(err, failed) {
		t.Fatalf("AppendSynced = %v; want the fsync error", err)
	}
	if topics := got.list(); len(topics) != 0 {
		t.Fatalf("published %v after a failed fsync; want nothing", topics)
	}
}

// A slow fsync on one log never delays appends to another log, such as a
// room post: the fsync runs outside this Writer's append lock.
func TestBlockedSyncDoesNotDelayOtherLogs(t *testing.T) {
	dir := t.TempDir()
	slow, fast := filepath.Join(dir, "journal.jsonl"), filepath.Join(dir, "posts.jsonl")
	syncing, release := make(chan struct{}), make(chan struct{})
	replaceSync(t, func(f *os.File) error {
		if f.Name() == slow {
			close(syncing)
			<-release
		}
		return nil
	})
	var got published
	w := newWriter(nil)
	w.SetPublish(got.add)
	done := make(chan error, 1)
	go func() { done <- w.AppendSynced(context.Background(), hub.Event{Topic: "journal"}, slow, []byte(`{}`)) }()
	<-syncing
	// The journal's fsync is blocked; a post to another log still lands. The
	// guard only turns a wrong lock order, which would hang, into a failure.
	posted := make(chan error, 1)
	go func() { posted <- w.Append(context.Background(), hub.Event{Topic: "posts"}, fast, []byte(`{}`)) }()
	select {
	case err := <-posted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("an append to another log waited for the journal's fsync")
	}
	if topics := got.list(); len(topics) != 1 || topics[0] != "posts" {
		t.Fatalf("published %v while the journal fsync was blocked; want only posts", topics)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if topics := got.list(); len(topics) != 2 || topics[1] != "journal" {
		t.Fatalf("published %v; want the journal after its fsync", topics)
	}
}
