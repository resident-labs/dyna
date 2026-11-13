// Package voice bridges the owner's page and one OpenAI Realtime call.
package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Manager permits one call per engine, including while its provider is dialing.
type Manager struct {
	mu     sync.Mutex
	room   string
	cancel context.CancelFunc
}

func (m *Manager) Status() string { m.mu.Lock(); defer m.mu.Unlock(); return m.room }
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
}

// Start holds the call until either connection ends. backend contains only root
// DM posts and questions. Its producer must observe ctx when the call ends.
func (m *Manager) Start(ctx context.Context, room string, page *websocket.Conn, dial func(context.Context) (*websocket.Conn, error), dispatch func(context.Context, string) error, backend <-chan string) error {
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		cancel()
		return errors.New("another voice call is active")
	}
	m.room = room
	m.cancel = cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		m.room = ""
		m.cancel = nil
		m.mu.Unlock()
		page.Close(websocket.StatusNormalClosure, "call ended")
	}()
	provider, err := dial(ctx)
	if err != nil {
		return err
	}
	defer provider.Close(websocket.StatusNormalClosure, "call ended")
	return run(ctx, page, provider, dispatch, backend, 60*time.Second)
}

type frame struct {
	kind websocket.MessageType
	data []byte
	err  error
}

func readFrames(ctx context.Context, c *websocket.Conn) <-chan frame {
	out := make(chan frame, 8)
	go func() {
		defer close(out)
		for {
			kind, b, err := c.Read(ctx)
			select {
			case out <- frame{kind, b, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return out
}

func run(ctx context.Context, page, provider *websocket.Conn, dispatch func(context.Context, string) error, backend <-chan string, idle time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	provider.SetReadLimit(2 << 20)
	page.SetReadLimit(128 << 10)
	if err := writeJSON(ctx, provider, sessionUpdate()); err != nil {
		return err
	}
	pf, vf := readFrames(ctx, page), readFrames(ctx, provider)
	timer := time.NewTimer(idle)
	defer timer.Stop()
	busy := false
	ownerPending := false
	interrupted := false
	var pending []string
	item := ""
	played := map[string]int64{}
	type toolResult struct {
		callID, output string
	}
	results := make(chan toolResult, 8)
	respond := func() error { busy = true; return writeJSON(ctx, provider, message{"type": "response.create"}) }
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			_ = writeJSON(ctx, page, message{"type": "closed", "reason": "idle"})
			return nil
		case f, ok := <-pf:
			if !ok {
				return nil
			}
			if f.err != nil {
				return f.err
			}
			timer.Reset(idle)
			if f.kind == websocket.MessageBinary {
				if len(f.data)%2 != 0 {
					return errors.New("PCM16 frame has odd length")
				}
				if err := writeJSON(ctx, provider, message{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(f.data)}); err != nil {
					return err
				}
			} else {
				var control struct {
					Type   string `json:"type"`
					ItemID string `json:"item_id"`
					End    int64  `json:"audio_end_ms"`
				}
				if err := json.Unmarshal(f.data, &control); err != nil {
					return err
				}
				switch control.Type {
				case "played":
					if control.End < 0 {
						return errors.New("negative playback position")
					}
					played[control.ItemID] = control.End
				case "closed":
					return nil
				default:
					return errors.New("unknown page control")
				}
			}
		case text, ok := <-backend:
			if !ok {
				backend = nil
				continue
			}
			pending = append(pending, text)
			if !busy {
				if err := writeJSON(ctx, provider, userText("[BACKEND]\n"+strings.Join(pending, "\n\n"))); err != nil {
					return err
				}
				pending = nil
				if err := respond(); err != nil {
					return err
				}
			}
		case result := <-results:
			if err := writeJSON(ctx, provider, message{"type": "conversation.item.create", "item": message{"type": "function_call_output", "call_id": result.callID, "output": result.output}}); err != nil {
				return err
			}
			// dispatch does not elicit another acknowledgment; backend will speak results.
		case f, ok := <-vf:
			if !ok {
				return nil
			}
			if f.err != nil {
				return f.err
			}
			var event providerEvent
			if err := json.Unmarshal(f.data, &event); err != nil {
				return err
			}
			switch event.Type {
			case "error":
				return fmt.Errorf("realtime: %s", event.Error)
			case "session.updated":
				if err := writeJSON(ctx, page, message{"type": "ready"}); err != nil {
					return err
				}
				if err := respond(); err != nil {
					return err
				}
			case "input_audio_buffer.speech_started":
				interrupted = busy
				if busy {
					if err := writeJSON(ctx, provider, message{"type": "response.cancel"}); err != nil {
						return err
					}
				}
				if item != "" {
					if err := writeJSON(ctx, provider, message{"type": "conversation.item.truncate", "item_id": item, "content_index": 0, "audio_end_ms": played[item]}); err != nil {
						return err
					}
				}
				if err := writeJSON(ctx, page, message{"type": "speech_started"}); err != nil {
					return err
				}
				item = ""
			case "input_audio_buffer.committed":
				ownerPending = busy
				if !busy {
					if err := respond(); err != nil {
						return err
					}
				}
			case "response.output_audio.delta":
				if interrupted {
					continue
				}
				item = event.ItemID
				pcm, err := base64.StdEncoding.DecodeString(event.Delta)
				if err != nil {
					return err
				}
				// Associate binary frames with an item without adding another event type.
				if err := writeJSON(ctx, page, message{"type": "transcript", "role": "assistant", "item_id": item, "text": ""}); err != nil {
					return err
				}
				if err := page.Write(ctx, websocket.MessageBinary, pcm); err != nil {
					return err
				}
			case "conversation.item.input_audio_transcription.completed", "response.output_audio_transcript.done":
				role := "assistant"
				if event.Type == "conversation.item.input_audio_transcription.completed" {
					role = "owner"
				}
				if err := writeJSON(ctx, page, message{"type": "transcript", "role": role, "item_id": event.ItemID, "text": event.Transcript}); err != nil {
					return err
				}
			case "response.function_call_arguments.done":
				var args struct {
					Prompt string `json:"prompt"`
				}
				if err := json.Unmarshal([]byte(event.Arguments), &args); err != nil {
					return err
				}
				switch event.Name {
				case "dispatch", "remain_silent":
				default:
					return errors.New("unknown voice tool")
				}
				go func(e providerEvent, prompt string) {
					output := "ok"
					if e.Name == "dispatch" {
						toolCtx, stop := context.WithTimeout(ctx, 10*time.Second)
						defer stop()
						done := make(chan error, 1)
						go func() { done <- dispatch(toolCtx, prompt) }()
						select {
						case err := <-done:
							if err != nil {
								output = err.Error()
							}
						case <-toolCtx.Done():
							output = toolCtx.Err().Error()
						}
					}
					select {
					case results <- toolResult{callID: e.CallID, output: output}:
					case <-ctx.Done():
					}
				}(event, args.Prompt)
			case "response.done":
				interrupted = false
				busy = false
				if err := writeJSON(ctx, page, message{"type": "response_done"}); err != nil {
					return err
				}
				if ownerPending {
					ownerPending = false
					if err := respond(); err != nil {
						return err
					}
				} else if len(pending) > 0 {
					if err := writeJSON(ctx, provider, userText("[BACKEND]\n"+strings.Join(pending, "\n\n"))); err != nil {
						return err
					}
					pending = nil
					if err := respond(); err != nil {
						return err
					}
				}
			}
		}
	}
}
