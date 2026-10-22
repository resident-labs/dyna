package realtime

// Ported from qwen-audio-agent server/src/voice/providers/google-live.mjs
// and google-live-protocol.mjs (Apache-2.0).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GoogleLiveURL is the Gemini Live API endpoint, and GoogleLiveModel the
// model qwen-audio-agent 2.0.1 uses.
const (
	GoogleLiveURL   = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
	GoogleLiveModel = "gemini-3.8-live"
)

// GoogleKey is the API key in GOOGLE_API_KEY, GEMINI_API_KEY or
// GOOGLE_LIVE_API_KEY, the first set, as qwen reads it.
func GoogleKey() string {
	for _, name := range []string{"GOOGLE_API_KEY", "GEMINI_API_KEY", "GOOGLE_LIVE_API_KEY"} {
		if k := os.Getenv(name); k != "" {
			return k
		}
	}
	return ""
}

var googleRefusal = regexp.MustCompile(`(?i)safety|policy|blocked`)

// GoogleLive is Gemini's Live API for model at endpoint (GoogleLiveURL and
// GoogleLiveModel when empty), signed in with the key key returns.
func GoogleLive(model, endpoint string, key func() string) (Provider, error) {
	if model == "" {
		model = GoogleLiveModel
	}
	if endpoint == "" {
		endpoint = GoogleLiveURL
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return Provider{}, fmt.Errorf("the Google Live endpoint %q is not a ws:// or wss:// address", endpoint)
	}
	return Provider{
		Key:       "google-live",
		Label:     "Google Live",
		Aliases:   []string{"google", "gemini-live"},
		InputRate: 16000, OutputRate: 24000,
		Caps:     Capabilities{FunctionCalling: true, SingleResponseSlot: true, AcknowledgesSessionUpdate: true, EndsStreamOnMute: true},
		Protocol: &Google{},
		Endpoint: func(context.Context) (string, http.Header, error) {
			q := u.Query()
			if !q.Has("key") && !q.Has("access_token") {
				k := key()
				if k == "" {
					return "", nil, errors.New("set GOOGLE_API_KEY or GEMINI_API_KEY to use Google Live")
				}
				q.Set("key", k)
			}
			signed := *u
			signed.RawQuery = q.Encode()
			return signed.String(), http.Header{}, nil
		},
		Session: func(s Session) any { return googleSession(model, s) },
		ContentRefused: func(code, message string) bool {
			return googleRefusal.MatchString(code) || googleRefusal.MatchString(message)
		},
	}, nil
}

// googleSession is the setup message's body, as qwen's buildSession writes
// it. The call's voice names an OpenAI voice, so Google's default is used.
func googleSession(model string, s Session) any {
	if !strings.HasPrefix(model, "models/") {
		model = "models/" + model
	}
	var decls []msg
	for _, t := range s.Tools {
		decls = append(decls, msg{"name": t.Name, "description": t.Description, "parameters": t.Parameters})
	}
	tools := []msg{}
	if len(decls) > 0 {
		tools = []msg{{"functionDeclarations": decls}}
	}
	return msg{
		"model":                    model,
		"generationConfig":         msg{"responseModalities": []string{"AUDIO"}},
		"systemInstruction":        msg{"parts": []msg{{"text": s.Instructions}}},
		"tools":                    tools,
		"inputAudioTranscription":  msg{},
		"outputAudioTranscription": msg{},
	}
}

// Google speaks Gemini's Live (BidiGenerateContent) protocol. It has no
// response or item IDs, so it makes them up for each model turn, and one
// server message can carry several events. Its state lasts one connection:
// SessionUpdate starts it afresh. Reads and writes come from two
// goroutines, so a lock guards it.
type Google struct {
	mu        sync.Mutex
	n         int               // makes IDs
	active    string            // the model turn in progress, as a response ID
	cut       bool              // the active turn was interrupted
	automatic bool              // a tool response resumes generation by itself
	names     map[string]string // tool call ID -> its tool's name
	heard     strings.Builder   // the owner's words of the line in progress
	lastHeard time.Time         // when the last of them came
	now       func() time.Time  // the clock; nil means time.Now
	said      strings.Builder   // Voice's words this turn
}

// InputGap is the pause after which the owner's next words start a new
// line.
const InputGap = 1200 * time.Millisecond

func (g *Google) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// heardLine makes the owner's words so far a final line.
func (g *Google) heardLine() Event {
	ev := Event{Kind: OwnerTranscript, Text: g.heard.String()}
	g.heard.Reset()
	return ev
}

// Pending is the owner's line not yet final, which the call records when it
// ends.
func (g *Google) Pending() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	text := g.heard.String()
	g.heard.Reset()
	return text
}

func (g *Google) id(prefix string) string {
	g.n++
	return prefix + "_" + strconv.Itoa(g.n)
}

func (g *Google) SessionUpdate(session any) any {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active, g.cut, g.automatic, g.names = "", false, false, map[string]string{}
	g.heard.Reset()
	g.said.Reset()
	return msg{"setup": session}
}

func (*Google) AudioAppend(pcm []byte) any {
	return msg{"realtimeInput": msg{"audio": msg{"data": base64.StdEncoding.EncodeToString(pcm), "mimeType": "audio/pcm;rate=16000"}}}
}

// AudioCommit ends the owner's audio stream, so the server closes the turn.
func (*Google) AudioCommit() any { return msg{"realtimeInput": msg{"audioStreamEnd": true}} }

// UserText adds text to the conversation without starting a reply.
func (*Google) UserText(text string) any {
	return msg{"clientContent": msg{"turns": []msg{{"role": "user", "parts": []msg{{"text": text}}}}, "turnComplete": false}}
}

