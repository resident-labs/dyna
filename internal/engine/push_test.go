package engine

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/websvc"
)

type fakeNotifier struct{ events []websvc.Event }

func (f *fakeNotifier) Notify(_ context.Context, ev websvc.Event) { f.events = append(f.events, ev) }

// Voice call posts never push: the owner is on the call, and Voice's
// requests to root are not owner-facing. Other posts to the owner still do.
func TestPushPostsSkipsVoiceCallPosts(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	rooms := roomsvc.New(nil)
	rec, err := rooms.Notify.Rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	voice := rec.Name + ":" + room.VoiceAgent
	post := func(m room.Message) roomsvc.Post { m.ID = "msg_" + m.Text; return roomsvc.Post{Message: m} }
	voiceShapes := []roomsvc.Post{
		post(room.Message{From: rec.Name, To: []string{voice}, Human: true, Spoken: true, Kind: "voice", Source: room.SourceVoice, Text: "owner"}),
		post(room.Message{From: voice, To: []string{rec.Name}, Spoken: true, Kind: "voice", Source: room.SourceVoice, Text: "spoken"}),
		post(room.Message{From: voice, To: []string{rec.Name}, RelayOf: "msg_root", Kind: "voice", Source: room.SourceVoice, Text: "relay"}),
		post(room.Message{From: voice, To: []string{rec.Name + ":root"}, Kind: "message", Source: room.SourceVoice, Text: "toroot"}),
	}
	for _, p := range voiceShapes {
		n := &fakeNotifier{}
		pushPosts(t.Context(), rooms, n, roomsvc.Event{Kind: "posts", RoomID: rec.ID, Posts: []roomsvc.Post{p}})
		if len(n.events) != 0 {
			t.Fatalf("voice post %q pushed %+v", p.Text, n.events)
		}
	}
	n := &fakeNotifier{}
	agent := post(room.Message{From: rec.Name + ":lead", To: []string{rec.Name}, Kind: "message", Text: "real"})
	pushPosts(t.Context(), rooms, n, roomsvc.Event{Kind: "posts", RoomID: rec.ID, Posts: []roomsvc.Post{agent}})
	if len(n.events) != 1 || n.events[0].Kind != "post" {
		t.Fatalf("agent post pushed %+v, want one post push", n.events)
	}
}

// A completion in the same burst must not swallow the question after it.
func TestQuestionAfterCompletionInOneBurstPushes(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	rooms := roomsvc.New(nil)
	rec, err := rooms.Notify.Rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	lead := rec.Name + ":lead"
	done := roomsvc.Post{Message: room.Message{ID: "m1", From: lead, To: []string{rec.Name}, Kind: "completed", Turn: 1, Text: "done"}, Completion: true, Speaker: "lead"}
	ask := roomsvc.Post{Message: room.Message{ID: "m2", From: lead, To: []string{rec.Name}, Kind: "message", Text: "[QUESTION q1] Which fix?"}, Speaker: "lead"}
	n := &fakeNotifier{}
	pushPosts(t.Context(), rooms, n, roomsvc.Event{Kind: "posts", RoomID: rec.ID, Posts: []roomsvc.Post{done, ask}})
	if len(n.events) != 2 || n.events[0].Kind != "turn" || n.events[1].Kind != "question" || n.events[1].Post != "m2" {
		t.Fatalf("pushed %+v, want the completion and the question", n.events)
	}
}

// A failed session turn and a failed workflow run both push, including a
// worker and a run whose first target is the parent session. A shell exit
// stays in shell jobs and does not push.
func TestFailedTurnAndWorkflowRunPush(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	rooms := roomsvc.New(nil)
	rec, err := rooms.Notify.Rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	parent := rec.Name + ":lead"
	failed := func(id, from, speaker, text string, to []string) roomsvc.Post {
		m := room.Message{ID: id, From: from, To: to, Kind: "failed", Turn: 2, Text: text}
		return roomsvc.Post{Message: m, Completion: roomsvc.IsCompletion(m), Speaker: speaker}
	}
	turn := failed("m3", parent, "lead", "lead turn 2 failed. Error: boom", []string{rec.Name})
	worker := failed("m6", rec.Name+":worker", "worker", "worker turn 2 failed. Error: disk", []string{parent, rec.Name})
	other := failed("m7", rec.Name+":other", "other", "other turn 4 failed. Error: timeout", []string{parent, rec.Name})
	run := failed("m4", "run:wf_20261016-000009-eeeeeeee", "workflow review", "run failed. Error: disk full", []string{parent, rec.Name})
	shell := failed("m5", "shell:sh_20261017-120000-0000000a", "shell", "shell failed", []string{parent, rec.Name})
	n := &fakeNotifier{}
	pushPosts(t.Context(), rooms, n, roomsvc.Event{Kind: "posts", RoomID: rec.ID, Posts: []roomsvc.Post{turn, worker, other, run, shell}})
	wantFrom := []string{"lead", "worker", "other", "workflow review"}
	if len(n.events) != len(wantFrom) {
		t.Fatalf("pushed %+v, want one failure from each session and the run", n.events)
	}
	for i, from := range wantFrom {
		if n.events[i].Kind != "failure" || n.events[i].From != from {
			t.Fatalf("push %d = %+v, want failure from %s", i, n.events[i], from)
		}
	}
	if !strings.Contains(n.events[0].Text, "boom") || n.events[3].Post != "m4" {
		t.Fatalf("pushed %+v", n.events)
	}
}
