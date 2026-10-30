// Package pi drives a retained Pi RPC process.
package pi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
)

type promptInput struct {
	Message    string `json:"message"`
	DeliveryID string `json:"deliveryId"`
}

type command struct {
	ID         string        `json:"id"`
	Type       string        `json:"type"`
	Message    string        `json:"message,omitempty"`
	DeliveryID string        `json:"deliveryId,omitempty"`
	Inputs     []promptInput `json:"inputs,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type contentBlocks []contentBlock

func (c *contentBlocks) UnmarshalJSON(data []byte) error {
	// User messages may carry a plain string; only assistant blocks supply final text.
	if len(data) > 0 && data[0] == '"' {
		return nil
	}
	type blocks contentBlocks
	return json.Unmarshal(data, (*blocks)(c))
}

type event struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Aborted bool   `json:"aborted"`
	Data    struct {
		SessionFile string `json:"sessionFile"`
	} `json:"data"`
	Message struct {
		Role          string        `json:"role"`
		Model         string        `json:"model"`
		ResponseModel string        `json:"responseModel"`
		StopReason    string        `json:"stopReason"`
		ErrorMessage  string        `json:"errorMessage"`
		Content       contentBlocks `json:"content"`
		Usage         struct {
			Input      int64 `json:"input"`
			Output     int64 `json:"output"`
			CacheRead  int64 `json:"cacheRead"`
			CacheWrite int64 `json:"cacheWrite"`
		} `json:"usage"`
	} `json:"message"`
}

type Harness struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	session   string
	writeMu   sync.Mutex
	mu        sync.Mutex
	pending   map[string]chan event
	events    chan event
	done      chan struct{}
	stop      chan struct{}
	readErr   error
	sequence  atomic.Uint64
	closeOnce sync.Once
	closeErr  error
}

var _ harness.Harness = (*Harness)(nil)

// Open starts Pi in RPC mode using the caller's image configuration and session file.
func Open(ctx context.Context, executable string, args []string, session string) (*Harness, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	h := &Harness{cmd: cmd, stdin: stdin, session: session, pending: make(map[string]chan event), events: make(chan event, 256), done: make(chan struct{}), stop: make(chan struct{})}
	go h.read(stdout)
	if session == "" {
		state, err := h.request(ctx, "get_state", nil, nil)
		if err != nil {
			_ = h.Close()
			return nil, err
		}
		h.session = state.Data.SessionFile
		if h.session == "" {
			_ = h.Close()
			return nil, errors.New("pi returned no session file")
		}
	}
	return h, nil
}

func (h *Harness) read(r io.Reader) {
	defer close(h.done)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	for scanner.Scan() {
		var e event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			h.readErr = err
			return
		}
		if e.Type == "response" {
			h.mu.Lock()
			ch := h.pending[e.ID]
			h.mu.Unlock()
			if ch != nil {
				ch <- e
			}
		} else {
			select {
			case h.events <- e:
			case <-h.stop:
				return
			}
		}
	}
	h.readErr = scanner.Err()
}

func (h *Harness) send(ctx context.Context, typ string, input *contract.TurnInput, inputs []contract.TurnInput) error {
	_, err := h.request(ctx, typ, input, inputs)
	return err
}

func (h *Harness) request(ctx context.Context, typ string, input *contract.TurnInput, inputs []contract.TurnInput) (event, error) {
	id := strconv.FormatUint(h.sequence.Add(1), 10)
	c := command{ID: id, Type: typ}
	for _, item := range inputs {
		c.Inputs = append(c.Inputs, promptInput{Message: item.Text, DeliveryID: strconv.FormatInt(int64(item.Delivery), 10)})
	}
	if input != nil {
		c.Message = input.Text
		c.DeliveryID = strconv.FormatInt(int64(input.Delivery), 10)
	}
	ch := make(chan event, 1)
	h.mu.Lock()
	h.pending[id] = ch
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.pending, id); h.mu.Unlock() }()
	h.writeMu.Lock()
	err := json.NewEncoder(h.stdin).Encode(c)
	h.writeMu.Unlock()
	if err != nil {
		return event{}, err
	}
	select {
	case result := <-ch:
		if !result.Success {
			return event{}, fmt.Errorf("pi %s: %s", typ, result.Error)
		}
		return result, nil
	case <-h.done:
		return event{}, h.readerError()
	case <-ctx.Done():
		return event{}, ctx.Err()
	}
}

func (h *Harness) readerError() error {
	if h.readErr != nil {
		return h.readErr
	}
	return errors.New("pi RPC stream closed")
}

func (h *Harness) Start(ctx context.Context, id contract.TurnID, inputs []contract.TurnInput) (contract.TurnEnded, error) {
	return h.run(ctx, id, "prompt", inputs)
}

func (h *Harness) Continue(ctx context.Context, id contract.TurnID) (contract.TurnEnded, error) {
	return h.run(ctx, id, "continue", nil)
}

func (h *Harness) run(ctx context.Context, id contract.TurnID, typ string, inputs []contract.TurnInput) (contract.TurnEnded, error) {
	result := contract.TurnEnded{Turn: id, Outcome: contract.TurnOutcome("completed")}
	if err := h.send(ctx, typ, nil, inputs); err != nil {
		return result, err
	}
	for {
		select {
		case e := <-h.events:
			switch e.Type {
			case "message_end":
				m := e.Message
				if m.Role != "assistant" {
					continue
				}
				var text []string
				for _, block := range m.Content {
					if block.Type == "text" {
						text = append(text, block.Text)
					}
				}
				result.Final = strings.Join(text, "\n")
				result.Usage.Model = m.Model
				if m.ResponseModel != "" {
					result.Usage.Model = m.ResponseModel
				}
				result.Usage.InputTokens += m.Usage.Input
				result.Usage.OutputTokens += m.Usage.Output
				result.Usage.CacheReadTokens += m.Usage.CacheRead
				result.Usage.CacheWriteTokens += m.Usage.CacheWrite
				if m.StopReason == "error" || m.StopReason == "length" {
					result.Outcome, result.Error = contract.TurnOutcome("failed"), m.ErrorMessage
				} else if m.StopReason == "aborted" {
					result.Outcome = contract.TurnOutcome("stopped")
				} else {
					result.Outcome, result.Error = contract.TurnOutcome("completed"), ""
				}
			case "agent_settled":
				if e.Aborted {
					result.Outcome = contract.TurnOutcome("stopped")
				}
				return result, nil
			}
		case <-h.done:
			return result, h.readerError()
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

func (h *Harness) Steer(ctx context.Context, input contract.TurnInput) error {
	return h.send(ctx, "steer", &input, nil)
}
func (h *Harness) FollowUp(ctx context.Context, input contract.TurnInput) error {
	return h.send(ctx, "follow_up", &input, nil)
}
func (h *Harness) Abort(ctx context.Context) error { return h.send(ctx, "abort", nil, nil) }

func (h *Harness) SessionFile() string { return h.session }

func completeLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), nil, nil
	}
	return 0, nil, nil
}

func (h *Harness) Delivered(_ context.Context, id contract.DeliverySeq) (bool, error) {
	file, err := os.Open(h.session)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	scanner.Split(completeLines)
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Role       string `json:"role"`
				DeliveryID string `json:"deliveryId"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return false, err
		}
		if entry.Type == "message" && entry.Message.Role == "user" && entry.Message.DeliveryID == strconv.FormatInt(int64(id), 10) {
			return true, nil
		}
	}
	return false, scanner.Err()
}

func (h *Harness) Close() error {
	h.closeOnce.Do(func() {
		close(h.stop)
		_ = h.stdin.Close()
		if h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
		err := h.cmd.Wait()
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			h.closeErr = err
		}
	})
	return h.closeErr
}
