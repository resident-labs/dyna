package realtime

// Ported from qwen-audio-agent server/src/voice/providers/gpt-live.mjs.

import (
	"context"
	"net/http"
)

// Credentials gives the bearer token and ChatGPT account ID of one connection.
type Credentials func(context.Context) (token, account string, err error)

// ChatGPTHeader is the header OpenAI expects from a ChatGPT sign-in. The
// originator value is the client name the provider checks for that grant.
func ChatGPTHeader(token, account string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	h.Set("ChatGPT-Account-ID", account)
	h.Set("originator", "codex_cli_rs")
	return h
}

// OpenAITranscriber is the model that writes the owner's speech as text.
const OpenAITranscriber = "gpt-4o-mini-transcribe"

// OpenAI is OpenAI Realtime at url, which names the model in its query,
// signed in with creds.
func OpenAI(url string, creds Credentials) Provider {
	return Provider{
		Key:       "openai",
		Label:     "OpenAI Realtime",
		Aliases:   []string{"gpt-live", "gpt-realtime"},
		InputRate: 24000, OutputRate: 24000,
		Caps:     Capabilities{FunctionCalling: true, AcknowledgesSessionUpdate: true, ImageInput: true, ManualCommit: true, VADEagerness: true, ManualTurns: true, OutputSpeed: true, ResponseInstructions: true},
		Protocol: GA{},
		Endpoint: func(ctx context.Context) (string, http.Header, error) {
			token, account, err := creds(ctx)
			if err != nil {
				return "", nil, err
			}
			return url, ChatGPTHeader(token, account), nil
		},
		Session: openAISession,
		ContentRefused: func(code, message string) bool {
			return openAIRefusal.MatchString(code) || openAIRefusal.MatchString(message)
		},
	}
}

func openAISession(s Session) any {
	pcm := msg{"type": "audio/pcm", "rate": 24000}
	output := msg{"format": pcm}
	if s.Voice != "" {
		output["voice"] = s.Voice
	}
	// speed is 0.25 to 1.5. Zero keeps the provider default, which is 1.
	if s.Speed != 0 {
		output["speed"] = s.Speed
	}
	return msg{
		"type":              "realtime",
		"instructions":      s.Instructions,
		"tools":             gaTools(s.Tools),
		"output_modalities": []string{"audio"},
		"audio": msg{
			"input": msg{
				"format":          pcm,
				"transcription":   msg{"model": OpenAITranscriber, "prompt": s.TranscriptionPrompt, "language": s.Language},
				"noise_reduction": msg{"type": "near_field"},
				// The call creates and cancels every response itself, so a
				// Dyna item never races the owner's turn.
				// eagerness is low, medium, high or auto. Empty keeps the
				// provider default, which waits longer than high.
				// Manual turns set this to null, so the server does not end
				// a turn or interrupt a reply. The client commits and interrupts.
				"turn_detection": openAITurnDetection(s),
			},
			"output": output,
		},
	}
}

// openAITurnDetection is semantic VAD with Dyna's response flags, or null
// when the session owns the turn. Null is explicit: omitting the field would
// keep the provider's own detection. eagerness is set only when the session
// names one, so the default session stays as the provider defines it.
func openAITurnDetection(s Session) any {
	if s.ManualTurns {
		return nil
	}
	turn := msg{"type": "semantic_vad", "create_response": false, "interrupt_response": false}
	if s.VADEagerness != "" {
		turn["eagerness"] = s.VADEagerness
	}
	return turn
}

// gaTools lists tools in the GA form: flat function objects.
func gaTools(tools []Tool) []msg {
	out := []msg{}
	for _, t := range tools {
		out = append(out, msg{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Parameters})
	}
	return out
}
