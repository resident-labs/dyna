package state

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/hub"
)

func TestAppendRecordBuildsAtItsOffset(t *testing.T) {
	w, events := testWriter(t)
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := w.AppendRecord(context.Background(), hub.Event{Topic: "journal"}, path, func(r io.ReaderAt, size int64) ([]byte, error) {
		prior, err := io.ReadAll(io.NewSectionReader(r, 0, size))
		if err != nil || string(prior) != "{}\n" || size != 3 {
			t.Fatalf("builder read %q at %d, %v", prior, size, err)
		}
		return fmt.Appendf(nil, `{"id":"j%d"}`, size), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{}\n{\"id\":\"j3\"}\n" || len(*events) != 1 {
		t.Fatalf("journal %q, events %v, error %v", data, *events, err)
	}
}

func TestAppendRecordSkipLeavesPartialLineAlone(t *testing.T) {
	w, events := testWriter(t)
	before := w.Health().Writes
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendRecord(context.Background(), hub.Event{Topic: "journal"}, path, func(io.ReaderAt, int64) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "partial" || len(*events) != 0 {
		t.Fatalf("skipped append changed journal %q, events %v, error %v", data, *events, err)
	}
	if got := w.Health().Writes; got != before {
		t.Fatalf("skipped append changed write count from %d to %d", before, got)
	}
}

func TestAppendRecordSeparatesPartialLine(t *testing.T) {
	w, events := testWriter(t)
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendRecord(context.Background(), hub.Event{Topic: "journal"}, path, func(_ io.ReaderAt, size int64) ([]byte, error) {
		return fmt.Appendf(nil, `{"id":"j%d"}`, size), nil
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "partial\n{\"id\":\"j8\"}\n" || len(*events) != 1 {
		t.Fatalf("recovered journal %q, events %v, error %v", data, *events, err)
	}
}

func TestAppendRecordDeduplicatesUnderFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	var events atomic.Int32
	var wg sync.WaitGroup
	for range 2 {
		// Separate writers exercise the file lock, not one append mutex.
		w := newWriter(nil)
		w.SetPublish(func(hub.Event) { events.Add(1) })
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.AppendRecord(context.Background(), hub.Event{Topic: "journal"}, path, func(r io.ReaderAt, size int64) ([]byte, error) {
				prior, err := io.ReadAll(io.NewSectionReader(r, 0, size))
				if err != nil || bytes.Contains(prior, []byte("round-1")) {
					return nil, err
				}
				return []byte(`{"round":"round-1"}`), nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{\"round\":\"round-1\"}\n" || events.Load() != 1 {
		t.Fatalf("deduplicated journal %q, events %d, error %v", data, events.Load(), err)
	}
}

func TestAppendRecordFailedBuildWritesNothing(t *testing.T) {
	w, events := testWriter(t)
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	failed := errors.New("cannot build record")
	if err := w.AppendRecord(context.Background(), hub.Event{Topic: "journal"}, path, func(io.ReaderAt, int64) ([]byte, error) { return nil, failed }); !errors.Is(err, failed) {
		t.Fatalf("failed build returned %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := w.AppendRecord(ctx, hub.Event{Topic: "journal"}, path, func(io.ReaderAt, int64) ([]byte, error) {
		cancel()
		return []byte(`{}`), nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled build returned %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "partial" || len(*events) != 0 {
		t.Fatalf("failed append changed journal %q, events %v, error %v", data, *events, err)
	}
}

func TestAppendRecordLockWaitEndsWithContext(t *testing.T) {
	w, events := testWriter(t)
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	held, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Append(ctx, hub.Event{Topic: "journal"}, path, []byte(`{}`)) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lock wait returned %v; want deadline error", err)
		}
	case <-time.After(200 * time.Millisecond):
		held.Close()
		<-done
		t.Fatal("lock wait did not end with the context")
	}
	if len(*events) != 0 || w.Health().Writes != 0 || w.Health().InFlight != 0 {
		t.Fatalf("cancelled lock wait recorded a write: events %v, health %+v", *events, w.Health())
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(context.Background(), hub.Event{Topic: "journal"}, path, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 1 || w.Health().Writes != 1 {
		t.Fatalf("released lock did not allow one append: events %v, health %+v", *events, w.Health())
	}
}
