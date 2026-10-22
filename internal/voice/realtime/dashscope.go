package realtime

// Ported from qwen-audio-agent server/src/voice/providers/dashscope.mjs and
// shared/realtime-model-catalog.mjs (Apache-2.0).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
)

// DashScopeURL is DashScope's public realtime endpoint.
const DashScopeURL = "wss://dashscope.aliyuncs.com/api-ws/v1/realtime"

// DashScopeModel is one DashScope realtime model, as qwen's catalog lists
// it.
type DashScopeModel struct {
	ID, Label string
	Omni      bool   // the Omni family; else the Audio family
	Voice     string // the model's default voice
	Turn      string // the model's turn detection type
	// Nested: the model takes its audio formats and voice in the GA-like
	// nested audio object, not as flat beta fields (Qwen3.8 Omni).
	Nested bool
	// Workspace: the model needs a Model Studio workspace endpoint, not a
	// public one.
	Workspace bool
}

// DashScopeModels are the models qwen-audio-agent 2.0.1 supports, the
// default first.
var DashScopeModels = []DashScopeModel{
	{ID: "qwen-audio-3.0-realtime-plus", Label: "Qwen Audio 3.0 Realtime Plus", Voice: "longanqian", Turn: "smart_turn"},
	{ID: "qwen-audio-3.0-realtime-flash", Label: "Qwen Audio 3.0 Realtime Flash", Voice: "longanqian", Turn: "smart_turn"},
	{ID: "qwen3.5-omni-flash-realtime", Label: "Qwen3.5 Omni Flash Realtime", Omni: true, Voice: "Ethan", Turn: "semantic_vad"},
	{ID: "qwen3.5-omni-plus-realtime", Label: "Qwen3.5 Omni Plus Realtime", Omni: true, Voice: "Ethan", Turn: "semantic_vad"},
	{ID: "qwen3.8-omni-flash-realtime", Label: "Qwen3.8 Omni Flash Realtime", Omni: true, Voice: "Tina", Turn: "semantic_vad", Nested: true, Workspace: true},
}

// publicDashScopeHosts are DashScope's public endpoints, which a workspace
// model refuses.
var publicDashScopeHosts = []string{"dashscope.aliyuncs.com", "dashscope-intl.aliyuncs.com", "dashscope-us.aliyuncs.com"}

// dashScopeRefusal matches DashScope's content-safety messages, as qwen's
// classifyError does.
var dashScopeRefusal = regexp.MustCompile(`(?i)data[_ -]?inspection[_ -]?failed|ip[_ -]?infringement[_ -]?suspect|inappropriate content|content[_ -]?(?:filter|moderation|safety|policy)|(?:input|output) data may contain`)

// DashScope is Alibaba Cloud's realtime service for the named model at
// endpoint (DashScopeURL when empty), signed in with the key key returns.
func DashScope(model, endpoint string, key func() string) (Provider, error) {
	i := slices.IndexFunc(DashScopeModels, func(m DashScopeModel) bool { return m.ID == model })
	if i < 0 {
		return Provider{}, fmt.Errorf("DashScope has no realtime model %q", model)
	}
	m := DashScopeModels[i]
	if endpoint == "" {
		endpoint = DashScopeURL
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return Provider{}, fmt.Errorf("the DashScope endpoint %q is not a ws:// or wss:// address", endpoint)
	}
	if m.Workspace && slices.Contains(publicDashScopeHosts, u.Hostname()) {
		return Provider{}, fmt.Errorf("%s needs your Model Studio workspace's WebSocket address, not the public one", m.ID)
	}
	q := u.Query()
	q.Set("model", m.ID)
	u.RawQuery = q.Encode()
	full := u.String()
	return Provider{
		Key:       "dashscope",
		Label:     "DashScope Realtime",
		Aliases:   []string{"qwen"},
		InputRate: 16000, OutputRate: 24000,
		Caps:     Capabilities{FunctionCalling: true, SingleResponseSlot: true, AcknowledgesSessionUpdate: true},
		Protocol: Beta{Modalities: []string{"text", "audio"}},
		Endpoint: func(context.Context) (string, http.Header, error) {
			k := key()
			if k == "" {
				return "", nil, errors.New("set DASHSCOPE_API_KEY to use DashScope")
			}
			return full, http.Header{"Authorization": {"Bearer " + k}}, nil
		},
		Session: func(s Session) any { return dashScopeSession(m, s) },
		ContentRefused: func(code, message string) bool {
			return dashScopeRefusal.MatchString(code) || dashScopeRefusal.MatchString(message)
		},
	}, nil
}

// dashScopeSession is the first session update for model m, as qwen's
// buildSession writes it, with Dyna's turn flags: the call creates and
// cancels every response itself. Whether DashScope honors create_response
// and interrupt_response is untested (a guess from its OpenAI likeness).
func dashScopeSession(m DashScopeModel, s Session) any {
	// The call's voice names an OpenAI voice; each DashScope model has its
	// own, so the model's default is used, as qwen does when none is set.
	voice := m.Voice
	session := msg{
		"instructions":   s.Instructions,
		"tools":          gaTools(s.Tools),
		"modalities":     []string{"text", "audio"},
		"turn_detection": msg{"type": m.Turn, "create_response": false, "interrupt_response": false},
	}
	if m.Nested {
		session["audio"] = msg{
			"input":  msg{"format": msg{"type": "pcm", "sample_rate": 16000}},
			"output": msg{"format": msg{"type": "pcm", "sample_rate": 24000}, "voice": voice},
		}
	} else {
		session["voice"] = voice
		session["input_audio_format"] = "pcm"
		session["output_audio_format"] = "pcm"
	}
	return session
}
