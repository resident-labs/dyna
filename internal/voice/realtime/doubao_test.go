package realtime

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func float32B64(samples ...float32) string {
	b := make([]byte, 4*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(s))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestDoubaoConvertsFloat32AudioAsQwenDoes(t *testing.T) {
	pcm, err := float32ToPCM16(float32B64(0, 1, -1, 2, 0.5))
	if err != nil {
		t.Fatal(err)
	}
	var got []int16
	for i := 0; i < len(pcm); i += 2 {
		got = append(got, int16(binary.LittleEndian.Uint16(pcm[i:])))
	}
	if want := []int16{0, 32767, -32768, 32767, 16384}; !reflect.DeepEqual(got, want) {
		t.Fatalf("samples %v, want %v", got, want)
	}
	if _, err := float32ToPCM16(base64.StdEncoding.EncodeToString([]byte{1, 2, 3})); err == nil {
		t.Fatal("a broken sample was accepted")
	}
}

// Response events carry no response.created: the first starts one, and
// response.done or response.canceled ends it. Other events read as GA.
func TestDoubaoMapsEventsToOneResponse(t *testing.T) {
	d := &DoubaoProtocol{}
	d.SessionUpdate(nil)
	step := func(in string, want ...Kind) []Event {
		t.Helper()
		evs, err := d.DecodeAll([]byte(in))
		if err != nil || !reflect.DeepEqual(kinds(evs), want) {
			t.Fatalf("%s: %v, %v; want %v", in, kinds(evs), err, want)
		}
		return evs
	}
	step(`{"type":"input_audio_buffer.speech_started"}`, SpeechStarted)
	evs := step(`{"type":"response.output_audio.delta","item_id":"i1","delta":"`+float32B64(0.5)+`"}`, ResponseStarted, AudioDelta)
	if evs[1].ItemID != "i1" || evs[1].ResponseID != evs[0].ResponseID || len(evs[1].Audio) != 2 {
		t.Fatalf("events %+v", evs)
	}
	step(`{"type":"response.output_audio.done","item_id":"i1"}`, AudioDone)
	if evs := step(`{"type":"response.done"}`, ResponseDone); evs[0].Text != "completed" {
		t.Fatalf("status %q", evs[0].Text)
	}
	step(`{"type":"response.function_call_arguments.done","items":[{"call_id":"c1","name":"check_agents"},{"call_id":"c2","name":"check_pr","arguments":"{\"number\":7}"}]}`, ResponseStarted, ToolCall, ToolCall)
	if evs := step(`{"type":"response.canceled"}`, ResponseDone); evs[0].Text != "cancelled" {
		t.Fatalf("a canceled response ends %q", evs[0].Text)
	}
	step(`{"type":"error","error":{"message":"audit failed"}}`, Error)
}

// A Dyna item is a text turn, sent as speech_text_buffer.commit; a reply
// with no text turn sends nothing, since Seeduplex replies by itself.
func TestDoubaoSendsTextTurnsAndToolOutputs(t *testing.T) {
	d := &DoubaoProtocol{}
	d.SessionUpdate(nil)
	if d.UserText("CI is green.") != nil {
		t.Fatal("the text turn went out before its reply")
	}
	if got, _ := json.Marshal(d.ResponseCreate("m1")); string(got) != `{"text":"CI is green.","type":"speech_text_buffer.commit"}` {
		t.Fatalf("reply %s", got)
	}
	if d.ResponseCreate("") != nil {
		t.Fatal("a reply with no text turn sent something")
	}
	out, _ := json.Marshal(d.FunctionOutput("c1", `{"result":"ok"}`))
	if want := `{"items":[{"call_id":"c1","content":[{"text":"{\"result\":\"ok\"}","type":"input_text"}],"role":"tool"}],"type":"conversation.item.create"}`; string(out) != want {
		t.Fatalf("tool output %s, want %s", out, want)
	}
}

// The owner's mute goes to Seeduplex as mute and unmute, on every change,
// and again after a redial.
func TestCallMutesDoubaoAsTheServiceAsks(t *testing.T) {
	f := newFakeServer(t)
	p, err := Doubao("", f.url, func() string { return "dk" })
	if err != nil {
		t.Fatal(err)
	}
	conn, err := Dial(context.Background(), p, testSession)
	if err != nil {
		t.Fatal(err)
	}
	if h := <-f.header; h.Get("X-Api-Key") != "dk" {
		t.Fatalf("X-Api-Key %q", h.Get("X-Api-Key"))
	}
	if m := <-f.got; m["type"] != "session.create" || field(m, "session", "audio", "output", "voice") != DoubaoVoice {
		t.Fatalf("first message %v", m)
	}
	cf := runCallOn(t, f, conn, nil)
	cf.redialTo(func(ctx context.Context) (*Conn, error) { return Dial(ctx, p, testSession) })
	next := func() map[string]any {
		t.Helper()
		select {
		case m := <-f.got:
			return m
		case <-time.After(waitLimit):
			t.Fatal("the server got nothing")
			return nil
		}
	}
	cf.call.FromPage(PageMessage{Type: "mute", Muted: true})
	if m := next(); m["type"] != "input_audio_mute.commit" {
		t.Fatalf("got %v, want the mute", m)
	}
	cf.call.FromPage(PageMessage{Type: "mute", Muted: true}) // no change: nothing
	cf.call.FromPage(PageMessage{Type: "mute", Muted: false})
	if m := next(); m["type"] != "input_audio_unmute.commit" {
		t.Fatalf("got %v, want the unmute", m)
	}
	cf.call.FromPage(PageMessage{Type: "mute", Muted: true})
	next()
	cf.f.closeAll()
	cf.pageState(t, "reconnecting")
	cf.timer(t, isBackoff)()
	<-f.header
	if m := next(); m["type"] != "session.create" {
		t.Fatalf("got %v, want the session again", m)
	}
	if m := next(); m["type"] != "input_audio_mute.commit" {
		t.Fatalf("got %v, want the mute again on the new connection", m)
	}
	cf.call.Speak("m1", "", "CI is green.")
	if m := next(); m["type"] != "speech_text_buffer.commit" || !strings.HasSuffix(m["text"].(string), "CI is green.") {
		t.Fatalf("got %v, want the item as a text turn", m)
	}
}

// Seeduplex's reply text is what Voice says: its deltas are Voice's words
// as they come, on the reply's audio item, and its final text is Voice's
// line, also when the text comes before the audio.
func TestDoubaoKeepsVoicesWords(t *testing.T) {
	d := &DoubaoProtocol{}
	d.SessionUpdate(nil)
	decode := func(in string) []Event {
		t.Helper()
		evs, err := d.DecodeAll([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}
	evs := decode(`{"type":"response.output_text.delta","delta":"CI is green. "}`) // before any audio
	evs = append(evs, decode(`{"type":"response.output_text.delta","delta":"The queue is next."}`)...)
	evs = append(evs, decode(`{"type":"response.output_audio.delta","delta":"`+float32B64(0.5)+`"}`)...)
	evs = append(evs, decode(`{"type":"response.output_text.done"}`)...)
	if want := []Kind{ResponseStarted, VoiceDelta, VoiceDelta, AudioDelta, VoiceTranscript}; !reflect.DeepEqual(kinds(evs), want) {
		t.Fatalf("kinds %v, want %v", kinds(evs), want)
	}
	if evs[1].ItemID != evs[3].ItemID || evs[1].Text != "CI is green. " {
		t.Fatalf("text and audio on items %q and %q, %+v", evs[1].ItemID, evs[3].ItemID, evs[1])
	}
	if evs[4].Text != "CI is green. The queue is next." {
		t.Fatalf("final line %q, want the deltas joined", evs[4].Text)
	}
	if evs := decode(`{"type":"response.output_text.done","text":"Given in full."}`); evs[0].Text != "Given in full." {
		t.Fatalf("final line %q, want the done event's text", evs[0].Text)
	}
}
