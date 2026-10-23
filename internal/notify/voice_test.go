package notify

import (
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

// Voice relays the owner's spoken request from <room>:voice-call. Like a
// typed request, it starts the internal root when none is open, and root's
// successful turn reports back, which ends Voice's wait.
func TestVoiceRequestStartsRootAndGetsItsReport(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	ask, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderVoice, Room: rec.Name}, Mentions: []string{"@root"}, Text: "RESPOND: CI is green"})
	if err != nil {
		t.Fatal(err)
	}
	if msg := ask.Message; msg.From != rec.Name+":"+room.VoiceAgent || msg.Source != room.SourceVoice || msg.Human {
		t.Fatalf("request = %+v", msg)
	}
	if got := rootDelivery(ask, rec); got != "root-started" {
		t.Fatalf("root delivery = %q, want root-started", got)
	}
	root := e.wait(e.rootSession(rec).ID, 1)
	if p := e.turnInput(root.ID, 1); !strings.Contains(p, "RESPOND: CI is green") {
		t.Fatalf("first turn prompt = %q", p)
	}
	answer, err := e.svc.CompleteSessionTurn(e.ctx, root.ID, 1)
	if err != nil || answer.Message.ID == "" || !turnReport(answer.Message, room.TurnCompleted) || !strings.Contains(answer.Message.Text, "CI is green") {
		t.Fatalf("root owed Voice no report: %+v, %v", answer.Message, err)
	}
	// An open, idle root also owes a report for the next request.
	again, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderVoice, Room: rec.Name}, Mentions: []string{"@root"}, Text: "RESPOND: the queue is empty"})
	if err != nil || rootDelivery(again, rec) != "session-queued" {
		t.Fatalf("second request: %+v, %v", again.Deliveries, err)
	}
	e.wait(root.ID, 2)
	second, err := e.svc.CompleteSessionTurn(e.ctx, root.ID, 2)
	if err != nil || second.Message.ID == "" || !strings.Contains(second.Message.Text, "the queue is empty") {
		t.Fatalf("root owed the second request no report: %+v, %v", second.Message, err)
	}
}

// Root can post to @voice-call, bare or qualified. The post stays in the
// room and creates no call request. An external root can post there too.
func TestRootVoicePostDoesNotRequestACall(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	e.human(rec, "RESPOND: on it", "@root")
	root := e.wait(e.rootSession(rec).ID, 1)
	voice := rec.Name + ":" + room.VoiceAgent
	for _, c := range []struct {
		name    string
		sender  Sender
		mention string
	}{
		{"internal root, bare", Sender{Kind: SenderSession, Room: rec.Name, Session: root}, "@voice-call"},
		{"internal root, qualified", Sender{Kind: SenderSession, Room: rec.Name, Session: root}, "@" + voice},
		{"external root", Sender{Kind: SenderOwner, Room: rec.Name}, "@voice-call"},
	} {
		out, err := e.svc.Post(e.ctx, PostInput{Sender: c.sender, Mentions: []string{c.mention}, Text: "CI is failing on PR 978. Can you talk now?"})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := out.Message.To; len(got) != 1 || got[0] != voice {
			t.Fatalf("%s: to = %v, want [%s]", c.name, got, voice)
		}
		// Nothing wakes: the one delivery is the stored post.
		if len(out.Deliveries) != 1 || out.Deliveries[0].To != voice || out.Deliveries[0].Delivery != "room" {
			t.Fatalf("%s: deliveries = %+v", c.name, out.Deliveries)
		}
	}
	rows, err := e.svc.CallRequestStore().List(rec.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("voice post created call requests: %+v, %v", rows, err)
	}
	// An engine pass after the posts starts no turn.
	if _, err := e.mgr.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	if info, err := e.mgr.Store.Info(root.ID); err != nil || info.Turns != 1 || info.CurrentTurn != 0 {
		t.Fatalf("root woke: turns %d, current %d, %v", info.Turns, info.CurrentTurn, err)
	}
}

// Only root may post to the voice call: an agent, and the human typing in the app,
// get a clear error, and nothing is stored.
func TestOnlyRootCanCallTheOwner(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	agent := e.start(rec, "worker", nil)
	for _, snd := range []Sender{
		{Kind: SenderSession, Room: rec.Name, Session: agent},
		{Kind: SenderOwner, Room: rec.Name, Human: true},
	} {
		for _, mention := range []string{"@voice-call", "@" + rec.Name + ":" + room.VoiceAgent} {
			_, err := e.svc.Post(e.ctx, PostInput{Sender: snd, Mentions: []string{mention}, Text: "talk to me"})
			if err == nil || !strings.Contains(err.Error(), "post @voice-call is root only") {
				t.Fatalf("%v %s: err = %v", snd.Kind, mention, err)
			}
		}
	}
}
