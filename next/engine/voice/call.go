// Package voice bridges the owner's page and one OpenAI Realtime call.
package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
func (m *Manager) Start(ctx context.Context, room string, page *websocket.Conn, dial func(context.Context) (*websocket.Conn, error), dispatch func(context.Context, string) (string, error), backend <-chan string) (err error) {
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
		if err != nil && !errors.Is(err, context.Canceled) {
			closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
			reason := "The connection dropped. Try calling again."
			var failure callFailure
			if errors.As(err, &failure) {
				reason = string(failure)
			}
			_ = writeJSON(closeCtx, page, message{"type": "closed", "reason": reason})
			stop()
		}
		cancel()
		m.mu.Lock()
		m.room = ""
		m.cancel = nil
		m.mu.Unlock()
		page.Close(websocket.StatusNormalClosure, "call ended")
	}()
	dialCtx, stopDial := context.WithTimeout(ctx, 15*time.Second)
	provider, err := dial(dialCtx)
	stopDial()
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

func run(ctx context.Context, page, provider *websocket.Conn, dispatch func(context.Context, string) (string, error), backend <-chan string, idle time.Duration) error {
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
	type toolResult struct{ id, output string }
	results := make(chan toolResult, 8)
	handled := map[string]bool{}
	var output *outputAudio
	var responses responseQueue
	active := 0
	var budget outputBudget
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			_ = writeJSON(ctx, page, message{"type": "closed", "reason": "The call was inactive for 60 seconds."})
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
			if active > 0 {
				text = budget.append(text)
			} else {
				text = truncateText(text, 4000)
			}
			if text != "" {
				if err := writeJSON(ctx, provider, userText("[BACKEND] "+text)); err != nil {
					return err
				}
			}
			if active == 0 {
				if err := responses.request(ctx, provider); err != nil {
					return err
				}
			}
		case result := <-results:
			active--
			if err := writeJSON(ctx, page, message{"type": "response_done", "working": active > 0}); err != nil {
				return err
			}
			if result.output != steerAcknowledgement {
				if chunk := budget.append(result.output); chunk != "" {
					if err := writeJSON(ctx, provider, userText("[BACKEND] "+chunk)); err != nil {
						return err
					}
				}
				if tail := budget.finish(); tail != "" {
					if err := writeJSON(ctx, provider, userText("[BACKEND] "+tail)); err != nil {
						return err
					}
				}
				result.output = completionAcknowledgement
			}
			if err := writeJSON(ctx, provider, message{"type": "conversation.item.create", "item": message{"type": "function_call_output", "call_id": result.id, "output": truncateText(result.output, 4000)}}); err != nil {
				return err
			}
			if err := responses.request(ctx, provider); err != nil {
				return err
			}
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
			if event.Type == "conversation.item.done" && event.Item != nil && event.Item.Type == "function_call" {
				event = *event.Item
				event.Type = "response.function_call_arguments.done"
			}
			switch event.Type {
			case "error":
				if event.Error.Code == "conversation_already_has_active_response" || strings.HasPrefix(event.Error.Message, "Conversation already has an active response in progress:") {
					responses.active = true
					responses.pending = true
					continue
				}
				switch event.Error.Code {
				case "invalid_api_key", "invalid_auth", "authentication_error", "token_expired":
					return callFailure("Reconnect your ChatGPT account and try again.")
				case "rate_limit_exceeded", "insufficient_quota":
					return callFailure("Your ChatGPT account has reached its usage limit. Try again later.")
				default:
					return callFailure("The connection dropped. Try calling again.")
				}
			case "session.updated":
				if err := writeJSON(ctx, page, message{"type": "ready"}); err != nil {
					return err
				}
			case "response.created":
				responses.active = true
			case "response.cancelled":
				output = nil
				if err := responses.finished(ctx, provider); err != nil {
					return err
				}
			case "input_audio_buffer.speech_started":
				if err := writeJSON(ctx, page, message{"type": "speech_started"}); err != nil {
					return err
				}
				if output != nil && (event.ItemID == "" || event.ItemID == output.item) {
					if err := writeJSON(ctx, provider, message{"type": "conversation.item.truncate", "item_id": output.item, "content_index": 0, "audio_end_ms": output.end}); err != nil {
						return err
					}
				}
				output = nil
			case "response.output_audio.delta", "response.audio.delta":
				pcm, err := base64.StdEncoding.DecodeString(event.Delta)
				if err != nil {
					return err
				}
				if event.ItemID != "" {
					rate := event.Rate
					if rate == 0 {
						rate = 24000
					}
					channels := event.Channels
					if channels == 0 {
						channels = event.NumChannels
					}
					if channels == 0 {
						channels = 1
					}
					samples := uint64(len(pcm)/2) / uint64(channels)
					if event.Samples != nil {
						samples = uint64(*event.Samples)
					}
					duration := samples * 1000 / uint64(rate)
					if duration > 0 {
						if output != nil && output.item == event.ItemID {
							output.end += duration
						} else {
							output = &outputAudio{event.ItemID, duration}
						}
					}
				}
				if err := writeJSON(ctx, page, message{"type": "transcript", "role": "assistant", "item_id": event.ItemID, "text": ""}); err != nil {
					return err
				}
				writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
				err = page.Write(writeCtx, websocket.MessageBinary, pcm)
				stop()
				if err != nil {
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
				output = nil
				if handled[event.CallID] {
					continue
				}
				handled[event.CallID] = true
				if event.Name == "remain_silent" {
					if err := writeJSON(ctx, provider, message{"type": "conversation.item.create", "item": message{"type": "function_call_output", "call_id": event.CallID, "output": ""}}); err != nil {
						return err
					}
					continue
				}
				if event.Name != "background_agent" {
					return errors.New("unknown voice tool")
				}
				var args struct {
					Prompt string `json:"prompt"`
				}
				if err := json.Unmarshal([]byte(event.Arguments), &args); err != nil {
					return err
				}
				active++
				if err := writeJSON(ctx, page, message{"type": "response_done", "working": true}); err != nil {
					return err
				}
				go func(id, prompt string) {
					output, err := dispatch(ctx, prompt)
					if err != nil {
						output = err.Error()
					}
					select {
					case results <- toolResult{id, output}:
					case <-ctx.Done():
					}
				}(event.CallID, args.Prompt)
			case "response.done":
				output = nil
				if err := responses.finished(ctx, provider); err != nil {
					return err
				}
				if err := writeJSON(ctx, page, message{"type": "response_done", "working": active > 0}); err != nil {
					return err
				}
			}
		}
	}
}

