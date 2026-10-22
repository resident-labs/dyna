package realtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// StepFun's response.create names only its modalities; response.cancelled
// ends a response; thinking is not speech; a response event without its ID
// takes the active response's.
func TestStepFunSpeaksTheBetaWireItsWay(t *testing.T) {
	p := &StepFunProtocol{Beta: Beta{Modalities: []string{"text", "audio"}}}
	p.SessionUpdate(nil)
	if got, _ := json.Marshal(p.ResponseCreate("m1")); string(got) != `{"response":{"modalities":["text","audio"]},"type":"response.create"}` {
		t.Fatalf("response.create %s", got)
	}
	decode := func(in string) Event {
		t.Helper()
		ev, err := p.Decode([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}
	if ev := decode(`{"type":"response.created","response":{"id":"r1"}}`); ev.Kind != ResponseStarted || ev.ResponseID != "r1" {
		t.Fatalf("created %+v", ev)
	}
	if ev := decode(`{"type":"response.audio.delta","item_id":"i1","delta":"AQI="}`); ev.Kind != AudioDelta || ev.ResponseID != "r1" {
		t.Fatalf("an audio delta without its ID %+v, want r1's", ev)
	}
	if ev := decode(`{"type":"response.thinking.delta","delta":"hmm"}`); ev.Kind != Activity || ev.ResponseID != "r1" {
		t.Fatalf("thinking %+v, want r1's activity with nothing to say", ev)
	}
	if ev := decode(`{"type":"response.cancelled"}`); ev.Kind != ResponseDone || ev.ResponseID != "r1" || ev.Text != "cancelled" {
		t.Fatalf("cancelled %+v", ev)
	}
	if ev := decode(`{"type":"response.audio.delta","delta":"AQI="}`); ev.ResponseID != "" {
		t.Fatalf("a delta after the response ended took %q", ev.ResponseID)
	}
}

func TestStepFunSignsInAndSetsItsSession(t *testing.T) {
	f := newFakeServer(t)
	p, err := StepFun("", f.url, func() string { return "sf" })
	if err != nil {
		t.Fatal(err)
	}
	f.reply <- `{"type":"session.updated"}`
	c, err := Dial(context.Background(), p, testSession)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if h := <-f.header; h.Get("Authorization") != "Bearer sf" {
		t.Fatalf("Authorization %q", h.Get("Authorization"))
	}
	s := (<-f.got)["session"].(map[string]any)
	if s["voice"] != nil || s["input_audio_format"] != "pcm16" || field(s, "turn_detection", "type") != "server_vad" {
		t.Fatalf("session %v", s)
	}
	url, _, _ := p.Endpoint(context.Background())
	if !strings.Contains(url, "model=stepaudio-3-realtime-preview") {
		t.Fatalf("url %q", url)
	}
	none, _ := StepFun("", "", func() string { return "" })
	if _, _, err := none.Endpoint(context.Background()); err == nil || !strings.Contains(err.Error(), "STEPFUN_API_KEY") {
		t.Fatalf("no key: %v", err)
	}
}

// A response that keeps thinking is alive: each thinking event starts its
// inactivity guard again, so it is not cancelled as quiet.
func TestCallKeepsAThinkingStepFunResponse(t *testing.T) {
	f := newFakeServer(t)
	p, err := StepFun("", f.url, func() string { return "sf" })
	if err != nil {
		t.Fatal(err)
	}
	cf := startCallOn(t, f, p)
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	first := cf.timer(t, is(ResponseInactivity))
	cf.f.reply <- `{"type":"response.thinking.delta","delta":"hmm"}`
	again := cf.timer(t, is(ResponseInactivity))
	first()    // replaced by the thinking: nothing
	cf.mark(t) // no cancel
	again()
	cf.serverGets(t, "response.cancel")
}
