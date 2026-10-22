package realtime

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestBetaMapsItsAudioEventsAndAsksWithModalities(t *testing.T) {
	b := Beta{Modalities: []string{"text", "audio"}}
	audio := base64.StdEncoding.EncodeToString([]byte{1, 2})
	for in, want := range map[string]Kind{
		`{"type":"response.audio.delta","item_id":"i1","delta":"` + audio + `"}`: AudioDelta,
		`{"type":"response.audio.done","item_id":"i1"}`:                          AudioDone,
		`{"type":"response.audio_transcript.delta","delta":"Hi"}`:                VoiceDelta,
		`{"type":"response.audio_transcript.done","transcript":"Hi."}`:           VoiceTranscript,
		`{"type":"input_audio_buffer.committed"}`:                                Committed,
	} {
		ev, err := b.Decode([]byte(in))
		if err != nil || ev.Kind != want {
			t.Errorf("%s: kind %v, %v; want %v", in, ev.Kind, err, want)
		}
	}
	if got := b.ResponseCreate("m1").(msg)["response"].(msg)["tool_choice"]; got != "none" {
		t.Errorf("a Dyna item's reply: tool_choice %v, want none", got)
	}
	r := b.ResponseCreate("").(msg)["response"].(msg)
	if _, ok := r["metadata"]; ok || len(r["modalities"].([]string)) != 2 || r["tool_choice"] != nil {
		t.Errorf("plain reply %v, want modalities only", r)
	}
}

// dashScopeDial connects to a fake server as DashScope with model and
// returns the URL path and query it dialed, its header and the session.
func dashScopeDial(t *testing.T, model string) (string, map[string]any, map[string]any) {
	t.Helper()
	f := newFakeServer(t)
	p, err := DashScope(model, f.url, func() string { return "sk-test" })
	if err != nil {
		t.Fatal(err)
	}
	url, _, _ := p.Endpoint(context.Background())
	f.reply <- `{"type":"session.updated"}`
	c, err := Dial(context.Background(), p, testSession)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if h := <-f.header; h.Get("Authorization") != "Bearer sk-test" {
		t.Fatalf("Authorization %q", h.Get("Authorization"))
	}
	update := <-f.got
	return url, update, update["session"].(map[string]any)
}

func TestDashScopeSessionsFollowEachModel(t *testing.T) {
	url, _, s := dashScopeDial(t, "qwen-audio-3.0-realtime-plus")
	if !strings.Contains(url, "model=qwen-audio-3.0-realtime-plus") {
		t.Errorf("url %q lacks the model", url)
	}
	if s["voice"] != "longanqian" || s["input_audio_format"] != "pcm" || field(s, "turn_detection", "type") != "smart_turn" || field(s, "turn_detection", "create_response") != false {
		t.Errorf("audio session %v", s)
	}
	_, _, s = dashScopeDial(t, "qwen3.5-omni-plus-realtime")
	if s["voice"] != "Ethan" || field(s, "turn_detection", "type") != "semantic_vad" {
		t.Errorf("omni session %v", s)
	}
	_, _, s = dashScopeDial(t, "qwen3.8-omni-flash-realtime") // the fake server is not a public host
	if s["voice"] != nil || field(s, "audio", "output", "voice") != "Tina" || field(s, "audio", "input", "format", "sample_rate") != float64(16000) {
		t.Errorf("nested session %v", s)
	}
	if tools, _ := s["tools"].([]any); len(tools) != 1 {
		t.Errorf("tools %v", s["tools"])
	}
}

func TestDashScopeRefusesWhatQwenRefuses(t *testing.T) {
	if _, err := DashScope("qwen-max", "", func() string { return "k" }); err == nil {
		t.Error("an unknown model was accepted")
	}
	if _, err := DashScope("qwen3.8-omni-flash-realtime", "", func() string { return "k" }); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("a workspace model on the public endpoint: %v", err)
	}
	p, err := DashScope("qwen-audio-3.0-realtime-flash", "", func() string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Endpoint(context.Background()); err == nil || !strings.Contains(err.Error(), "DASHSCOPE_API_KEY") {
		t.Errorf("no key: %v", err)
	}
	if !p.ContentRefused("", "DataInspectionFailed: input data may contain inappropriate content") || p.ContentRefused("", "user is speaking") {
		t.Error("content refusals are not classified as qwen does")
	}
}

// A dialect without response metadata matches a reply to the request sent
// last: its playing counts the item spoken, and the next one goes out.
func TestCallMatchesRepliesInOrderWithoutMetadata(t *testing.T) {
	f := newFakeServer(t)
	p, err := DashScope("qwen-audio-3.0-realtime-plus", f.url, func() string { return "k" })
	if err != nil {
		t.Fatal(err)
	}
	cf := startCallOn(t, f, p)
	cf.call.Speak("m1", "", "CI is green.")
	cf.serverGets(t, "conversation.item.create")
	cf.serverGets(t, "response.create")
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}` // no metadata
	cf.f.reply <- `{"type":"response.audio.delta","response_id":"r1","item_id":"i1","delta":"AQI="}`
	if e := cf.pageGets(t, "audio"); e.ID != "i1" {
		t.Fatalf("audio %+v", e)
	}
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i1", State: "started"})
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"completed"}}`
	cf.call.FromPage(PageMessage{Type: "playback", ID: "i1", State: "ended", PlayedMs: 0})
	cf.call.Speak("m2", "", "The queue is empty.")
	item := cf.serverGets(t, "conversation.item.create")
	content, _ := field(item, "item", "content").([]any)
	if got, _ := field(content[0], "text").(string); got != "The queue is empty." {
		t.Fatalf("server got %q, want m2 alone: m1 counted as spoken", got)
	}
}
