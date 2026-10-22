package voice

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/voice/realtime"
)

// prompt is Voice's instructions for a call.
//
//go:embed prompt.md
var prompt string

// RealtimeModel is the OpenAI Realtime model Voice uses.
const RealtimeModel = "gpt-realtime-2.1"

// RealtimeVoice is Codex's default realtime voice
// (codex-rs/protocol/src/protocol.rs).
const RealtimeVoice = "marin"

// openAIRealtime is OpenAI's Realtime endpoint for Voice's model.
const openAIRealtime = "wss://api.openai.com/v1/realtime?model=" + RealtimeModel

// Providers are the realtime voice services a call can use.
// speechToSpeechURL is the address of a self-hosted speech-to-speech server.
func Providers(speechToSpeechURL string) *realtime.Registry {
	return realtime.NewRegistry(realtime.OpenAI(openAIRealtime, CodexCredentials), realtime.SpeechToSpeech(speechToSpeechURL))
}

// CallSession is Voice's session for a call in the room of t. call is the
// call's context.
func CallSession(call context.Context, t *Tools) realtime.Session {
	return realtime.Session{
		Instructions:        prompt,
		Tools:               t.Realtime(call),
		Voice:               RealtimeVoice,
		TranscriptionPrompt: TranscriptionPrompt,
		Language:            "en",
	}
}

var (
	noParams     = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	promptParams = json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","description":"The owner's request, in their exact words."}},"required":["prompt"],"additionalProperties":false}`)
)

// Realtime follows Codex's two conversational tools. Execution and owner
// question answers go through root; remaining silent has no visible effect.
func (t *Tools) Realtime(call context.Context) []realtime.Tool {
	return []realtime.Tool{
		{
			Name:        "dispatch",
			Description: "Send the owner's exact words to the execution agent. Use this by default for work, status checks, question answers, and requests for later work. An idle agent starts work; a running agent receives steering. Do not paraphrase or promise work without calling this tool.",
			Parameters:  promptParams,
			Later:       true,
			Run: func(_ context.Context, args json.RawMessage) (string, error) {
				var a struct{ Prompt string }
				if err := json.Unmarshal(args, &a); err != nil {
					return "", errors.New("the arguments are not JSON")
				}
				return t.Dispatch(call, a.Prompt)
			},
		},
		{
			Name:        "remain_silent",
			Description: "Say nothing when an acknowledgement would distract, a control message needs no reply, or updates add nothing useful. This tool has no user-visible effect.",
			Parameters:  noParams,
			Silent:      true,
			Run:         func(context.Context, json.RawMessage) (string, error) { return "", nil },
		},
	}
}

// startCall connects Voice for the room of link over OpenAI Realtime.
func startCall(ctx context.Context, link *RoomLink) (*session, error) {
	p, err := chosenProvider(settings.Load)
	if err != nil {
		return nil, err
	}
	return runCall(ctx, link, p)
}

// chosenProvider is the provider the voice settings choose: voice.provider,
// and for speech-to-speech its server's address.
func chosenProvider(load func(string) (settings.Values, error)) (realtime.Provider, error) {
	v, err := load(settings.DefaultPath())
	if err != nil {
		return realtime.Provider{}, err
	}
	switch v.VoiceProvider {
	case "speech-to-speech":
		if v.VoiceURL == "" {
			return realtime.Provider{}, errors.New("voice.provider is speech-to-speech; set voice.url to its server's ws:// address")
		}
	case "dashscope":
		model := v.VoiceModel
		if model == "" {
			model = realtime.DashScopeModels[0].ID
		}
		return realtime.DashScope(model, v.VoiceURL, func() string { return os.Getenv("DASHSCOPE_API_KEY") })
	case "google-live":
		return realtime.GoogleLive(v.VoiceModel, v.VoiceURL, realtime.GoogleKey)
	case "doubao-seeduplex":
		return realtime.Doubao(v.VoiceModel, v.VoiceURL, realtime.DoubaoKey)
	case "stepfun":
		return realtime.StepFun(v.VoiceModel, v.VoiceURL, realtime.StepFunKey)
	case "minicpm-o":
		return realtime.MiniCPMO(v.VoiceURL, realtime.MiniCPMOToken)
	}
	return Providers(v.VoiceURL).Lookup(v.VoiceProvider)
}

// runCall connects Voice for the room of link over p and runs the call
// until ctx ends, the owner hangs up or the connection fails. Root's
// @voice-call posts and its questions to the owner go into the call's speak
// queue; the transcript goes to the room. When the call ends, so does its
// watch on the room.
func runCall(ctx context.Context, link *RoomLink, p realtime.Provider) (*session, error) {
	started := time.Now()
	ctx, end := context.WithCancel(ctx)
	s := CallSession(ctx, NewTools(link))
	conn, err := realtime.Dial(ctx, p, s)
	if err != nil {
		end()
		return nil, err
	}
	call := realtime.NewCall(conn, s.Tools, func(owner bool, text string) {
		if text = strings.TrimSpace(text); text == "" || hallucinated(text) {
			return
		}
		if err := link.Spoken(owner, text); err != nil {
			fmt.Fprintln(os.Stderr, "voice: could not record the transcript:", err)
		}
	})
	call.Greeting = "[BACKEND] The call has started. Greet the owner briefly once. If a request or question is pending, include its key point."
	call.Redial = func(ctx context.Context) (*realtime.Conn, error) { return realtime.Dial(ctx, p, s) }
	link.joinRequestedCall(call)
	sess := &session{call: call, done: make(chan struct{})}
	go func() {
		if err := link.Watch(ctx, started, call); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "voice: could not follow root's posts:", err)
		}
	}()
	go func() {
		defer close(sess.done)
		sess.err = call.Run(ctx)
		end()
	}()
	return sess, nil
}

// tellPrompt gives Voice context for a concise update. Shared files show
// in the call, where the owner opens them; Voice cannot see their contents.
func tellPrompt(text string, shared []room.Attachment) string {
	var parts []string
	if strings.TrimSpace(text) != "" || len(shared) == 0 {
		parts = append(parts, "[BACKEND] "+text)
	}
	if len(shared) > 0 {
		names := make([]string, len(shared))
		for i, a := range shared {
			names[i] = sharedName(a)
		}
		parts = append(parts, "Shared in the call, where the owner can open it: "+strings.Join(names, "; ")+`. Mention newly shared items briefly. You cannot see inside them, so never describe what they hold.`)
	}
	return strings.Join(parts, "\n\n")
}

// sharedName names a shared file by what it is and its title.
func sharedName(a room.Attachment) string {
	switch {
	case a.Render == room.RenderHTML:
		return fmt.Sprintf("a card titled %q", cmp.Or(a.Title, a.Name))
	case a.Image():
		return fmt.Sprintf("an image named %q", a.Name)
	}
	return fmt.Sprintf("a file named %q", a.Name)
}
