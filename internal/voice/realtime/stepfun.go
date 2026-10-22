package realtime

// Ported from qwen-audio-agent server/src/voice/providers/stepfun.mjs and
// stepfun-protocol.mjs (Apache-2.0).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

// StepFunURL and StepFunModel are qwen-audio-agent 2.0.1's defaults.
const (
	StepFunURL   = "wss://api.stepfun.com/v1/realtime"
	StepFunModel = "stepaudio-3-realtime-preview"
)

// StepFun is StepFun's realtime service for model at endpoint
// (StepFunModel and StepFunURL when empty), signed in with the key key
// returns.
func StepFun(model, endpoint string, key func() string) (Provider, error) {
	if model == "" {
		model = StepFunModel
	}
	if endpoint == "" {
		endpoint = StepFunURL
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return Provider{}, fmt.Errorf("the StepFun endpoint %q is not a ws:// or wss:// address", endpoint)
	}
	q := u.Query()
	q.Set("model", model)
	u.RawQuery = q.Encode()
	full := u.String()
	return Provider{
		Key:       "stepfun",
		Label:     "StepFun Realtime",
		InputRate: 24000, OutputRate: 24000,
		Caps:     Capabilities{FunctionCalling: true, SingleResponseSlot: true, AcknowledgesSessionUpdate: true},
		Protocol: &StepFunProtocol{Beta: Beta{Modalities: []string{"text", "audio"}}},
		Endpoint: func(context.Context) (string, http.Header, error) {
			k := key()
			if k == "" {
				return "", nil, errors.New("set STEPFUN_API_KEY to use StepFun")
			}
			return full, http.Header{"Authorization": {"Bearer " + k}}, nil
		},
		Session: stepFunSession,
	}, nil
}

// StepFunKey is the API key in STEPFUN_API_KEY.
func StepFunKey() string { return os.Getenv("STEPFUN_API_KEY") }

// stepFunSession is qwen's buildSession, with Dyna's turn flags: the call
// creates and cancels every response. Whether StepFun honors them is
// untested. The voice is the service's own, never an OpenAI one.
func stepFunSession(s Session) any {
	return msg{
		"instructions":        s.Instructions,
		"tools":               gaTools(s.Tools),
		"modalities":          []string{"text", "audio"},
		"input_audio_format":  "pcm16",
		"output_audio_format": "pcm16",
		"turn_detection":      msg{"type": "server_vad", "create_response": false, "interrupt_response": false},
	}
}

// StepFunProtocol is the beta wire as StepFun speaks it: response.create
// takes no response-level extensions such as tool_choice, a cancelled
// response ends with response.cancelled, thinking deltas are not speech,
// and some response events omit their response ID, which the adapter fills
// from the one active response.
type StepFunProtocol struct {
	Beta
	mu     sync.Mutex
	active string
}

func (p *StepFunProtocol) SessionUpdate(session any) any {
	p.mu.Lock()
	p.active = ""
	p.mu.Unlock()
	return p.Beta.SessionUpdate(session)
}

func (p *StepFunProtocol) ResponseCreate(string) any {
	return msg{"type": "response.create", "response": msg{"modalities": p.Modalities}}
}

func (p *StepFunProtocol) Decode(data []byte) (Event, error) {
	var e struct {
		Type       string `json:"type"`
		ResponseID string `json:"response_id"`
		Response   struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return Event{}, fmt.Errorf("realtime: undecodable event: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	id := e.ResponseID
	if id == "" {
		id = e.Response.ID
	}
	if e.Type == "response.created" {
		p.active = id
	}
	if id == "" {
		id = p.active
	}
	switch {
	case strings.HasPrefix(e.Type, "response.thinking."):
		return Event{Kind: Activity, Type: e.Type, ResponseID: id}, nil // liveness only, not speech
	case e.Type == "response.cancelled":
		if id == p.active {
			p.active = ""
		}
		return Event{Kind: ResponseDone, Type: e.Type, ResponseID: id, Text: "cancelled"}, nil
	}
	ev, err := p.Beta.Decode(data)
	if err != nil {
		return ev, err
	}
	if ev.ResponseID == "" && strings.HasPrefix(e.Type, "response.") {
		ev.ResponseID = id
	}
	if e.Type == "response.done" && id == p.active {
		p.active = ""
	}
	return ev, nil
}
