package computers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Client addresses one session through the shared computer daemon.
type Client struct {
	Socket  string
	Session string
}

func NewClient(sessionID string) *Client {
	return &Client{Socket: ControlSocket(sessionID), Session: sessionID}
}

type connection struct {
	net.Conn
	mu   sync.Mutex
	scan *bufio.Scanner
	done chan struct{}
	once sync.Once
}

func (c *connection) Close() error { c.once.Do(func() { close(c.done) }); return c.Conn.Close() }
func (c *Client) connect(ctx context.Context) (*connection, error) {
	raw, err := (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, err
	}
	conn := &connection{Conn: raw, scan: bufio.NewScanner(raw), done: make(chan struct{})}
	conn.scan.Buffer(make([]byte, 4096), 4<<20)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-conn.done:
		}
	}()
	return conn, nil
}
func (c *connection) call(ctx context.Context, req Request, reply any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	defer c.SetDeadline(time.Time{})
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return err
	}
	if !c.scan.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := c.scan.Err(); err != nil {
			return err
		}
		return errors.New("computer daemon closed connection")
	}
	line := bytes.TrimSpace(c.scan.Bytes())
	if len(line) > 0 && line[0] == '{' {
		var failure struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(line, &failure); err != nil {
			return fmt.Errorf("computer response: %w", err)
		}
		if failure.Error != "" {
			return errors.New(failure.Error)
		}
	}
	return json.Unmarshal(line, reply)
}
func (c *Client) Call(ctx context.Context, req Request, reply any) error {
	conn, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	req.Session = c.Session
	return conn.call(ctx, req, reply)
}
func (c *Client) Status(ctx context.Context) (v Status, err error) {
	err = c.Call(ctx, Request{Op: "status"}, &v)
	return
}
func (c *Client) Register(ctx context.Context, room string) (v Meta, err error) {
	err = c.Call(ctx, Request{Op: "register", Room: room}, &v)
	return
}
func (c *Client) List(ctx context.Context, room string) (v []Status, err error) {
	err = c.Call(ctx, Request{Op: "list", Room: room}, &v)
	return
}
func (c *Client) Shot(ctx context.Context, fresh bool) (v Shot, err error) {
	err = c.Call(ctx, Request{Op: "shot", Fresh: fresh}, &v)
	return
}
func (c *Client) RecordStart(ctx context.Context, seconds, fps, kbps int) (v Recording, err error) {
	err = c.Call(ctx, Request{Op: "record.start", RecordSeconds: seconds, RecordFPS: fps, RecordKbps: kbps}, &v)
	return
}
func (c *Client) RecordStop(ctx context.Context) (v Recording, err error) {
	err = c.Call(ctx, Request{Op: "record.stop"}, &v)
	return
}
func (c *Client) Act(ctx context.Context, actions []Action, timeout time.Duration, status string) (v ActionResult, err error) {
	err = c.Call(ctx, Request{Op: "act", Status: status, Actions: actions, TimeoutMS: timeout.Milliseconds()}, &v)
	return
}

// ViewLease owns the control connection that keeps a viewer alive. Close it on
// WebSocket disconnect. The daemon releases human control on EOF automatically.
type ViewLease struct {
	View
	conn    *connection
	session string
}

func (c *Client) OpenView(ctx context.Context) (*ViewLease, error) {
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	var v View
	if err = conn.call(ctx, Request{Session: c.Session, Op: "view.open"}, &v); err != nil {
		conn.Close()
		return nil, err
	}
	return &ViewLease{View: v, conn: conn, session: c.Session}, nil
}
func (v *ViewLease) Close() error { return v.conn.Close() }
func (v *ViewLease) SetControl(ctx context.Context, mode ControlMode) (reply Control, err error) {
	err = v.conn.call(ctx, Request{Op: "control", Session: v.session, ViewerID: v.ViewerID, Mode: mode}, &reply)
	return
}
func (c *Client) SetControl(ctx context.Context, mode ControlMode, viewerID string) (v Control, err error) {
	err = c.Call(ctx, Request{Op: "control", Mode: mode, ViewerID: viewerID}, &v)
	return
}

// RequestHuman asks the person to take control and shows the reason until control returns.
func (c *Client) RequestHuman(ctx context.Context, reason string) (v Control, err error) {
	err = c.Call(ctx, Request{Op: "control", Mode: ControlHuman, Status: reason}, &v)
	return
}
func (c *Client) CloseView(ctx context.Context, id string) (v Viewers, err error) {
	err = c.Call(ctx, Request{Op: "view.close", ViewerID: id}, &v)
	return
}
func (c *Client) Stop(ctx context.Context) (v Stopped, err error) {
	err = c.Call(ctx, Request{Op: "stop"}, &v)
	return
}

// Subscribe delivers state and redacted action events until cancellation or EOF.
// An empty Client.Session subscribes to every registered computer.
func (c *Client) Subscribe(ctx context.Context) (<-chan Event, error) {
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	var reply map[string]bool
	if err = conn.call(ctx, Request{Op: "subscribe", Session: c.Session}, &reply); err != nil {
		conn.Close()
		return nil, err
	}
	out := make(chan Event, 32)
	go func() {
		defer close(out)
		defer conn.Close()
		for conn.scan.Scan() {
			var e Event
			if json.Unmarshal(conn.scan.Bytes(), &e) != nil {
				continue
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
