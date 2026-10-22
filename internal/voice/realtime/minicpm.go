package realtime

// Ported from qwen-audio-agent server/src/voice/providers/minicpm-o.mjs and
// minicpm-o-protocol.mjs (Apache-2.0).

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
	"strconv"
	"strings"
	"sync"
	"time"
)

// MiniCPMOModel is the model MiniCPM-o's full-duplex server runs.
const MiniCPMOModel = "openbmb/MiniCPM-o-4_5"

// MiniCPMO is a self-hosted MiniCPM-o 4.5 full-duplex server at endpoint,
// with the bearer token token returns, if any. It has no tool calling and
// takes no client replies, so Voice can talk on it but cannot dispatch work
// or say Dyna's items: those are reported unspoken.
func MiniCPMO(endpoint string, token func() string) (Provider, error) {
	if endpoint == "" {
		return Provider{}, errors.New("MiniCPM-o needs its server's address: set voice.url")
	}
	if u, err := url.Parse(endpoint); err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return Provider{}, fmt.Errorf("the MiniCPM-o endpoint %q is not a ws:// or wss:// address", endpoint)
	}
	return Provider{
		Key:       "minicpm-o",
		Label:     "MiniCPM-o",
		Aliases:   []string{"minicpmo"},
		InputRate: 16000, OutputRate: 24000,
		Caps:     Capabilities{AcknowledgesSessionUpdate: true, QueuesBeforeSession: true, NoClientResponses: true, SteppedByInput: true},
		Protocol: &MiniCPMOProtocol{},
		Endpoint: func(context.Context) (string, http.Header, error) {
			h := http.Header{}
			if t := token(); t != "" {
				h.Set("Authorization", "Bearer "+t)
			}
			return endpoint, h, nil
		},
		Session: func(s Session) any { return msg{"instructions": s.Instructions} },
		// The model thinks and speaks in one full-duplex stream; qwen allows
		// it 2 minutes to start.
		ResponseStartTimeout: 2 * time.Minute,
	}, nil
}

// MiniCPMOToken is the bearer token in MINICPM_O_AUTH_TOKEN.
func MiniCPMOToken() string { return os.Getenv("MINICPM_O_AUTH_TOKEN") }

// miniCPMOChunk is how much PCM16 audio goes out at once: 1 s at 16 kHz.
const miniCPMOChunk = 16000 * 2

// MiniCPMOProtocol speaks MiniCPM-o 4.5's full-duplex protocol, as qwen's
// adapter does: the server queues the client, then takes session.init;
// audio goes in as Float32 in 1 s chunks and comes out as Float32; replies
// are output deltas the adapter turns into responses, and a "listen" delta
// ends one. The client cannot add items, ask for replies or cancel. Its
// state lasts one connection, and a lock guards it.
type MiniCPMOProtocol struct {
	mu      sync.Mutex
	n       int
	active  string
	pending []byte          // PCM16 still short of a chunk
	said    strings.Builder // the reply's text so far
}

func (m *MiniCPMOProtocol) SessionUpdate(session any) any {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active, m.pending = "", nil
	m.said.Reset()
	prompt := ""
	if s, ok := session.(msg); ok {
		prompt, _ = s["instructions"].(string)
	}
	return msg{"type": "session.init", "payload": msg{"system_prompt": strings.TrimSpace(prompt), "config": msg{}}}
}

// AudioAppend holds audio until it fills a 1 s chunk, then sends it as
// Float32.
func (m *MiniCPMOProtocol) AudioAppend(pcm []byte) any {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending = append(m.pending, pcm...)
	if len(m.pending) < miniCPMOChunk {
		return nil
	}
	chunk := m.pending[:miniCPMOChunk]
	m.pending = append([]byte(nil), m.pending[miniCPMOChunk:]...)
	return msg{"type": "input.append", "input": msg{"audio": pcm16ToFloat32(chunk), "force_listen": false}}
}

// Mute sends the owner's last words: the audio still short of a chunk goes
// out padded with silence, so a short "yes" is heard and the next turn does
// not start with this one's tail. Unmuting sends nothing.
func (m *MiniCPMOProtocol) Mute(muted bool) any {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !muted || len(m.pending) == 0 {
		return nil
	}
	chunk := make([]byte, miniCPMOChunk)
	copy(chunk, m.pending)
	m.pending = nil
	return msg{"type": "input.append", "input": msg{"audio": pcm16ToFloat32(chunk), "force_listen": false}}
}

