// Package pi drives a retained Pi RPC process.
package pi

import (
	"bufio"
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

type command struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Message    string `json:"message,omitempty"`
	DeliveryID string `json:"deliveryId,omitempty"`
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
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	session  string
	writeMu  sync.Mutex
	mu       sync.Mutex
	pending  map[string]chan event
	events   chan event
	done     chan struct{}
	stop     chan struct{}
	readErr  error
	sequence atomic.Uint64
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

func (h *Harness) send(ctx context.Context, typ string, input *contract.TurnInput) error {
	id := strconv.FormatUint(h.sequence.Add(1), 10)
	c := command{ID: id, Type: typ}
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
		return err
	}
	select {
	case result := <-ch:
		if !result.Success {
			return fmt.Errorf("pi %s: %s", typ, result.Error)
		}
		return nil
	case <-h.done:
		return h.readerError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Harness) readerError() error {
	if h.readErr != nil {
		return h.readErr
	}
	return errors.New("pi RPC stream closed")
}

func (h *Harness) Start(ctx context.Context, id contract.TurnID, input contract.TurnInput) (contract.TurnEnded, error) {
	result := contract.TurnEnded{Turn: id, Outcome: contract.TurnOutcome("completed")}
	if err := h.send(ctx, "prompt", &input); err != nil {
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
	return h.send(ctx, "steer", &input)
}
func (h *Harness) FollowUp(ctx context.Context, input contract.TurnInput) error {
	return h.send(ctx, "follow_up", &input)
}
func (h *Harness) Abort(ctx context.Context) error { return h.send(ctx, "abort", nil) }

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
	close(h.stop)
	_ = h.stdin.Close()
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
	return h.cmd.Wait()
}
