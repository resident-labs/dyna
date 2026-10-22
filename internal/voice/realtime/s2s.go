package realtime

// Ported from qwen-audio-agent server/src/voice/providers/s2s.mjs.

import (
	"context"
	"net/http"
	"time"
)

// SpeechToSpeech is a user-managed huggingface/speech-to-speech endpoint at
// url. The upstream process owns the model, transcription and voice choices;
// this profile only describes its OpenAI Realtime wire contract. The endpoint
// is self-hosted, so it needs no key.
func SpeechToSpeech(url string) Provider {
	return Provider{
		Key:       "speech-to-speech",
		Label:     "Speech-to-Speech",
		Aliases:   []string{"s2s"},
		InputRate: 16000, OutputRate: 24000,
		Caps: Capabilities{
			FunctionCalling:    true,
			SingleResponseSlot: true,
			// speech-to-speech applies session.update without sending
			// session.updated, so Dial must not wait for one.
			AcknowledgesSessionUpdate: false,
		},
		Protocol: GA{},
		Endpoint: func(context.Context) (string, http.Header, error) {
			return url, http.Header{}, nil
		},
		Session: speechToSpeechSession,
		// The upstream pipeline transcribes, thinks and speaks in turn, so
		// its first audio comes late (qwen allows 60 s).
		ResponseStartTimeout: 60 * time.Second,
	}
}

func speechToSpeechSession(s Session) any {
	return msg{
		"type":              "realtime",
		"instructions":      s.Instructions,
		"tools":             gaTools(s.Tools),
		"output_modalities": []string{"audio"},
		"audio": msg{
			// speech-to-speech treats an omitted input format as its native
			// 16 kHz PCM pipeline rate. The GA schema accepts only an explicit
			// 24 kHz format, so declaring 16 kHz would make the whole
			// session.update invalid.
			// The engine call creates and cancels every response, so the
			// server must not open its own response at a VAD turn end and
			// must not interrupt on barge-in.
			"input": msg{
				"turn_detection": msg{"type": "server_vad", "create_response": false, "interrupt_response": false},
			},
			// The client asks for the shared 24 kHz playback rate. The
			// upstream service resamples its internal 16 kHz output.
			"output": msg{
				"format": msg{"type": "audio/pcm", "rate": 24000},
			},
		},
	}
}