func (*MiniCPMOProtocol) AudioCommit() any                    { return nil }
func (*MiniCPMOProtocol) UserText(string) any                 { return nil }
func (*MiniCPMOProtocol) FunctionOutput(string, string) any   { return nil }
func (*MiniCPMOProtocol) ResponseCreate(string) any           { return nil }
func (*MiniCPMOProtocol) ResponseCancel() any                 { return nil }
func (*MiniCPMOProtocol) Truncate(string, time.Duration) any  { return nil }
func (m *MiniCPMOProtocol) Decode(data []byte) (Event, error) { return firstEvent(m.DecodeAll(data)) }

func firstEvent(evs []Event, err error) (Event, error) {
	if err != nil || len(evs) == 0 {
		return Event{}, err
	}
	return evs[0], nil
}

type miniCPMOEvent struct {
	Type       string `json:"type"`
	ResponseID string `json:"response_id"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	Audio      string `json:"audio"`
	Error      *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

// DecodeAll maps one MiniCPM-o server message to Dyna's events.
func (m *MiniCPMOProtocol) DecodeAll(data []byte) ([]Event, error) {
	var e miniCPMOEvent
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("realtime: undecodable event: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var evs []Event
	finish := func() {
		if m.active == "" {
			return
		}
		if m.said.Len() > 0 {
			evs = append(evs, Event{Kind: VoiceTranscript, ResponseID: m.active, ItemID: m.active, Text: m.said.String()})
			m.said.Reset()
		}
		evs = append(evs, Event{Kind: AudioDone, ResponseID: m.active, ItemID: m.active}, Event{Kind: ResponseDone, ResponseID: m.active, Text: "completed"})
		m.active = ""
	}
	start := func(id string) string {
		if id == "" {
			id = m.active
		}
		if id == "" {
			m.n++
			id = "resp_" + strconv.Itoa(m.n)
		}
		if m.active != "" && m.active != id {
			finish()
		}
		if m.active != id {
			m.active = id
			evs = append(evs, Event{Kind: ResponseStarted, ResponseID: id})
		}
		return id
	}
	output := func(id string) error {
		switch {
		case e.Text != "":
			m.said.WriteString(e.Text)
			evs = append(evs, Event{Kind: VoiceDelta, ResponseID: id, ItemID: id, Text: e.Text})
		case e.Audio != "":
			pcm, err := float32ToPCM16(e.Audio)
			if err != nil {
				return errors.New("realtime: MiniCPM-o sent invalid Float32 PCM audio")
			}
			evs = append(evs, Event{Kind: AudioDelta, ResponseID: id, ItemID: id, Audio: pcm})
		}
		return nil
	}
	switch e.Type {
	case "session.queue_done":
		return []Event{{Kind: SessionQueued}}, nil
	case "session.created":
		return []Event{{Kind: SessionReady}}, nil
	case "error":
		text := e.Type
		if e.Error != nil {
			text = e.Error.Message
		}
		return []Event{{Kind: Error, Text: text}}, nil
	case "response.output.delta":
		if e.Kind == "listen" {
			finish()
			return evs, nil
		}
		if err := output(start(e.ResponseID)); err != nil {
			return nil, err
		}
	case "response.done":
		if e.Text != "" || e.Audio != "" {
			id := start(e.ResponseID)
			if e.Text != "" {
				m.said.WriteString(e.Text)
				evs = append(evs, Event{Kind: VoiceDelta, ResponseID: id, ItemID: id, Text: e.Text})
			}
			if e.Audio != "" {
				pcm, err := float32ToPCM16(e.Audio)
				if err != nil {
					return nil, errors.New("realtime: MiniCPM-o sent invalid Float32 PCM audio")
				}
				evs = append(evs, Event{Kind: AudioDelta, ResponseID: id, ItemID: id, Audio: pcm})
			}
		}
		finish()
	}
	return evs, nil
}

// pcm16ToFloat32 converts PCM16 little-endian samples to base64 Float32, as
// qwen's pcm16Base64ToFloat32 does.
func pcm16ToFloat32(pcm []byte) string {
	out := make([]byte, len(pcm)/2*4)
	for i := 0; i < len(pcm)/2; i++ {
		v := float32(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
		binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(v))
	}
	return base64.StdEncoding.EncodeToString(out)
}
