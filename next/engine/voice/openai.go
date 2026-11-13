package voice

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

//go:embed prompt.md
var prompt string

type message map[string]any

// DialProxy is the single provider connection boundary. The engine supplies the
// proxy's private key and the room's eligible accounts; voice owns no credentials.
func DialProxy(ctx context.Context, socket, key string, eligible []string) (*websocket.Conn, error) {
	if len(eligible) == 0 {
		return nil, errors.New("no eligible ChatGPT account")
	}
	ids := make([]string, len(eligible))
	for i, id := range eligible {
		ids[i] = base64.RawURLEncoding.EncodeToString([]byte(id))
	}
	h := http.Header{"Authorization": {"Bearer " + key}, "X-Dyna-Eligible-Auth": {strings.Join(ids, ",")}}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	conn, _, err := websocket.Dial(ctx, "ws://proxy/v1/realtime?model=gpt-realtime", &websocket.DialOptions{HTTPClient: client, HTTPHeader: h})
	return conn, err
}

func sessionUpdate() message {
	pcm := message{"type": "audio/pcm", "rate": 24000}
	return message{"type": "session.update", "session": message{
		"type": "realtime", "instructions": prompt, "output_modalities": []string{"audio"},
		"audio":       message{"input": message{"format": pcm, "transcription": message{"model": "gpt-4o-mini-transcribe"}, "noise_reduction": message{"type": "near_field"}, "turn_detection": message{"type": "server_vad", "silence_duration_ms": 500, "create_response": true, "interrupt_response": true}}, "output": message{"format": pcm, "voice": "marin"}},
		"tool_choice": "auto",
		"tools": []message{
			{"type": "function", "name": "background_agent", "description": "Send a user request to the background agent. Use this as the default action. Do not rephrase the user's ask or rewrite it in your own words; pass along the user's own words. If the background agent is idle, this starts a new task and returns the final result to the user. If the background agent is already working on a task, this sends the request as guidance to steer that previous task. If the user asks to do something next, later, after this, or once current work finishes, call this tool so the work is actually queued instead of merely promising to do it later.", "parameters": message{"type": "object", "properties": message{"prompt": message{"type": "string", "description": "The user request to delegate to the background agent."}}, "required": []string{"prompt"}, "additionalProperties": false}},
			{"type": "function", "name": "remain_silent", "description": "Call this when the best response is to say nothing. Use it instead of speaking after hidden system/control messages, after background agent updates in silent modes, or whenever acknowledging aloud would be distracting. This tool has no user-visible effect.", "parameters": message{"type": "object", "properties": message{}, "additionalProperties": false}},
		},
	}}
}
func writeJSON(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Write(writeCtx, websocket.MessageText, b)
}
func userText(text string) message {
	return message{"type": "conversation.item.create", "item": message{"type": "message", "role": "user", "content": []message{{"type": "input_text", "text": text}}}}
}

type providerEvent struct {
	Type       string `json:"type"`
	ItemID     string `json:"item_id"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
	Name       string `json:"name"`
	CallID     string `json:"call_id"`
	Arguments  string `json:"arguments"`
	Error      struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Item        *providerEvent `json:"item"`
	Samples     *uint32        `json:"samples_per_channel"`
	Rate        uint32         `json:"sample_rate"`
	Channels    uint32         `json:"channels"`
	NumChannels uint32         `json:"num_channels"`
}
