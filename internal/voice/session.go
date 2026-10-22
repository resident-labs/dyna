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
	noParams     = json.RawMessage(`{"type":"object","properties":{}}`)
	textParams   = json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"The owner's request, in their words."}},"required":["text"]}`)
	numberParams = json.RawMessage(`{"type":"object","properties":{"number":{"type":"integer","description":"The PR number."}},"required":["number"]}`)
	answerParams = json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"The owner's answer, in their words."},"question_id":{"type":"string","description":"The question_id given with the question you read out. Never guess it."}},"required":["text"]}`)
)

// Realtime lists t's tools for the model.
func (t *Tools) Realtime(call context.Context) []realtime.Tool {
	status := func(f func() (string, error)) func(context.Context, json.RawMessage) (string, error) {
		return func(context.Context, json.RawMessage) (string, error) { return f() }
	}
	return []realtime.Tool{
		{
			Name:        "dispatch",
			Description: "Start the owner's request in Dyna, in the owner's words: work, a change, a fix, a decision, or a question no check answers. Returns at once with accepted, or duplicate when the same request is still under way; the answer is spoken in the call when it comes. After it, call no other tool for the same question.",
			Parameters:  textParams,
			Later:       true,
			Run: func(_ context.Context, args json.RawMessage) (string, error) {
				var a struct{ Text string }
				if err := json.Unmarshal(args, &a); err != nil {
					return "", errors.New("the arguments are not JSON")
				}
				return t.Dispatch(call, a.Text)
			},
		},
		{
			Name:        "check_pr",
			Description: "Check one of the room's PRs: its state, CI result, review verdict and merge queue place.",
			Parameters:  numberParams,
			Run: func(_ context.Context, args json.RawMessage) (string, error) {
				var a struct{ Number int }
				if err := json.Unmarshal(args, &a); err != nil || a.Number <= 0 {
					return "", errors.New("say which PR number")
				}
				return t.PRStatus(a.Number)
			},
		},
		{Name: "check_merge_queue", Description: "Check how many of the room's PRs wait to merge or are merging, and which is next.", Parameters: noParams, Run: status(t.MergeQueue)},
		{
			Name:        "answer_question",
			Description: "Answer an open question to the owner, one that was read out in this call, with the owner's words. Use it instead of dispatch when the owner answers that question.",
			Parameters:  answerParams,
			Run: func(_ context.Context, args json.RawMessage) (string, error) {
				var a struct {
					Text       string `json:"text"`
					QuestionID string `json:"question_id"`
				}
				if err := json.Unmarshal(args, &a); err != nil {
					return "", errors.New("the arguments are not JSON")
				}
				return t.AnswerQuestion(call, a.QuestionID, a.Text)
			},
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

// tellPrompt asks Voice to pass on what root says to the owner. The files
// root shared show in the call, where the owner opens them; Voice only says,
// in one short line, that it shared them.
func tellPrompt(text string, shared []room.Attachment) string {
	var parts []string
	if strings.TrimSpace(text) != "" || len(shared) == 0 {
		parts = append(parts, "A work result for the owner. Tell the owner in your own words, in one to three short sentences, as your own work: say \"I\", and keep what was done accurate: "+text)
	}
	if len(shared) > 0 {
		names := make([]string, len(shared))
		for i, a := range shared {
			names[i] = sharedName(a)
		}
		parts = append(parts, "Shared in the call, where the owner can open it: "+strings.Join(names, "; ")+`. Say one short line that you shared it, such as "I sent you a comparison card." You cannot see inside it, so never describe what it holds.`)
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
