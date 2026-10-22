package realtime

import (
	"context"
	"encoding/base64"
	"reflect"
	"testing"
	"time"
)

// Audio goes in as Float32 in 1 s chunks; until a chunk fills, nothing is
// sent.
func TestMiniCPMOSendsAudioInSecondChunks(t *testing.T) {
	m := &MiniCPMOProtocol{}
	m.SessionUpdate(msg{"instructions": "Be Voice."})
	if m.AudioAppend(make([]byte, miniCPMOChunk-2)) != nil {
		t.Fatal("a short chunk went out")
	}
	out, ok := m.AudioAppend(make([]byte, 4)).(msg)
	if !ok || out["type"] != "input.append" {
		t.Fatalf("got %v, want a chunk", out)
	}
	f32, _ := base64.StdEncoding.DecodeString(out["input"].(msg)["audio"].(string))
	if len(f32) != miniCPMOChunk*2 || len(m.pending) != 2 {
		t.Fatalf("chunk of %d Float32 bytes and %d left, want %d and 2", len(f32), len(m.pending), miniCPMOChunk*2)
	}
	if got := pcm16ToFloat32([]byte{0x00, 0x40}); got != base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 0x3f}) {
		t.Fatalf("16384 became %s, want 0.5", got)
	}
}

// Output deltas become one response that a listen delta ends; the full
// reply in response.done works too.
func TestMiniCPMOMapsOutputToResponses(t *testing.T) {
	m := &MiniCPMOProtocol{}
	m.SessionUpdate(nil)
	step := func(in string, want ...Kind) []Event {
		t.Helper()
		evs, err := m.DecodeAll([]byte(in))
		if err != nil || !reflect.DeepEqual(kinds(evs), want) {
			t.Fatalf("%s: %v, %v; want %v", in, kinds(evs), err, want)
		}
		return evs
	}
	step(`{"type":"session.queue_done"}`, SessionQueued)
	step(`{"type":"session.created","session_id":"s1"}`, SessionReady)
	step(`{"type":"response.output.delta","kind":"text","text":"Hello."}`, ResponseStarted, VoiceDelta)
	step(`{"type":"response.output.delta","kind":"audio","audio":"`+float32B64(0.5)+`"}`, AudioDelta)
	if evs := step(`{"type":"response.output.delta","kind":"listen"}`, VoiceTranscript, AudioDone, ResponseDone); evs[0].Text != "Hello." {
		t.Fatalf("line %q", evs[0].Text)
	}
	step(`{"type":"response.done","response_id":"r2","audio":"`+float32B64(0.1)+`"}`, ResponseStarted, AudioDelta, AudioDone, ResponseDone)
}

// Dial waits until the server lets the client in before it sends
// session.init, then for session.created.
func TestMiniCPMOWaitsForItsQueueBeforeTheSession(t *testing.T) {
	f := newFakeServer(t)
	p, err := MiniCPMO(f.url, func() string { return "tok" })
	if err != nil {
		t.Fatal(err)
	}
	dialed := make(chan error, 1)
	var conn *Conn
	go func() {
		c, err := Dial(context.Background(), p, testSession)
		conn = c
		dialed <- err
	}()
	if h := <-f.header; h.Get("Authorization") != "Bearer tok" {
		t.Fatalf("Authorization %q", h.Get("Authorization"))
	}
	select {
	case m := <-f.got:
		t.Fatalf("the client sent %v before the queue let it in", m)
	default:
	}
	f.reply <- `{"type":"session.queue_done"}`
	if m := <-f.got; m["type"] != "session.init" || field(m, "payload", "system_prompt") != "Be Voice." {
		t.Fatalf("got %v, want session.init with the instructions", m)
	}
	f.reply <- `{"type":"session.created","session_id":"s1"}`
	select {
	case err := <-dialed:
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	case <-time.After(waitLimit):
		t.Fatal("Dial did not return after session.created")
	}
}

