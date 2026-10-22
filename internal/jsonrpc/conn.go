// Package jsonrpc is a small JSON-RPC 2.0 client connection shared by the
// agent protocols Dyna speaks (ACP, Codex app-server). It owns framing,
// request/response routing, notifications, server-initiated requests and
// close handling. Protocol packages supply the methods and the handlers.
package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
)

// Error codes a request handler may return through *Error.
const (
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// Error is a JSON-RPC error object. A Request call returns it for an error
// reply; a request handler returns it to choose the code of its own reply.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("jsonrpc: %d %s: %s", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("jsonrpc: %d %s", e.Code, e.Message)
}

// ErrorCode returns the JSON-RPC error code.
func (e *Error) ErrorCode() int { return e.Code }

// TransportError reports a broken or closed transport, not a protocol reply.
type TransportError struct{ Cause error }

func (e *TransportError) Error() string { return fmt.Sprintf("jsonrpc transport: %v", e.Cause) }
func (e *TransportError) Unwrap() error { return e.Cause }

// ErrClosed is returned (wrapped) once the peer ended the connection.
var ErrClosed = errors.New("jsonrpc: connection closed")

// message is one JSON-RPC message in either direction.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Notification is one notification from the peer. Raw is the complete frame,
// for decoders that read the method and params envelope themselves.
type Notification struct {
	Method string
	Params json.RawMessage
	Raw    json.RawMessage
}

// Handlers answers what the peer sends. Every field is optional.
type Handlers struct {
	// Notification observes every notification. It runs on the reader
	// goroutine.
	Notification func(Notification)
	// Request answers a request from the peer on its own goroutine. It
	// returns a JSON-serializable result or an error; an *Error picks the
	// reply's code, any other error is an internal error. Nil replies
	// "method not found".
	Request func(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// Transport reads and writes one JSON-RPC message per frame.
type Transport interface {
	Read() ([]byte, error)
	Write([]byte) error
	Close() error
}

// Conn runs JSON-RPC over one transport. It is safe for concurrent use.
type Conn struct {
	tr       Transport
	handlers Handlers
	onClose  func()

	writeMu sync.Mutex
	nextID  int64

	mu       sync.Mutex
	pending  map[string]chan *message
	closed   bool
	closeErr error

	wg sync.WaitGroup
}

// NewConn starts protocol I/O over a transport. onClose runs once after the
// connection ended (it may stop a child process).
func NewConn(tr Transport, handlers Handlers, onClose func()) *Conn {
	c := &Conn{tr: tr, handlers: handlers, onClose: onClose, pending: map[string]chan *message{}}
	c.wg.Add(1)
	go c.readLoop()
	return c
}

// Close ends the connection and runs the closer. It is idempotent.
func (c *Conn) Close() {
	if c.shutdown(errors.New("jsonrpc: closed by client")) {
		c.wg.Wait()
	}
}

// shutdown marks the connection closed, fails pending requests and closes
// the transport. It reports whether this call did the shutdown.
func (c *Conn) shutdown(cause error) bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	c.closed = true
	c.closeErr = cause
	pending := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- nil:
		default:
		}
	}
	_ = c.tr.Close()
	if c.onClose != nil {
		c.onClose()
	}
	return true
}

// Done reports whether the transport ended.
func (c *Conn) Done() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *Conn) readLoop() {
	defer c.wg.Done()
	for {
		frame, err := c.tr.Read()
		if len(frame) > 0 {
			c.dispatch(frame)
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				err = fmt.Errorf("%w: peer connection ended", ErrClosed)
			} else {
				err = &TransportError{Cause: err}
			}
			c.shutdown(err)
			return
		}
	}
}

// SetNotification replaces the notification handler, for example to drop
// replay notifications while a session restores.
func (c *Conn) SetNotification(fn func(Notification)) {
	c.mu.Lock()
	c.handlers.Notification = fn
	c.mu.Unlock()
}

// snapshotHandlers copies the handlers under lock; the reader goroutine and
// SetNotification may otherwise race.
func (c *Conn) snapshotHandlers() Handlers {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.handlers
}

// call sends a request and returns the reply channel.
func (c *Conn) call(method string, params any) (chan *message, string, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, "", err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, "", &TransportError{Cause: c.closeErr}
	}
	c.nextID++
	// The wire id is a JSON string, so a peer echoing it back decodes as
	// the same type dispatch expects (a bare number would not).
	id := fmt.Sprintf("%d", c.nextID)
	ch := make(chan *message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	msg, err := json.Marshal(message{JSONRPC: "2.0", ID: json.RawMessage(strconv.Quote(id)), Method: method, Params: raw})
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, "", err
	}
	c.writeMu.Lock()
	writeErr := c.tr.Write(msg)
	c.writeMu.Unlock()
	if writeErr != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, "", &TransportError{Cause: writeErr}
	}
	return ch, id, nil
}

// Notify sends a notification (no reply).
func (c *Conn) Notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	msg, err := json.Marshal(message{JSONRPC: "2.0", Method: method, Params: raw})
	if err != nil {
		return err
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return &TransportError{Cause: c.closeErr}
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.tr.Write(msg)
}

// Request sends a request and decodes its result into out (nil discards it).
func (c *Conn) Request(ctx context.Context, method string, params any, out any) error {
	ch, id, err := c.call(method, params)
	if err != nil {
		return err
	}
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case msg := <-ch:
		if msg == nil {
			c.mu.Lock()
			cause := c.closeErr
			c.mu.Unlock()
			return &TransportError{Cause: cause}
		}
		if msg.Error != nil {
			return msg.Error
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(msg.Result, out); err != nil {
			return fmt.Errorf("jsonrpc: decode %s result: %w", method, err)
		}
		return nil
	}
}

func (c *Conn) dispatch(frame []byte) {
	var msg message
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	if msg.Method == "" {
		// A reply to one of our requests.
		var id string
		if err := json.Unmarshal(msg.ID, &id); err != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- &msg
		}
		return
	}
	if len(msg.ID) == 0 {
		if notify := c.snapshotHandlers().Notification; notify != nil {
			notify(Notification{Method: msg.Method, Params: msg.Params, Raw: frame})
		}
		return
	}
	// A request from the peer; answer it on a worker goroutine so a slow
	// handler never blocks the reader.
	go c.answerRequest(msg)
}

func (c *Conn) answerRequest(msg message) {
	handler := c.snapshotHandlers().Request
	if handler == nil {
		c.reply(msg.ID, nil, &Error{Code: CodeMethodNotFound, Message: fmt.Sprintf("method not found: %s", msg.Method)})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := handler(ctx, msg.Method, msg.Params)
	if err != nil {
		var rpcErr *Error
		if !errors.As(err, &rpcErr) {
			rpcErr = &Error{Code: CodeInternal, Message: err.Error()}
		}
		c.reply(msg.ID, nil, rpcErr)
		return
	}
	c.reply(msg.ID, result, nil)
}

func (c *Conn) reply(id json.RawMessage, result any, werr *Error) {
	msg := message{JSONRPC: "2.0", ID: id}
	if werr != nil {
		msg.Error = werr
	} else {
		raw, err := json.Marshal(result)
		if err != nil {
			msg.Error = &Error{Code: CodeInternal, Message: err.Error()}
		} else {
			msg.Result = raw
		}
	}
	encoded, err := json.Marshal(msg)
	if err != nil {
		return
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.tr.Write(encoded)
}
