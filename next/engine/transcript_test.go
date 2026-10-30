package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func transcriptCaller(t *testing.T) (*Service, context.Context, contract.Principal) {
	t.Helper()
	s, _, session, ctx := setup(t)
	s.SetTranscriptDir(t.TempDir())
	return s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}
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
	stored, err := os.ReadFile(filepath.Join(s.transcriptDir, string(caller.Session), "sessions/a.jsonl"))
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
	stored, err := os.ReadFile(filepath.Join(s.transcriptDir, string(caller.Session), "a.jsonl"))
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
