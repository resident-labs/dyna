package voice

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/coder/websocket"
)

//go:embed prompt.md
var prompt string

type message map[string]any

// DialProxy is the single provider connection boundary. The engine supplies the
// proxy's private key and the room's eligible accounts; voice owns no credentials.
func DialProxy(ctx context.Context, socket, key string, eligible []string) (*websocket.Conn, error) {
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
		"audio": message{"input": message{"format": pcm, "transcription": message{"model": "gpt-4o-mini-transcribe"}, "noise_reduction": message{"type": "near_field"}, "turn_detection": message{"type": "semantic_vad", "create_response": false, "interrupt_response": false}}, "output": message{"format": pcm, "voice": "marin"}},
		"tools": []message{
			{"type": "function", "name": "dispatch", "description": "Send the owner's exact words to the execution agent.", "parameters": message{"type": "object", "properties": message{"prompt": message{"type": "string"}}, "required": []string{"prompt"}, "additionalProperties": false}},
			{"type": "function", "name": "remain_silent", "description": "Say nothing when speech adds no useful information.", "parameters": message{"type": "object", "properties": message{}, "additionalProperties": false}},
		},
	}}
}
func writeJSON(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, b)
}
func userText(text string) message {
	return message{"type": "conversation.item.create", "item": message{"type": "message", "role": "user", "content": []message{{"type": "input_text", "text": text}}}}
}

type providerEvent struct {
	Type       string          `json:"type"`
	ItemID     string          `json:"item_id"`
	Delta      string          `json:"delta"`
	Transcript string          `json:"transcript"`
	Name       string          `json:"name"`
	CallID     string          `json:"call_id"`
	Arguments  string          `json:"arguments"`
	Error      json.RawMessage `json:"error"`
}