func (g *Google) FunctionOutput(callID, output string) any {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.automatic = true // a tool response resumes generation itself
	name := g.names[callID]
	delete(g.names, callID)
	var response any = msg{"result": output}
	var parsed map[string]any
	if json.Unmarshal([]byte(output), &parsed) == nil {
		response = parsed
	}
	return msg{"toolResponse": msg{"functionResponses": []msg{{"id": callID, "name": name, "response": response}}}}
}

// ResponseCreate completes the turn, which starts a reply; after a tool
// response the reply comes by itself, so it sends nothing.
func (g *Google) ResponseCreate(string) any {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.automatic {
		g.automatic = false
		return nil
	}
	return msg{"clientContent": msg{"turnComplete": true}}
}

// ResponseCancel interrupts generation without asking for another reply.
func (*Google) ResponseCancel() any { return msg{"clientContent": msg{"turnComplete": false}} }

// Truncate has no Live equivalent: the server keeps what it generated.
func (*Google) Truncate(string, time.Duration) any { return nil }

func (g *Google) Decode(data []byte) (Event, error) {
	evs, err := g.DecodeAll(data)
	if err != nil || len(evs) == 0 {
		return Event{}, err
	}
	return evs[0], nil
}

type googleEvent struct {
	Error *struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
	SetupComplete *struct{} `json:"setupComplete"`
	ServerContent *struct {
		Interrupted bool `json:"interrupted"`
		ModelTurn   *struct {
			Parts []struct {
				InlineData *struct {
					Data string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"modelTurn"`
		InputTranscription  *struct{ Text string } `json:"inputTranscription"`
		OutputTranscription *struct{ Text string } `json:"outputTranscription"`
		TurnComplete        bool                   `json:"turnComplete"`
	} `json:"serverContent"`
	ToolCall *struct {
		FunctionCalls []struct {
			ID   string          `json:"id"`
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		} `json:"functionCalls"`
	} `json:"toolCall"`
	ToolCallCancellation *struct{} `json:"toolCallCancellation"`
}

// DecodeAll maps one Live server message to Dyna's events.
func (g *Google) DecodeAll(data []byte) ([]Event, error) {
	var e googleEvent
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("realtime: undecodable event: %w", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var evs []Event
	start := func() string {
		if g.active == "" {
			g.active = g.id("response")
			evs = append(evs, Event{Kind: ResponseStarted, ResponseID: g.active})
		}
		return g.active
	}
	finish := func() {
		if g.active == "" {
			return
		}
		status := "completed"
		if g.cut {
			status = "cancelled"
		}
		if g.said.Len() > 0 {
			evs = append(evs, Event{Kind: VoiceTranscript, ResponseID: g.active, ItemID: g.active, Text: g.said.String()})
		}
		evs = append(evs, Event{Kind: AudioDone, ResponseID: g.active, ItemID: g.active}, Event{Kind: ResponseDone, ResponseID: g.active, Text: status})
		g.active, g.cut, g.automatic = "", false, false
		g.said.Reset()
	}
	if e.Error != nil {
		return []Event{{Kind: Error, Text: e.Error.Message, Code: e.Error.Status}}, nil
	}
	if e.SetupComplete != nil {
		return []Event{{Kind: SessionReady}}, nil
	}
	if sc := e.ServerContent; sc != nil {
		if sc.Interrupted {
			g.cut = true
			evs = append(evs, Event{Kind: Interrupted})
		}
		if sc.ModelTurn != nil {
			for _, part := range sc.ModelTurn.Parts {
				if part.InlineData == nil || part.InlineData.Data == "" {
					continue
				}
				pcm, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
				if err != nil {
					return nil, fmt.Errorf("realtime: undecodable audio: %w", err)
				}
				id := start()
				evs = append(evs, Event{Kind: AudioDelta, ResponseID: id, ItemID: id, Audio: pcm})
			}
		}
		if t := sc.InputTranscription; t != nil && t.Text != "" {
			// Live sends input transcription on its own schedule, with no
			// order against model turns and no boundary of its own. Words
			// after a pause of InputGap start a new line; until then, late
			// words still join the line they belong to.
			now := g.clock()
			if g.heard.Len() > 0 && now.Sub(g.lastHeard) >= InputGap {
				evs = append(evs, g.heardLine())
			}
			g.heard.WriteString(t.Text)
			g.lastHeard = now
			evs = append(evs, Event{Kind: OwnerPartial, Text: g.heard.String()})
		}
		if t := sc.OutputTranscription; t != nil && t.Text != "" {
			id := start()
			g.said.WriteString(t.Text)
			evs = append(evs, Event{Kind: VoiceDelta, ResponseID: id, ItemID: id, Text: t.Text})
		}
		// The owner's line is final once the model answers it or the turn
		// ends. Words that come later start a new line.
		if g.heard.Len() > 0 && (g.active != "" || sc.TurnComplete) {
			evs = append(evs, g.heardLine())
		}
		// generationComplete can come before the turn ends; the slot stays
		// taken until the server closes the turn.
		if sc.TurnComplete {
			finish()
		}
	}
	if tc := e.ToolCall; tc != nil && len(tc.FunctionCalls) > 0 {
		id := start()
		for _, call := range tc.FunctionCalls {
			callID := call.ID
			if callID == "" {
				callID = g.id("call")
			}
			g.names[callID] = call.Name
			args := call.Args
			if len(args) == 0 || string(args) == "null" {
				args = json.RawMessage(`{}`)
			}
			evs = append(evs, Event{Kind: ToolCall, ResponseID: id, CallID: callID, Name: call.Name, Args: args})
		}
		finish()
	}
	if e.ToolCallCancellation != nil {
		evs = append(evs, Event{Kind: Error, Text: "Google Live cancelled one or more tool calls."})
	}
	return evs, nil
}
