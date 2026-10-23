package state

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/store"
)

// AppendRecord builds and appends one JSONL line under the log's locks.
// size is the offset where the line will start, after any partial-line
// separator. build may read r, but must not take locks or do slow work.
// Keep build to a tail read: it holds this Writer's append lock for every log.
// A nil line skips the append and its event. A line cannot contain a newline.
// Health counts only records that were appended.
func (w *Writer) AppendRecord(ctx context.Context, c hub.Event, path string, build func(r io.ReaderAt, size int64) ([]byte, error)) error {
	return w.appendCounted(ctx, c, path, build, false)
}

// AppendSynced is Append for a log that must survive a crash, such as a
// run's events and journal: the line is on disk before its event is
// published. The fsync runs after this Writer's append lock is released,
// under only the log's own file lock, so a slow flush delays appends to
// that log and never to others, such as room posts. A failed fsync returns
// an error and publishes nothing, since the line may not be durable.
func (w *Writer) AppendSynced(ctx context.Context, c hub.Event, path string, line []byte) error {
	return w.appendCounted(ctx, c, path, func(io.ReaderAt, int64) ([]byte, error) { return line, nil }, true)
}

// syncFile flushes an appended log. Tests replace it to count or block.
var syncFile = (*os.File).Sync

func (w *Writer) appendCounted(ctx context.Context, c hub.Event, path string, build func(io.ReaderAt, int64) ([]byte, error), sync bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id, start := w.begin()
	written, err := w.appendRecord(ctx, c, path, build, sync)
	if written {
		w.end(id, start, c)
	} else {
		w.mu.Lock()
		delete(w.inFlight, id)
		w.mu.Unlock()
	}
	return err
}

// appendRecord writes one line under the log's file lock and this Writer's
// append lock. Without sync it closes and queues its event under the append
// lock. With sync it releases the append lock, then fsyncs and queues under
// only the file lock, which still orders that log's lines and events. Both
// publish after releasing those locks.
func (w *Writer) appendRecord(ctx context.Context, c hub.Event, path string, build func(io.ReaderAt, int64) ([]byte, error), sync bool) (bool, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	// TODO: legacy/shim remove later — the file lock keeps direct CLI appenders ordered; remove when only the engine appends.
	if err := store.FlockContext(ctx, f); err != nil {
		return false, err
	}
	// Closing f releases the file lock on every return.
	w.appendMu.Lock()
	locked := true
	defer func() {
		if locked {
			w.appendMu.Unlock()
		}
	}()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	size := info.Size()
	var prefix []byte
	if size > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], size-1); err != nil {
			return false, err
		}
		if last[0] != '\n' {
			prefix, size = []byte{'\n'}, size+1
		}
	}
	line, err := build(f, size)
	if err != nil || line == nil {
		return false, err
	}
	if bytes.ContainsRune(line, '\n') {
		return false, errors.New("a JSONL line must not contain a newline")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// Copy, so framing never changes the caller's spare capacity.
	data := make([]byte, 0, len(prefix)+len(line)+1)
	data = append(append(append(data, prefix...), line...), '\n')
	if _, err := f.Write(data); err != nil {
		return false, err
	}
	if sync {
		w.appendMu.Unlock()
		locked = false
		if err := syncFile(f); err != nil {
			return true, err
		}
		publish := w.queue(c)
		err := f.Close()
		if publish != nil {
			publish()
		}
		return true, err
	}
	if err := f.Close(); err != nil {
		return true, err
	}
	publish := w.queue(c)
	w.appendMu.Unlock()
	locked = false
	if publish != nil {
		publish()
	}
	return true, nil
}
