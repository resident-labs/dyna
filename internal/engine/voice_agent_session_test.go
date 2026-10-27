package engine

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/voice"
	"dyna-agent/internal/voice/agentwire"
	"dyna-agent/internal/websvc"
)

// lineStream feeds a session its lines, then ends the input.
type lineStream struct{ lines []agentwire.InMessage }

func (s *lineStream) Read() (agentwire.InMessage, error) {
	if len(s.lines) == 0 {
		return agentwire.InMessage{}, io.EOF
	}
	m := s.lines[0]
	s.lines = s.lines[1:]
	return m, nil
}

func (s *lineStream) Write(agentwire.OutMessage) error { return nil }

// runnerFunc is a session runner that a test writes in place.
type runnerFunc func(context.Context, agentwire.SessionRequest, voice.SessionIO) (voice.SessionEnd, error)

func (f runnerFunc) Run(ctx context.Context, req agentwire.SessionRequest, io voice.SessionIO) (voice.SessionEnd, error) {
	return f(ctx, req, io)
}

// A session tells its room when it starts and ends, with the time it used and
// why, and the two notices wake nobody.
func TestAgentVoiceSessionTellsTheRoomAndWakesNobody(t *testing.T) {
	s := testServices(t, false)
	rec, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "voice-notice"})
	if err != nil {
		t.Fatal(err)
	}
	h := &agentVoice{
		sessions: runnerFunc(func(_ context.Context, req agentwire.SessionRequest, io voice.SessionIO) (voice.SessionEnd, error) {
			io.Started("openai")
			return voice.SessionEnd{Provider: "openai", Elapsed: 200 * time.Second, Reason: agentwire.EndTimeLimit}, nil
		}),
		actor: func(websvc.Caller) (authority.Actor, error) {
			return authority.Actor{Role: authority.Session, Session: "s1", Room: rec.ID}, nil
		},
		address: func(authority.Actor) string { return rec.Name + ":storyteller" },
		post:    postVoiceSession(s.rooms),
		newID:   func() string { return "v1" },
	}
	stream := &lineStream{lines: []agentwire.InMessage{{Type: agentwire.InStart, Request: &agentwire.SessionRequest{}}}}
	if err := h.Session(t.Context(), websvc.Caller{Kind: websvc.Session, Session: "s1"}, stream); err != nil {
		t.Fatal(err)
	}
	posts, err := s.rooms.Notify.Rooms.Messages(rec.ID)
	if err != nil || len(posts) != 2 {
		t.Fatalf("posts = %+v, %v", posts, err)
	}
	want := []room.VoiceSessionEvent{
		{Type: room.VoiceStarted, Voice: "v1", Session: rec.Name + ":storyteller", Provider: "openai"},
		{Type: room.VoiceEnded, Voice: "v1", Session: rec.Name + ":storyteller", Provider: "openai", Seconds: 200, Reason: room.VoiceEndTimeLimit},
	}
	for i, post := range posts {
		ev, ok := post.VoiceSession()
		if !ok || ev != want[i] {
			t.Fatalf("notice %d = %+v, %v; want %+v", i, ev, ok, want[i])
		}
		if decision, why := room.RootContext(post, rec.Name); decision == room.RootWake || post.NeedsRoot {
			t.Fatalf("notice %d wakes root: %v (%s)", i, decision, why)
		}
	}
}

func TestAgentVoiceSessionRefusesAnUnverifiedCallerAndABadStart(t *testing.T) {
	ran := false
	h := &agentVoice{
		sessions: runnerFunc(func(context.Context, agentwire.SessionRequest, voice.SessionIO) (voice.SessionEnd, error) {
			ran = true
			return voice.SessionEnd{}, nil
		}),
		address: func(authority.Actor) string { return "" },
		newID:   func() string { return "v1" },
	}
	h.actor = func(websvc.Caller) (authority.Actor, error) {
		return authority.Actor{}, errors.New("the engine could not verify this caller")
	}
	if err := h.Session(t.Context(), websvc.Caller{}, &lineStream{}); err == nil {
		t.Fatal("an unverified caller started a session")
	}
	h.actor = func(websvc.Caller) (authority.Actor, error) { return authority.Actor{Role: authority.Owner}, nil }
	if err := h.Session(t.Context(), websvc.Caller{}, &lineStream{lines: []agentwire.InMessage{{Type: agentwire.InAudio}}}); err == nil || ran {
		t.Fatalf("a session without a start line ran: err=%v ran=%v", err, ran)
	}
}
