package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestTranscriptAppendRefusesGap(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	if _, e := appendTranscript(s, ctx, caller, "a.jsonl", 0, "abc"); e != nil {
		t.Fatal(e)
	}
	if _, e := appendTranscript(s, ctx, caller, "a.jsonl", 9, "xyz"); e == nil || e.Code != contract.ErrConflict {
		t.Fatalf("gap error = %v; want conflict", e)
	}
}

// A session open across the move to room folders has an acknowledged offset and no copy. Writing at the offset would leave
// a hole of zero bytes, which then reads as the session's history, so the box goes back to the start of its own file.
func TestTranscriptAppendRewindsABoxWhoseCopyIsGone(t *testing.T) {
	for name, lose := range map[string]func(string) error{
		"missing": os.Remove,
		"short":   func(path string) error { return os.Truncate(path, 1) },
	} {
		t.Run(name, func(t *testing.T) {
			s, ctx, caller := transcriptCaller(t)
			if resp, e := appendTranscript(s, ctx, caller, "a.jsonl", 0, "abc"); e != nil || resp.AckedOffset != 3 {
				t.Fatalf("first chunk = %+v, %v", resp, e)
			}
			path := filepath.Join(transcripts(t, s, ctx, caller), "a.jsonl")
			if err := lose(path); err != nil {
				t.Fatal(err)
			}
			if resp, e := appendTranscript(s, ctx, caller, "a.jsonl", 3, "def"); e != nil || resp.AckedOffset != 0 {
				t.Fatalf("append past the lost copy = %+v, %v; want a rewind", resp, e)
			}
			if resp, e := appendTranscript(s, ctx, caller, "a.jsonl", 0, "abcdef"); e != nil || resp.AckedOffset != 6 {
				t.Fatalf("resend = %+v, %v", resp, e)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "abcdef" {
				t.Fatalf("copy = %q, %v", got, err)
			}
			fetched, e := s.TranscriptFetch(ctx, caller, contract.TranscriptFetchRequest{Path: "a.jsonl"})
			if e != nil || string(fetched.Bytes) != "abcdef" {
				t.Fatalf("fetch = %q, %v", fetched.Bytes, e)
			}
		})
	}
}

func TestTranscriptRecordsFailedToolCalls(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	now := s.now().UTC().Format(time.RFC3339Nano)
	old := s.now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	call := `{"type":"message","timestamp":"` + now + `","message":{"role":"assistant","model":"gpt-x","content":[{"type":"toolCall","id":"c1","name":"read","arguments":{"path":"gone.md"}}]}}` + "\n"
	result := `{"type":"message","timestamp":"` + now + `","message":{"role":"toolResult","toolCallId":"c1","toolName":"read","isError":true,"content":[{"type":"text","text":"ENOENT: gone.md"}]}}` + "\n"
	stale := `{"type":"message","timestamp":"` + old + `","message":{"role":"toolResult","toolCallId":"c0","toolName":"bash","isError":true,"content":[{"type":"text","text":"old"}]}}` + "\n"
	// The call and its result arrive in separate appends, as they do while a tool runs.
	if _, e := appendTranscript(s, ctx, caller, "s.jsonl", 0, stale+call); e != nil {
		t.Fatal(e)
	}
	if _, e := appendTranscript(s, ctx, caller, "s.jsonl", int64(len(stale+call)), result); e != nil {
		t.Fatal(e)
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		t.Fatal(err)
	}
	got := eventsOf[contract.ToolFailed](t, s, ctx, session.Room)
	want := contract.ToolFailed{Session: caller.Session, Tool: "read", Model: "gpt-x", Args: `{"path":"gone.md"}`, Error: "ENOENT: gone.md"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestHeadTailKeepsTheEndOfALongError(t *testing.T) {
	long := strings.Repeat("o", 900) + " Script error: SyntaxError"
	got := headTail(long, 300)
	if !strings.HasSuffix(got, "Script error: SyntaxError") || !strings.HasPrefix(got, "ooo") || len([]rune(got)) != 603 {
		t.Fatalf("%d %q", len([]rune(got)), got)
	}
	if headTail("short", 300) != "short" {
		t.Fatal("short changed")
	}
}
