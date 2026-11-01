package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func transcriptCaller(t *testing.T) (*Service, context.Context, contract.Principal) {
	t.Helper()
	s, _, session, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	return s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}
}

func transcripts(t *testing.T, s *Service, ctx context.Context, caller contract.Principal) string {
	t.Helper()
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		t.Fatal(err)
	}
	return s.layout.Transcripts(session.Room, caller.Session)
}

func appendTranscript(s *Service, ctx context.Context, caller contract.Principal, path string, offset int64, data string) (contract.TranscriptAppendResponse, *contract.Error) {
	return s.TranscriptAppend(ctx, caller, contract.TranscriptAppendRequest{Path: path, Offset: offset, Bytes: []byte(data)})
}

func TestTranscriptAppendResumesFromAckedOffset(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	if resp, e := appendTranscript(s, ctx, caller, "sessions/a.jsonl", 0, "abc"); e != nil || resp.AckedOffset != 3 {
		t.Fatalf("first chunk = %+v, %v; want ack 3", resp, e)
	}
	// The box resends a chunk that spans the ack. Only the new tail is stored.
	if resp, e := appendTranscript(s, ctx, caller, "sessions/a.jsonl", 0, "abcdef"); e != nil || resp.AckedOffset != 6 {
		t.Fatalf("spanning chunk = %+v, %v; want ack 6", resp, e)
	}
	stored, err := os.ReadFile(filepath.Join(transcripts(t, s, ctx, caller), "sessions/a.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != "abcdef" {
		t.Fatalf("stored = %q, want %q", stored, "abcdef")
	}
}

func TestTranscriptAppendNoOpsDuplicateChunk(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	if _, e := appendTranscript(s, ctx, caller, "a.jsonl", 0, "abc"); e != nil {
		t.Fatal(e)
	}
	if resp, e := appendTranscript(s, ctx, caller, "a.jsonl", 0, "abc"); e != nil || resp.AckedOffset != 3 {
		t.Fatalf("duplicate = %+v, %v; want the same ack 3", resp, e)
	}
}

func TestTranscriptAppendRefusesGap(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	if _, e := appendTranscript(s, ctx, caller, "a.jsonl", 0, "abc"); e != nil {
		t.Fatal(e)
	}
	if _, e := appendTranscript(s, ctx, caller, "a.jsonl", 9, "xyz"); e == nil || e.Code != contract.ErrConflict {
		t.Fatalf("gap error = %v; want conflict", e)
	}
}

func TestTranscriptAppendSkipsStoredPrefix(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	if _, e := appendTranscript(s, ctx, caller, "a.jsonl", 0, "abc"); e != nil {
		t.Fatal(e)
	}
	// The bytes below the ack are not compared. Only the new tail is written.
	if resp, e := appendTranscript(s, ctx, caller, "a.jsonl", 0, "xbcdef"); e != nil || resp.AckedOffset != 6 {
		t.Fatalf("prefix chunk = %+v, %v; want ack 6", resp, e)
	}
	stored, err := os.ReadFile(filepath.Join(transcripts(t, s, ctx, caller), "a.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != "abcdef" {
		t.Fatalf("stored = %q, want %q", stored, "abcdef")
	}
}

func TestTranscriptAppendStartsNewFileAtZero(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	if _, e := appendTranscript(s, ctx, caller, "a.jsonl", 0, "abc"); e != nil {
		t.Fatal(e)
	}
	if resp, e := appendTranscript(s, ctx, caller, "b.jsonl", 0, "new"); e != nil || resp.AckedOffset != 3 {
		t.Fatalf("new file = %+v, %v; want ack 3", resp, e)
	}
}

func TestTranscriptFetchAcknowledged(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	if _, e := appendTranscript(s, ctx, caller, "a.jsonl", 0, "abc"); e != nil {
		t.Fatal(e)
	}
	name := filepath.Join(transcripts(t, s, ctx, caller), "a.jsonl")
	if err := os.WriteFile(name, []byte("abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	got, e := s.TranscriptFetch(ctx, caller, contract.TranscriptFetchRequest{Path: "a.jsonl", Offset: 1})
	if e != nil || string(got.Bytes) != "bc" {
		t.Fatalf("fetch = %q, %v", got.Bytes, e)
	}
	if err := os.Truncate(name, 2); err != nil {
		t.Fatal(err)
	}
	if _, e := s.TranscriptFetch(ctx, caller, contract.TranscriptFetchRequest{Path: "a.jsonl"}); e == nil || e.Code != contract.ErrConflict {
		t.Fatal("short backing file did not return conflict")
	}
}

func TestTranscriptFetchBoundaries(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	data := make([]byte, (1<<20)+1)
	if _, e := s.TranscriptAppend(ctx, caller, contract.TranscriptAppendRequest{Path: "a", Bytes: data}); e != nil {
		t.Fatal(e)
	}
	got, e := s.TranscriptFetch(ctx, caller, contract.TranscriptFetchRequest{Path: "a"})
	if e != nil || len(got.Bytes) != (1<<20) {
		t.Fatalf("chunk %d: %v", len(got.Bytes), e)
	}
	for _, req := range []contract.TranscriptFetchRequest{{Path: "../a"}, {Path: "a", Offset: -1}, {Path: "a", Offset: int64(len(data) + 1)}} {
		if _, e := s.TranscriptFetch(ctx, caller, req); e == nil {
			t.Fatalf("accepted %+v", req)
		}
	}
	other := contract.Principal{Kind: contract.PrincipalSession, Session: "another"}
	if got, e := s.TranscriptFetch(ctx, other, contract.TranscriptFetchRequest{Path: "a"}); e == nil && len(got.Bytes) > 0 {
		t.Fatal("another session read bytes")
	}
}
