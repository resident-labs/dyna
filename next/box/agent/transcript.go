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
	"time"

	"github.com/resident-labs/dyna/contract"
)

// transcriptChunk is the most bytes sent in one transcript.append call.
const transcriptChunk = 1 << 20

// transcriptInterval is how often a running turn sends new transcript bytes.
const transcriptInterval = 2 * time.Second

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

// Run syncs file on each tick until ctx ends. A failed sync is logged and retried on the next tick.
func (t *Transcript) Run(ctx context.Context, ticks <-chan time.Time, file func() string) {
	for {
		select {
		case <-ticks:
			t.syncLogged(ctx, file())
		case <-ctx.Done():
			return
		}
	}
}

// syncLogged keeps transcript errors away from the box and the turn. The next sync resumes from the last acknowledged offset.
func (t *Transcript) syncLogged(ctx context.Context, file string) {
	if err := t.Sync(ctx, file); err != nil {
		log.Printf("transcript sync %s: %v", file, err)
	}
}

// endedSink syncs the transcript after the engine accepts each turn output.
type endedSink struct {
	turnSink
	transcript *Transcript
	file       func() string
}

func (s endedSink) TurnOutput(ctx context.Context, result contract.TurnEnded) error {
	err := s.turnSink.TurnOutput(ctx, result)
	s.transcript.syncLogged(ctx, s.file())
	return err
}

// closeBox stops Pi and sends the rest of the transcript. Only then does it report the box archived, so the engine cannot delete the box with unsent bytes.
func closeBox(ctx context.Context, h io.Closer, transcript *Transcript, file string, archived func(context.Context) error) error {
	if err := h.Close(); err != nil {
		return err
	}
	if err := transcript.Sync(ctx, file); err != nil {
		return err
	}
	return archived(ctx)
}
