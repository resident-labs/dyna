package realtime

// Ported from qwen-audio-agent server/src/voice/providers/doubao-seeduplex.mjs
// and doubao-seeduplex-protocol.mjs (Apache-2.0).

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DoubaoURL, DoubaoModel and DoubaoVoice are qwen-audio-agent 2.0.1's
// defaults for Volcengine's Doubao Seeduplex realtime dialogue.
const (
	DoubaoURL   = "wss://openspeech.bytedance.com/api/v3/duplex/realtime/dialogue"
	DoubaoModel = "1.2.6.1"
	DoubaoVoice = "zh_female_vv_jupiter_bigtts"
)

// DoubaoKey is the API key in DOUBAO_API_KEY, SEEDUPLEX_API_KEY or
// VOLCENGINE_DOUBAO_API_KEY, the first set, as qwen reads it.
func DoubaoKey() string {
	for _, name := range []string{"DOUBAO_API_KEY", "SEEDUPLEX_API_KEY", "VOLCENGINE_DOUBAO_API_KEY"} {
		if k := os.Getenv(name); k != "" {
			return k
		}
	}
	return ""
}

// doubaoRefusal matches Doubao's content-safety messages, as qwen's
// classifyError does.
var doubaoRefusal = regexp.MustCompile(`(?i)content|audit|safety|risk|risky|inspection`)

// Doubao is Volcengine's Doubao Seeduplex duplex dialogue for model at
// endpoint (DoubaoModel and DoubaoURL when empty), signed in with the key
// key returns.
func Doubao(model, endpoint string, key func() string) (Provider, error) {
	if model == "" {
		model = DoubaoModel
	}
	if endpoint == "" {
		endpoint = DoubaoURL
	}
	if u, err := url.Parse(endpoint); err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return Provider{}, fmt.Errorf("the Doubao endpoint %q is not a ws:// or wss:// address", endpoint)
	}
	return Provider{
		Key:       "doubao-seeduplex",
		Label:     "Doubao Seeduplex Realtime",
		Aliases:   []string{"doubao", "seeduplex", "volcengine"},
		InputRate: 16000, OutputRate: 24000,
		// Whether Seeduplex answers session.create is not known, so Dial
		// does not wait for it.
		Caps:     Capabilities{FunctionCalling: true, SingleResponseSlot: true},
		Protocol: &DoubaoProtocol{},
		Endpoint: func(context.Context) (string, http.Header, error) {
			k := key()
			if k == "" {
				return "", nil, errors.New("set DOUBAO_API_KEY or SEEDUPLEX_API_KEY to use Doubao Seeduplex")
			}
			return endpoint, http.Header{"X-Api-Key": {k}}, nil
		},
		Session: func(s Session) any { return doubaoSession(model, s) },
		ContentRefused: func(code, message string) bool {
			return doubaoRefusal.MatchString(code) || doubaoRefusal.MatchString(message)
		},
	}, nil
}

// doubaoSession is qwen's buildSession. The call's voice names an OpenAI
// voice, so the model's default is used.
func doubaoSession(model string, s Session) any {
	return msg{
		"model":        model,
		"instructions": s.Instructions,
		"audio": msg{
			"input":  msg{"format": msg{"type": "pcm", "rate": 16000}},
			"output": msg{"format": msg{"type": "pcm", "rate": 24000}, "voice": DoubaoVoice, "speed": 0, "loudness": 0},
		},
		"tools": gaTools(s.Tools),
	}
}

// InputMuter is a Protocol whose service takes the owner's mute itself: the
// call sends Mute on every mute change, instead of committing the turn.
type InputMuter interface {
	Mute(muted bool) any
}

// DoubaoProtocol speaks Seeduplex's duplex dialogue. It is close to the GA
// Realtime protocol, but the service starts its own replies, a text turn
// goes in as speech_text_buffer.commit, audio comes as Float32 PCM, and
// response events carry no response.created: the adapter makes one up. Its
// state lasts one connection, and a lock guards it.
type DoubaoProtocol struct {
	mu      sync.Mutex
	n       int
	active  string          // the response in progress
	pending string          // a text turn, waiting for its response.create
	said    strings.Builder // the response's text so far
}

func (d *DoubaoProtocol) SessionUpdate(session any) any {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.active, d.pending = "", ""
	d.said.Reset()
	return msg{"type": "session.create", "session": session}
}

func (*DoubaoProtocol) AudioAppend(pcm []byte) any { return GA{}.AudioAppend(pcm) }

// AudioCommit is the mute: the call sends Mute instead.
func (*DoubaoProtocol) AudioCommit() any { return msg{"type": "input_audio_mute.commit"} }

func (*DoubaoProtocol) Mute(muted bool) any {
	if muted {
		return msg{"type": "input_audio_mute.commit"}
	}
	return msg{"type": "input_audio_unmute.commit"}
}

// UserText holds the text for the response.create that follows: Seeduplex
// takes a text turn as speech_text_buffer.commit.
func (d *DoubaoProtocol) UserText(text string) any {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending = text
	return nil
}

func (*DoubaoProtocol) FunctionOutput(callID, output string) any {
	return msg{"type": "conversation.item.create", "items": []msg{{
		"call_id": callID, "role": "tool",
		"content": []msg{{"type": "input_text", "text": output}},
	}}}
}

