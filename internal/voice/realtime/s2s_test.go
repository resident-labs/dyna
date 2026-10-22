package realtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// s2sDial connects with the speech-to-speech adapter and returns the server
// and the session.update it got. The fake never replies, so Dial must not
// wait for session.updated. The deadline only turns a wrong wait into a fast
// failure instead of a hang.
func s2sDial(t *testing.T) (*fakeServer, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f := newFakeServer(t)
	c, err := Dial(ctx, SpeechToSpeech(f.url), testSession)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return f, <-f.got
}

func TestSpeechToSpeechDialDoesNotWaitForSessionUpdated(t *testing.T) {
	_, update := s2sDial(t) // the fake sends nothing back
	if update["type"] != "session.update" {
		t.Fatalf("first message = %v, want session.update", update)
	}
}

func TestSpeechToSpeechSessionUsesTheNativeInputPipeline(t *testing.T) {
	_, update := s2sDial(t)
	s := update["session"]
	for _, c := range []struct {
		path []string
		want any
	}{
		{[]string{"type"}, "realtime"},
		{[]string{"instructions"}, "Be Voice."},
		{[]string{"audio", "input", "turn_detection", "type"}, "server_vad"},
		{[]string{"audio", "input", "turn_detection", "create_response"}, false},
		{[]string{"audio", "input", "turn_detection", "interrupt_response"}, false},
		{[]string{"audio", "output", "format", "type"}, "audio/pcm"},
		{[]string{"audio", "output", "format", "rate"}, float64(24000)},
	} {
		if got := field(s, c.path...); got != c.want {
			t.Errorf("session.%s = %v, want %v", strings.Join(c.path, "."), got, c.want)
		}
	}
	if got := field(s, "audio", "input", "format"); got != nil {
		t.Errorf("session.audio.input.format = %v, want none: speech-to-speech takes its native 16 kHz input", got)
	}
	if got := mustMarshal(field(s, "output_modalities")); got != `["audio"]` {
		t.Errorf("session.output_modalities = %s, want [\"audio\"]", got)
	}
	tools, _ := field(s, "tools").([]any)
	if len(tools) != 1 ||
		field(tools[0], "type") != "function" ||
		field(tools[0], "name") != "agents" ||
		field(tools[0], "description") != "Who works." ||
		mustMarshal(field(tools[0], "parameters")) != `{"properties":{},"type":"object"}` {
		t.Errorf("session.tools = %v, want the one flat agents function", tools)
	}
}

func TestSpeechToSpeechSendsNoAuthorizationHeader(t *testing.T) {
	f, _ := s2sDial(t)
	if got := (<-f.header).Get("Authorization"); got != "" {
		t.Errorf("Authorization header = %q, want none: the endpoint is self-hosted", got)
	}
}

func TestRegistryFindsSpeechToSpeech(t *testing.T) {
	r := NewRegistry(SpeechToSpeech("ws://127.0.0.1:1"))
	for _, name := range []string{"speech-to-speech", "s2s"} {
		if p, err := r.Lookup(name); err != nil || p.Key != "speech-to-speech" {
			t.Errorf("Lookup(%q) = %q, %v; want the speech-to-speech provider", name, p.Key, err)
		}
	}
}

// The page sends 24 kHz audio; speech-to-speech takes 16 kHz, so the call
// resamples it. OpenAI takes 24 kHz and gets the page's bytes unchanged.
func TestCallResamplesThePagesAudioToTheProvidersRate(t *testing.T) {
	pcm := func(samples ...int16) []byte {
		b := make([]byte, 2*len(samples))
		for i, s := range samples {
			binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
		}
		return b
	}
	page := pcm(0, 300, 600, 900, 1200, 1500) // 6 samples at 24 kHz
	if got, want := resample(page, PageRate, 16000), pcm(0, 450, 900, 1350); !bytes.Equal(got, want) {
		t.Fatalf("16 kHz = %v, want %v", got, want)
	}
	if got := resample(page, PageRate, 24000); !bytes.Equal(got, page) {
		t.Fatalf("24 kHz changed the audio: %v", got)
	}
	f := newFakeServer(t)
	f.reply <- `{"type":"session.created"}`
	p := SpeechToSpeech(f.url)
	conn, err := Dial(context.Background(), p, testSession)
	if err != nil {
		t.Fatal(err)
	}
	<-f.got // session.update
	call := NewCall(conn, nil, func(bool, string) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go call.Run(ctx)
	call.FromPage(PageMessage{Type: "audio", Audio: page})
	m := <-f.got
	got, _ := base64.StdEncoding.DecodeString(m["audio"].(string))
	if m["type"] != "input_audio_buffer.append" || !bytes.Equal(got, pcm(0, 450, 900, 1350)) {
		t.Fatalf("server got %v (%v), want the audio at 16 kHz", m["type"], got)
	}
}