// MiniCPM-o takes no client replies: a Dyna item is unspoken at once and
// nothing goes to the server for it.
func TestCallReportsDynaItemsUnspokenOnMiniCPMO(t *testing.T) {
	f := newFakeServer(t)
	p, err := MiniCPMO(f.url, func() string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	f.reply <- `{"type":"session.queue_done"}`
	f.reply <- `{"type":"session.created"}`
	conn, err := Dial(context.Background(), p, testSession)
	if err != nil {
		t.Fatal(err)
	}
	<-f.got // session.init
	cf := runCallOn(t, f, conn, nil)
	cf.call.Speak("m1", "", "CI is green.")
	if e := cf.pageGets(t, "unspoken"); e.Post != "m1" {
		t.Fatalf("unspoken %q", e.Post)
	}
	cf.call.FromPage(PageMessage{Type: "audio", Audio: make([]byte, miniCPMOChunk*3/2)}) // 1 s at 24 kHz is 1 s at 16 kHz
	select {
	case m := <-f.got:
		if m["type"] != "input.append" {
			t.Fatalf("got %v, want only the audio chunk", m)
		}
	case <-time.After(waitLimit):
		t.Fatal("the audio chunk never came")
	}
}

// A short request is heard: on mute, audio short of a chunk goes out padded
// with silence. While muted the stepped model gets a second of silence each
// second, so it can finish its reply; unmuting stops that.
func TestCallFlushesAndStepsMiniCPMOWhenMuted(t *testing.T) {
	f := newFakeServer(t)
	p, err := MiniCPMO(f.url, func() string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	f.reply <- `{"type":"session.queue_done"}`
	f.reply <- `{"type":"session.created"}`
	conn, err := Dial(context.Background(), p, testSession)
	if err != nil {
		t.Fatal(err)
	}
	<-f.got // session.init
	cf := runCallOn(t, f, conn, nil)
	next := func(what string) map[string]any {
		t.Helper()
		select {
		case m := <-f.got:
			if m["type"] != "input.append" {
				t.Fatalf("%s: got %v", what, m)
			}
			return m
		case <-time.After(waitLimit):
			t.Fatalf("%s: the server got nothing", what)
			return nil
		}
	}
	cf.call.FromPage(PageMessage{Type: "audio", Audio: make([]byte, 24000)}) // 0.5 s: short of a chunk
	cf.call.FromPage(PageMessage{Type: "mute", Muted: true})
	next("the owner's half second, padded")
	tick := cf.timer(t, is(time.Second))
	tick()
	next("a second of silence")
	again := cf.timer(t, is(time.Second))
	cf.call.FromPage(PageMessage{Type: "mute", Muted: false})
	again() // unmuted: nothing
	cf.call.FromPage(PageMessage{Type: "audio", Audio: make([]byte, miniCPMOChunk*3/2)})
	if m := next("the owner's next second"); m == nil {
		t.Fatal("no audio")
	}
}

// MiniCPM-o takes no client replies, so a guarded reply is dropped from the
// record but never asks for a correction: nothing waits on the slot.
func TestCallNeverAsksMiniCPMOToCorrectAReply(t *testing.T) {
	f := newFakeServer(t)
	p, err := MiniCPMO(f.url, func() string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	f.reply <- `{"type":"session.queue_done"}`
	f.reply <- `{"type":"session.created"}`
	conn, err := Dial(context.Background(), p, testSession)
	if err != nil {
		t.Fatal(err)
	}
	<-f.got // session.init
	cf := runCallOn(t, f, conn, nil)
	// The decoded events and the call's next step run in one turn of the
	// call's loop, so the state read below sees all of them.
	var requested bool
	var phase string
	done := make(chan struct{})
	cf.call.do(func() {
		for _, in := range []string{
			`{"type":"response.output.delta","kind":"text","text":"<background_work_progress>x</background_work_progress>"}`,
			`{"type":"response.output.delta","kind":"listen"}`,
		} {
			evs, err := p.Protocol.(*MiniCPMOProtocol).DecodeAll([]byte(in))
			if err != nil {
				t.Error(err)
			}
			for _, ev := range evs {
				cf.call.event(context.Background(), ev)
				cf.call.next()
			}
		}
		requested, phase = cf.call.requested, cf.call.slot.phase
		close(done)
	})
	<-done
	if requested || phase != "" {
		t.Fatalf("after the guarded reply: requested %v, slot %q; want neither", requested, phase)
	}
	select {
	case l := <-cf.lines:
		t.Fatalf("the guarded reply was recorded: %+v", l)
	default:
	}
}