// ResponseCreate commits a held text turn; otherwise the service starts its
// own reply, so it sends nothing.
func (d *DoubaoProtocol) ResponseCreate(string) any {
	d.mu.Lock()
	defer d.mu.Unlock()
	text := d.pending
	d.pending = ""
	if text == "" {
		return nil
	}
	return msg{"type": "speech_text_buffer.commit", "text": text}
}

func (*DoubaoProtocol) ResponseCancel() any { return msg{"type": "response.cancel"} }

// Truncate has no Seeduplex equivalent.
func (*DoubaoProtocol) Truncate(string, time.Duration) any { return nil }

func (d *DoubaoProtocol) Decode(data []byte) (Event, error) {
	evs, err := d.DecodeAll(data)
	if err != nil || len(evs) == 0 {
		return Event{}, err
	}
	return evs[0], nil
}

type doubaoEvent struct {
	Type       string `json:"type"`
	ResponseID string `json:"response_id"`
	ItemID     string `json:"item_id"`
	Delta      string `json:"delta"`
	Text       string `json:"text"`
	Transcript string `json:"transcript"`
	Audio      string `json:"audio"`
	CallID     string `json:"call_id"`
	Name       string `json:"name"`
	Arguments  string `json:"arguments"`
	Items      []struct {
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"items"`
	Response struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"response"`
}

// DecodeAll maps one Seeduplex event to Dyna's events. Events outside a
// response, such as errors, speech and transcripts, read as GA events.
func (d *DoubaoProtocol) DecodeAll(data []byte) ([]Event, error) {
	var e doubaoEvent
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("realtime: undecodable event: %w", err)
	}
	if e.Type == "response.canceled" {
		e.Type, e.Response.Status = "response.done", "cancelled"
	}
	switch e.Type {
	case "response.output_text.delta", "response.output_text.done", "response.output_audio.started",
		"response.output_audio.delta", "response.output_audio.done", "response.function_call_arguments.done", "response.done":
	default:
		ev, err := GA{}.Decode(data)
		if err != nil || ev.Kind == Other {
			return nil, err
		}
		return []Event{ev}, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var evs []Event
	if d.active == "" {
		d.active = e.ResponseID
		if d.active == "" {
			d.active = e.Response.ID
		}
		if d.active == "" {
			d.n++
			d.active = "response_" + strconv.Itoa(d.n)
		}
		evs = append(evs, Event{Kind: ResponseStarted, ResponseID: d.active})
	}
	id := d.active
	item := e.ItemID
	if item == "" {
		item = id
	}
	switch e.Type {
	case "response.output_text.delta":
		// Seeduplex's reply text is what Voice says; its deltas mark the
		// sentences the call steers by.
		text := e.Delta
		if text == "" {
			text = e.Text
		}
		d.said.WriteString(text)
		evs = append(evs, Event{Kind: VoiceDelta, ResponseID: id, ItemID: item, Text: text})
	case "response.output_text.done":
		text := e.Text
		if text == "" {
			text = e.Transcript
		}
		if text == "" {
			text = d.said.String()
		}
		d.said.Reset()
		evs = append(evs, Event{Kind: VoiceTranscript, ResponseID: id, ItemID: item, Text: text})
	case "response.output_audio.delta":
		b64 := e.Delta
		if b64 == "" {
			b64 = e.Audio
		}
		pcm, err := float32ToPCM16(b64)
		if err != nil {
			return nil, err
		}
		evs = append(evs, Event{Kind: AudioDelta, ResponseID: id, ItemID: item, Audio: pcm})
	case "response.output_audio.done":
		evs = append(evs, Event{Kind: AudioDone, ResponseID: id, ItemID: item})
	case "response.function_call_arguments.done":
		calls := e.Items
		if len(calls) == 0 {
			calls = append(calls, struct {
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{e.CallID, e.Name, e.Arguments})
		}
		for _, c := range calls {
			args := c.Arguments
			if args == "" {
				args = "{}"
			}
			evs = append(evs, Event{Kind: ToolCall, ResponseID: id, CallID: c.CallID, Name: c.Name, Args: json.RawMessage(args)})
		}
	case "response.done":
		status := e.Response.Status
		if status == "" {
			status = "completed"
		}
		evs = append(evs, Event{Kind: ResponseDone, ResponseID: id, Text: status})
		d.active = ""
		d.said.Reset()
	}
	return evs, nil
}

// float32ToPCM16 converts base64 Float32 little-endian samples to PCM16,
// clipped to [-1, 1], as qwen's pcmFloat32Base64ToPcm16 does.
func float32ToPCM16(b64 string) ([]byte, error) {
	in, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(in)%4 != 0 {
		return nil, errors.New("realtime: Doubao Seeduplex sent invalid Float32 PCM audio")
	}
	out := make([]byte, len(in)/2)
	for i := 0; i < len(in)/4; i++ {
		s := math.Max(-1, math.Min(1, float64(math.Float32frombits(binary.LittleEndian.Uint32(in[4*i:])))))
		var v int16
		if s < 0 {
			v = int16(math.Round(s * 0x8000))
		} else {
			v = int16(math.Round(s * 0x7fff))
		}
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out, nil
}
