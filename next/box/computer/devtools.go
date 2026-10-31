package computer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

type target struct {
	Type      string `json:"type"`
	WebSocket string `json:"webSocketDebuggerUrl"`
}
type message struct {
	ID     int             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// DevTools is a single sequential page connection.
type DevTools struct {
	conn    *websocket.Conn
	id      int
	loaded  bool
	pending []message
}

// ConnectTimeoutError reports that Chromium did not expose a page in time.
type ConnectTimeoutError struct{}

func (ConnectTimeoutError) Error() string { return "timed out connecting to Chromium" }

// Connect waits for Chromium to expose its first page target.
func Connect(ctx context.Context, base string) (*DevTools, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return connect(ctx, base)
}

func connect(ctx context.Context, base string) (_ *DevTools, resultErr error) {
	defer func() {
		if resultErr != nil && ctx.Err() == context.DeadlineExceeded {
			resultErr = ConnectTimeoutError{}
		}
	}()
	for {
		var targets []target
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/list", nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			err = json.NewDecoder(resp.Body).Decode(&targets)
			resp.Body.Close()
			if err != nil {
				return nil, err
			}
			for _, t := range targets {
				if t.Type == "page" {
					conn, _, err := websocket.Dial(ctx, t.WebSocket, nil)
					if err != nil {
						return nil, err
					}
					conn.SetReadLimit(64 << 20)
					return &DevTools{conn: conn}, nil
				}
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (d *DevTools) Close() { _ = d.conn.Close(websocket.StatusNormalClosure, "") }

// Call sends one protocol command and returns its result.
func (d *DevTools) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if method == "Page.navigate" {
		d.loaded = false
	}
	d.id++
	payload, err := json.Marshal(message{ID: d.id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	if err = d.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return nil, err
	}
	for {
		_, data, err := d.conn.Read(ctx)
		if err != nil {
			return nil, err
		}
		var msg message
		if err = json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		if msg.Method == "Page.loadEventFired" {
			d.loaded = true
		}
		if msg.ID != d.id {
			if msg.Method == "Page.screencastFrame" {
				d.pending = append(d.pending, msg)
			}
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("DevTools %s: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	}
}

// WaitLoad waits for the load event, including one received during Navigate.
func (d *DevTools) WaitLoad(ctx context.Context) error {
	for !d.loaded {
		_, data, err := d.conn.Read(ctx)
		if err != nil {
			return err
		}
		var msg message
		if err = json.Unmarshal(data, &msg); err != nil {
			return err
		}
		if msg.Method == "Page.loadEventFired" {
			d.loaded = true
		}
	}
	return nil
}
