// Package acp is a JSON-RPC 2.0 client for the Agent Client Protocol over a newline-delimited stdio stream. It carries
// only what Dyna's agy harness uses: calls to the agent, and the agent's own requests and notifications.
package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// MethodNotFound is the JSON-RPC code for a method the client does not serve.
const MethodNotFound = -32601

// MaxLine bounds one message. A longer line is dropped whole, as T3 drops lines past its bound, and never fails the stream.
const MaxLine = 16 << 20

// Error is a JSON-RPC error object, from the agent or to it.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("acp error %d: %s", e.Code, e.Message) }

// ErrClosed is what a call returns once the stream has ended.
var ErrClosed = errors.New("acp stream closed")

// Handler serves what the agent sends. Both run on the reading goroutine, in order, so they must not block or call the client.
type Handler struct {
	// Request answers an agent request. An *Error becomes the JSON-RPC error; another error becomes -32603. Nil: method not found.
	Request func(method string, params json.RawMessage) (any, error)
	Notify  func(method string, params json.RawMessage)
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
	closed bool            // the stream ended before an answer came
}

// Client speaks to one agent.
type Client struct {
	handler Handler
	writeMu sync.Mutex
	w       io.Writer

	mu      sync.Mutex
	next    int64
	pending map[int64]chan message
	done    chan struct{}
}

// NewClient starts reading r. The client is done when r ends.
func NewClient(r io.Reader, w io.Writer, h Handler) *Client {
	c := &Client{handler: h, w: w, pending: map[int64]chan message{}, done: make(chan struct{})}
	go c.read(r)
	return c
}

// Done closes when the stream ends.
func (c *Client) Done() <-chan struct{} { return c.done }

// ReadLine returns the next line without its newline. A line longer than max is skipped and the next one returned, so a
// huge line never ends the stream. The error is the reader's, io.EOF at the end.
func ReadLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	over := false
	for {
		part, err := r.ReadSlice('\n')
		if !over {
			if len(line)+len(part) > max+1 {
				over, line = true, nil
			} else {
				line = append(line, part...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		// A last line with no newline is an interrupted write and is not a message.
		if err != nil {
			return nil, err
		}
		if over {
			over, line = false, nil
			continue
		}
		return bytes.TrimRight(line, "\r\n"), nil
	}
}

func (c *Client) read(r io.Reader) {
	reader := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := ReadLine(reader, MaxLine)
		if err != nil {
			break
		}
		var m message
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			c.serve(m)
		case m.Method != "":
			if c.handler.Notify != nil {
				c.handler.Notify(m.Method, m.Params)
			}
		case len(m.ID) > 0:
			var id int64
			if json.Unmarshal(m.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	c.mu.Lock()
	for id, ch := range c.pending {
		ch <- message{closed: true}
		delete(c.pending, id)
	}
	close(c.done)
	c.mu.Unlock()
}

func (c *Client) serve(m message) {
	var result any
	err := error(&Error{Code: MethodNotFound, Message: "method not found: " + m.Method})
	if c.handler.Request != nil {
		result, err = c.handler.Request(m.Method, m.Params)
	}
	reply := map[string]any{"jsonrpc": "2.0", "id": m.ID}
	var rpc *Error
	switch {
	case errors.As(err, &rpc):
		reply["error"] = rpc
	case err != nil:
		reply["error"] = &Error{Code: -32603, Message: err.Error()}
	default:
		reply["result"] = result
	}
	_ = c.write(reply)
}

func (c *Client) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.w.Write(append(data, '\n'))
	return err
}

// Notify sends a notification.
func (c *Client) Notify(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// Pending is a call that has been written and not yet answered.
type Pending struct {
	ch chan message
}

// Start writes a call and returns without waiting for its answer.
func (c *Client) Start(method string, params any) (*Pending, error) {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return nil, ErrClosed
	default:
	}
	c.next++
	id := c.next
	ch := make(chan message, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	return &Pending{ch: ch}, nil
}

// Wait returns the answer: the agent's *Error, or the result decoded into out (which may be nil).
func (p *Pending) Wait(ctx context.Context, out any) error {
	select {
	case m := <-p.ch:
		if m.closed {
			return ErrClosed
		}
		if m.Error != nil {
			return m.Error
		}
		if out == nil || len(m.Result) == 0 {
			return nil
		}
		return json.Unmarshal(m.Result, out)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Call is Start and Wait.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	p, err := c.Start(method, params)
	if err != nil {
		return err
	}
	return p.Wait(ctx, out)
}
