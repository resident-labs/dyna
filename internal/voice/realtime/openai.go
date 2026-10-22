package realtime

// Ported from qwen-audio-agent server/src/voice/providers/gpt-live.mjs.

import (
	"context"
	"net/http"
)

// Credentials gives the bearer token and ChatGPT account ID of one connection.
type Credentials func(context.Context) (token, account string, err error)

// CodexHeader is the header OpenAI expects from a Codex sign-in.
func CodexHeader(token, account string) http.Header {
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
		Caps:     Capabilities{FunctionCalling: true, AcknowledgesSessionUpdate: true},
		Protocol: GA{},
		Endpoint: func(ctx context.Context) (string, http.Header, error) {
			token, account, err := creds(ctx)
			if err != nil {
				return "", nil, err
			}
			return url, CodexHeader(token, account), nil
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
				"turn_detection": msg{"type": "semantic_vad", "create_response": false, "interrupt_response": false},
			},
			"output": output,
		},
	}
}

// gaTools lists tools in the GA form: flat function objects.
func gaTools(tools []Tool) []msg {
	out := []msg{}
	for _, t := range tools {
		out = append(out, msg{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Parameters})
	}
	return out
}