const steerAcknowledgement = "This was sent to steer the previous background agent task."
const completionAcknowledgement = "Background agent finished. Use the preceding [BACKEND] messages as the result."

const truncationMarker = "\n…output truncated…\n"

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xc0) == 0x80 {
		n--
	}
	return s[:n]
}
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && (s[start]&0xc0) == 0x80 {
		start++
	}
	return s[start:]
}
func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	keep := (n - len(truncationMarker)) / 2
	return head(s, keep) + truncationMarker + tail(s, keep)
}

// outputBudget follows Codex's approximate 1,000-token handoff budget: stream
// the head immediately, retain the newest tail, and emit the marker on finish.
type outputBudget struct {
	sent      int
	pending   string
	truncated bool
}

func (b *outputBudget) append(s string) string {
	remaining := 2000 - b.sent
	chunk := head(s, remaining)
	b.sent += len(chunk)
	if len(chunk) < len(s) {
		b.truncated = true
		b.pending = tail(b.pending+s[len(chunk):], 2000-len(truncationMarker))
	}
	return chunk
}
func (b *outputBudget) finish() string {
	out := ""
	if b.truncated {
		out = truncationMarker + b.pending
	}
	*b = outputBudget{}
	return out
}

type outputAudio struct {
	item string
	end  uint64
}

// responseQueue is Codex's RealtimeResponseCreateQueue: a single deferred
// create request, not a speak queue. A racing active-response error re-arms it.
type responseQueue struct{ active, pending bool }

func (q *responseQueue) request(ctx context.Context, c *websocket.Conn) error {
	if q.active {
		q.pending = true
		return nil
	}
	q.active = true
	return writeJSON(ctx, c, message{"type": "response.create"})
}
func (q *responseQueue) finished(ctx context.Context, c *websocket.Conn) error {
	q.active = false
	if !q.pending {
		return nil
	}
	q.pending = false
	return q.request(ctx, c)
}

// callFailure contains only owner-facing text, never an upstream message.
type callFailure string

func (e callFailure) Error() string { return string(e) }
