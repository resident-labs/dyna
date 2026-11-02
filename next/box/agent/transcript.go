package agent

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// transcriptChunk is the most bytes sent in one transcript.append call.
const transcriptChunk = 1 << 20

// transcriptInterval is how often a running turn sends new transcript bytes.
const transcriptInterval = 2 * time.Second

// transcriptRetryMin and transcriptRetryMax bound the wait between failed syncs before archive. The wait doubles each time.
const (
	transcriptRetryMin = time.Second
	transcriptRetryMax = 30 * time.Second
)

type transcriptSink interface {
	TranscriptAppend(context.Context, contract.TranscriptAppendRequest) (contract.TranscriptAppendResponse, error)
}

// Transcript sends Pi session files to the engine. Each file resumes from the offset the engine acknowledged.
type Transcript struct {
	sink    transcriptSink
	dir     string
	mu      sync.Mutex
	offsets map[string]int64
}

// NewTranscript sends the session files under dir, which is Pi's session directory.
func NewTranscript(sink transcriptSink, dir string) *Transcript {
	return &Transcript{sink: sink, dir: dir, offsets: make(map[string]int64)}
}

// Sync sends the bytes of file after the acknowledged offset, in chunks of at most 1 MiB.
func (t *Transcript) Sync(ctx context.Context, file string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	path, err := filepath.Rel(t.dir, file)
	if err != nil {
		return err
	}
	f, err := os.Open(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	offset := t.offsets[path]
	if info, err := f.Stat(); err == nil && info.Size() == offset {
		return nil // A crew asks after every worker it ever ran on each tick, and most have nothing new.
	}
	buf := make([]byte, transcriptChunk)
	for {
		n, err := f.ReadAt(buf, offset)
		if n == 0 {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		resp, err := t.sink.TranscriptAppend(ctx, contract.TranscriptAppendRequest{Path: path, Offset: offset, Bytes: buf[:n]})
		if err != nil {
			return err
		}
		offset = resp.AckedOffset
		t.offsets[path] = offset
	}
}

// Run syncs files on each tick until ctx ends. A failed sync is logged and retried on the next tick.
func (t *Transcript) Run(ctx context.Context, ticks <-chan time.Time, files func() []string) {
	for {
		select {
		case <-ticks:
			t.syncLogged(ctx, files()...)
		case <-ctx.Done():
			return
		}
	}
}

// syncLogged keeps transcript errors away from the box and the turn. The next sync resumes from the last acknowledged offset.
func (t *Transcript) syncLogged(ctx context.Context, files ...string) {
	for _, file := range files {
		if err := t.Sync(ctx, file); err != nil {
			log.Printf("transcript sync %s: %v", file, err)
		}
	}
}

// endedSink syncs the transcript after the engine accepts each turn output.
type endedSink struct {
	turnSink
	transcript *Transcript
	files      func() []string
}

func (s endedSink) TurnOutput(ctx context.Context, result contract.TurnEnded) error {
	err := s.turnSink.TurnOutput(ctx, result)
	s.transcript.syncLogged(ctx, s.files()...)
	return err
}

// Compacted sends the compaction to the engine's transcript first, since this answer lets the box stop.
func (s endedSink) Compacted(ctx context.Context, answer contract.BoxCompactedRequest) error {
	s.transcript.syncLogged(ctx, s.files()...)
	return s.turnSink.Compacted(ctx, answer)
}

// closeBox stops Pi and sends the rest of the transcript. It retries a failed sync with backoff, then reports the box archived, so the engine cannot delete the box with unsent bytes.
func closeBox(ctx context.Context, h io.Closer, transcript *Transcript, files func() []string, after func(time.Duration) <-chan time.Time, archived func(context.Context) error) error {
	if err := h.Close(); err != nil {
		return err
	}
	send := func(ctx context.Context) error {
		for _, file := range files() {
			if err := transcript.Sync(ctx, file); err != nil {
				return err
			}
		}
		return nil
	}
	if err := retryClose(ctx, after, send); err != nil {
		return err
	}
	return retryClose(ctx, after, archived)
}

// retryClose runs step until it succeeds. It waits between failures with backoff, so a transient engine error does not end the close.
func retryClose(ctx context.Context, after func(time.Duration) <-chan time.Time, step func(context.Context) error) error {
	delay := transcriptRetryMin
	for err := step(ctx); err != nil; err = step(ctx) {
		log.Printf("close step failed, retrying: %v", err)
		select {
		case <-after(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
		delay = min(2*delay, transcriptRetryMax)
	}
	return nil
}

// waitRun ends Run when the box channel or server fails, or when ctx ends. A runner error ends Run only before a close starts. After that, closeBox decides when Run ends, so a failed turn report cannot cancel the transcript retry.
func waitRun(ctx context.Context, errs <-chan error, runnerErrs <-chan error, piDone <-chan struct{}, closing *atomic.Bool) error {
	for {
		select {
		case err := <-errs:
			return err
		case err := <-runnerErrs:
			if !closing.Load() {
				return err
			}
			log.Printf("runner error after close started: %v", err)
		case <-piDone:
			if !closing.Load() {
				return errors.New("pi RPC stream closed")
			}
			piDone = nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// restoreTranscripts restores engine copies only when the volume has no session files.
func restoreTranscripts(ctx context.Context, dir string, files []contract.TranscriptFile, fetch func(context.Context, contract.TranscriptFetchRequest) (contract.TranscriptFetchResponse, error)) (err error) {
	existing, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(existing) > 0 {
		return err
	}
	type stagedFile struct{ temp, name string }
	var staged []stagedFile
	defer func() {
		for _, file := range staged {
			_ = os.Remove(file.temp)
		}
	}()
	for i, item := range files {
		name := filepath.Join(dir, item.Path)
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(name), ".transcript-*")
		if err != nil {
			return err
		}
		temp := f.Name()
		staged = append(staged, stagedFile{temp: temp, name: name})
		for offset := int64(0); offset < item.Bytes; {
			chunk, e := fetch(ctx, contract.TranscriptFetchRequest{Path: item.Path, Offset: offset})
			if e != nil {
				err = e
				break
			}
			if len(chunk.Bytes) == 0 || int64(len(chunk.Bytes)) > item.Bytes-offset {
				err = io.ErrUnexpectedEOF
				break
			}
			if _, err = f.Write(chunk.Bytes); err != nil {
				break
			}
			offset += int64(len(chunk.Bytes))
		}
		if e := f.Close(); err == nil {
			err = e
		}
		if err != nil {
			return err
		}
		at := time.Unix(int64(i+1), 0)
		if err := os.Chtimes(temp, at, at); err != nil {
			return err
		}
	}
	for i := len(staged) - 1; i >= 0; i-- {
		if err := os.Rename(staged[i].temp, staged[i].name); err != nil {
			return err
		}
	}
	return nil
}
