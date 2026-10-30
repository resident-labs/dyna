package agent

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/resident-labs/dyna/contract"
)

// transcriptChunk is the most bytes sent in one transcript.append call.
const transcriptChunk = 1 << 20

type transcriptSink interface {
	TranscriptAppend(context.Context, contract.TranscriptAppendRequest) (contract.TranscriptAppendResponse, error)
}

// Transcript sends Pi session files to the engine. Each file resumes from the offset the engine acknowledged.
type Transcript struct {
	sink    transcriptSink
	dir     string
	offsets map[string]int64
}

// NewTranscript sends the session files under dir, which is Pi's session directory.
func NewTranscript(sink transcriptSink, dir string) *Transcript {
	return &Transcript{sink: sink, dir: dir, offsets: make(map[string]int64)}
}

// Sync sends the bytes of file after the acknowledged offset, in chunks of at most 1 MiB.
func (t *Transcript) Sync(ctx context.Context, file string) error {
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

// endedSink syncs the transcript after the engine accepts each turn output.
type endedSink struct {
	turnSink
	transcript *Transcript
	file       func() string
}

func (s endedSink) TurnOutput(ctx context.Context, result contract.TurnEnded) error {
	if err := s.turnSink.TurnOutput(ctx, result); err != nil {
		return err
	}
	return s.transcript.Sync(ctx, s.file())
}
