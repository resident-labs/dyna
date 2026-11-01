package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	sink := endedSink{turnSink: recordingTurnSink{}, transcript: NewTranscript(&transcriptEngine{lose: true}, dir), files: func() []string { return []string{file} }}
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
	noWait := func(time.Duration) <-chan time.Time {
		t.Fatal("closeBox waited although the sync succeeded")
		return nil
	}
	err := closeBox(context.Background(), fakeCloser{}, NewTranscript(engine, dir), func() []string { return []string{file} }, noWait, func(context.Context) error {
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

func TestCloseBoxRetriesFailedSyncBeforeArchive(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.jsonl")
	writeSession(t, file, "abc")
	engine := &transcriptEngine{lose: true}
	var waits []time.Duration
	fired := func(d time.Duration) <-chan time.Time {
		waits = append(waits, d)
		ticked := make(chan time.Time, 1)
		ticked <- time.Time{}
		return ticked
	}
	archived := false
	err := closeBox(context.Background(), fakeCloser{}, NewTranscript(engine, dir), func() []string { return []string{file} }, fired, func(context.Context) error {
		archived = true
		if len(engine.requests) != 2 || string(engine.requests[1].Bytes) != "abc" {
			t.Fatalf("archived before the retry reached the engine: %+v", engine.requests)
		}
		return nil
	})
	if err != nil || !archived {
		t.Fatalf("closeBox = %v, archived = %v, want nil and true", err, archived)
	}
	if len(waits) != 1 || waits[0] != transcriptRetryMin {
		t.Fatalf("waits = %v, want one wait of %v", waits, transcriptRetryMin)
	}
}

func TestCloseRetriesFailedArchiveDone(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.jsonl")
	writeSession(t, file, "abc")
	var waits []time.Duration
	fired := func(d time.Duration) <-chan time.Time {
		waits = append(waits, d)
		ticked := make(chan time.Time, 1)
		ticked <- time.Time{}
		return ticked
	}
	calls := 0
	err := closeBox(context.Background(), fakeCloser{}, NewTranscript(&transcriptEngine{}, dir), func() []string { return []string{file} }, fired, func(context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("engine unavailable")
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("closeBox = %v after %d ArchiveDone calls, want nil after 2", err, calls)
	}
	if len(waits) != 1 || waits[0] != transcriptRetryMin {
		t.Fatalf("waits = %v, want one wait of %v", waits, transcriptRetryMin)
	}
}

func TestPiExitStopsRunUnlessClosing(t *testing.T) {
	piDone := make(chan struct{})
	close(piDone)
	var closing atomic.Bool
	err := waitRun(context.Background(), nil, nil, piDone, &closing)
	if err == nil || err.Error() != "pi RPC stream closed" {
		t.Fatalf("Pi exit: %v", err)
	}
	closing.Store(true)
	errs := make(chan error, 1)
	errs <- nil
	if err := waitRun(context.Background(), errs, nil, piDone, &closing); err != nil {
		t.Fatalf("Pi exit during close interrupted archive: %v", err)
	}
}

func TestCloseKeepsRunAfterRunnerErrorAndRetriesSync(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.jsonl")
	writeSession(t, file, "abc")
	engine := &transcriptEngine{lose: true}
	errs := make(chan error, 1)
	runnerErrs := make(chan error)
	var closing atomic.Bool
	done := make(chan error, 1)
	go func() { done <- waitRun(context.Background(), errs, runnerErrs, nil, &closing) }()
	closing.Store(true)
	runnerErrs <- errors.New("turn output: engine unavailable")
	fired := func(time.Duration) <-chan time.Time {
		ticked := make(chan time.Time, 1)
		ticked <- time.Time{}
		return ticked
	}
	archived := false
	err := closeBox(context.Background(), fakeCloser{}, NewTranscript(engine, dir), func() []string { return []string{file} }, fired, func(context.Context) error {
		archived = true
		if len(engine.requests) != 2 || string(engine.requests[1].Bytes) != "abc" {
			t.Fatalf("archived before the retry reached the engine: %+v", engine.requests)
		}
		return nil
	})
	if err != nil || !archived {
		t.Fatalf("closeBox = %v, archived = %v, want nil and true", err, archived)
	}
	errs <- nil
	if err := <-done; err != nil {
		t.Fatalf("waitRun = %v, want nil when the channel ends", err)
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
		NewTranscript(engine, dir).Run(ctx, ticks, func() []string { return []string{file} })
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

func TestRestoreTranscripts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	files := []contract.TranscriptFile{{Path: "a.jsonl", Bytes: 3}, {Path: "b.jsonl", Bytes: 3}}
	calls := 0
	fetch := func(_ context.Context, req contract.TranscriptFetchRequest) (contract.TranscriptFetchResponse, error) {
		calls++
		return contract.TranscriptFetchResponse{Bytes: []byte(req.Path[0:1] + "bc")[req.Offset:]}, nil
	}
	if err := restoreTranscripts(ctx, dir, files, fetch); err != nil {
		t.Fatal(err)
	}
	a, err := os.ReadFile(filepath.Join(dir, files[0].Path))
	if err != nil || string(a) != "abc" {
		t.Fatalf("first file %q %v", a, err)
	}
	b, err := os.Stat(filepath.Join(dir, files[1].Path))
	if err != nil {
		t.Fatal(err)
	}
	ai, err := os.Stat(filepath.Join(dir, files[0].Path))
	if err != nil || !b.ModTime().After(ai.ModTime()) {
		t.Fatal("session order lost")
	}
	if err := restoreTranscripts(ctx, dir, files, fetch); err != nil || calls != 2 {
		t.Fatalf("existing files fetched again: %d %v", calls, err)
	}
	preserved, err := os.ReadFile(filepath.Join(dir, files[0].Path))
	if err != nil || string(preserved) != "abc" {
		t.Fatalf("existing content changed: %q %v", preserved, err)
	}
}

func TestRestoreFailureRemovesTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	files := []contract.TranscriptFile{{Path: "a.jsonl", Bytes: 1}, {Path: "b.jsonl", Bytes: 1}}
	fetch := func(_ context.Context, req contract.TranscriptFetchRequest) (contract.TranscriptFetchResponse, error) {
		if req.Path == "b.jsonl" {
			return contract.TranscriptFetchResponse{}, errors.New("interrupted")
		}
		return contract.TranscriptFetchResponse{Bytes: []byte("a")}, nil
	}
	if err := restoreTranscripts(context.Background(), dir, files, fetch); err == nil {
		t.Fatal("expected interruption")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("restore left files: %v %v", entries, err)
	}
}

func TestTranscriptFetchPreservesConflictCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"conflict","message":"short"}`))
	}))
	defer server.Close()
	c := &Client{http: server.Client()}
	c.http.Transport = rewriteTransport{base: server.URL, next: c.http.Transport}
	_, err := c.post(context.Background(), contract.MethodTranscriptFetch, nil)
	var api apiError
	if !errors.As(err, &api) || api.code != contract.ErrConflict {
		t.Fatalf("expected typed conflict: %v", err)
	}
}

type rewriteTransport struct {
	base string
	next http.RoundTripper
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = r.base[len("http://"):]
	if r.next == nil {
		return http.DefaultTransport.RoundTrip(req)
	}
	return r.next.RoundTrip(req)
}

func TestChannelSkipsAnUnknownMessageType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{\"type\":\"computer.watch.stop\",\"body\":{}}\n{\"type\":\"from.a.newer.engine\",\"body\":{}}\n{\"type\":\"computer.watch.stop\",\"body\":{}}\n"))
	}))
	defer server.Close()
	c := &Client{http: server.Client()}
	c.http.Transport = rewriteTransport{base: server.URL, next: c.http.Transport}
	stop := errors.New("second message reached the handler")
	received := 0
	err := c.Channel(context.Background(), func(contract.BoxMessage) error {
		if received++; received == 2 {
			return stop
		}
		return nil
	}, func(bool) {})
	if !errors.Is(err, stop) || received != 2 {
		t.Fatalf("channel ended with %v after %d messages, want the second known message to reach the handler", err, received)
	}
}
