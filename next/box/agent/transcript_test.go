package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// transcriptEngine records each chunk and acknowledges it. With lose set, it stores the chunk but returns an error, as if the response was lost.
type transcriptEngine struct {
	requests []contract.TranscriptAppendRequest
	lose     bool
}

func (e *transcriptEngine) TranscriptAppend(_ context.Context, req contract.TranscriptAppendRequest) (contract.TranscriptAppendResponse, error) {
	e.requests = append(e.requests, req)
	acked := req.Offset + int64(len(req.Bytes))
	if e.lose {
		e.lose = false
		return contract.TranscriptAppendResponse{}, errors.New("response lost")
	}
	return contract.TranscriptAppendResponse{AckedOffset: acked}, nil
}

func writeSession(t *testing.T, file, data string) {
	t.Helper()
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptResendsFromAckedOffsetAfterLostResponse(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.jsonl")
	engine := &transcriptEngine{}
	transcript := NewTranscript(engine, dir)
	writeSession(t, file, "abc")
	if err := transcript.Sync(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	writeSession(t, file, "abcdef")
	engine.lose = true
	if err := transcript.Sync(context.Background(), file); err == nil {
		t.Fatal("Sync succeeded with a lost response")
	}
	if err := transcript.Sync(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	want := contract.TranscriptAppendRequest{Path: "a.jsonl", Offset: 3, Bytes: []byte("def")}
	last := engine.requests[len(engine.requests)-1]
	if last.Path != want.Path || last.Offset != want.Offset || string(last.Bytes) != string(want.Bytes) {
		t.Fatalf("resent request = %+v, want %+v", last, want)
	}
}

func TestTranscriptSplitsChunksAtOneMiB(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.jsonl")
	writeSession(t, file, strings.Repeat("x", transcriptChunk+1))
	engine := &transcriptEngine{}
	if err := NewTranscript(engine, dir).Sync(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if len(engine.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(engine.requests))
	}
	if got := len(engine.requests[0].Bytes); got != transcriptChunk {
		t.Fatalf("first chunk = %d bytes, want %d", got, transcriptChunk)
	}
	if engine.requests[1].Offset != transcriptChunk || len(engine.requests[1].Bytes) != 1 {
		t.Fatalf("second request = %+v, want offset %d and one byte", engine.requests[1], transcriptChunk)
	}
}

func TestTranscriptStartsNewFileAtZero(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.jsonl")
	second := filepath.Join(dir, "b.jsonl")
	writeSession(t, first, "abc")
	writeSession(t, second, "new")
	engine := &transcriptEngine{}
	transcript := NewTranscript(engine, dir)
	if err := transcript.Sync(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := transcript.Sync(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	last := engine.requests[len(engine.requests)-1]
	if last.Path != "b.jsonl" || last.Offset != 0 {
		t.Fatalf("new file request = %+v, want path b.jsonl at offset 0", last)
	}
}

type recordingTurnSink struct{}

func (recordingTurnSink) DeliveryDone(context.Context, contract.DeliverySeq) error { return nil }

func (recordingTurnSink) TurnOutput(context.Context, contract.TurnEnded) error { return nil }

func TestTranscriptTurnOutputIgnoresSyncError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.jsonl")
	writeSession(t, file, "abc")
	sink := endedSink{turnSink: recordingTurnSink{}, transcript: NewTranscript(&transcriptEngine{lose: true}, dir), file: func() string { return file }}
	if err := sink.TurnOutput(context.Background(), contract.TurnEnded{}); err != nil {
		t.Fatalf("TurnOutput = %v, want nil after a failed sync", err)
	}
}

type fakeCloser struct{}

func (fakeCloser) Close() error { return nil }

func TestCloseBoxSendsUnsentBytesBeforeArchive(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.jsonl")
	writeSession(t, file, "abc")
	engine := &transcriptEngine{}
	archived := false
	err := closeBox(context.Background(), fakeCloser{}, NewTranscript(engine, dir), file, func(context.Context) error {
		archived = true
		if len(engine.requests) != 1 || string(engine.requests[0].Bytes) != "abc" {
			t.Fatalf("archived before the transcript reached the engine: %+v", engine.requests)
		}
		return nil
	})
	if err != nil || !archived {
		t.Fatalf("closeBox = %v, archived = %v, want nil and true", err, archived)
	}
}

func TestCloseBoxKeepsBoxWhenTranscriptFails(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.jsonl")
	writeSession(t, file, "abc")
	archived := false
	err := closeBox(context.Background(), fakeCloser{}, NewTranscript(&transcriptEngine{lose: true}, dir), file, func(context.Context) error {
		archived = true
		return nil
	})
	if err == nil || archived {
		t.Fatalf("closeBox = %v, archived = %v, want an error and no archive", err, archived)
	}
}

func TestTranscriptTickSendsNewBytes(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.jsonl")
	engine := &transcriptEngine{}
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		NewTranscript(engine, dir).Run(ctx, ticks, func() string { return file })
		close(done)
	}()
	writeSession(t, file, "abc")
	ticks <- time.Time{}
	// An unbuffered send returns only after the loop finished the previous sync.
	ticks <- time.Time{}
	writeSession(t, file, "abcdef")
	ticks <- time.Time{}
	cancel()
	<-done
	last := engine.requests[len(engine.requests)-1]
	if last.Offset != 3 || string(last.Bytes) != "def" {
		t.Fatalf("tick request = %+v, want offset 3 and bytes def", last)
	}
}
