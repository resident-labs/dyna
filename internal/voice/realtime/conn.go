package realtime

// Ported from qwen-audio-agent server/src/voice/realtime-provider-session.mjs.

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorilla/websocket"
)

// Conn is one open connection to a provider.
type Conn struct {
	Provider Provider
	ws       *websocket.Conn
	mu       sync.Mutex // serializes writes
	queued   []Event    // events of a message read already, still to hand out
}

// multiDecoder is a Protocol that maps one server message to several
// events.
type multiDecoder interface {
	DecodeAll(data []byte) ([]Event, error)
}

// Dial connects to p and sends the session. When p acknowledges session
// updates, Dial returns once the session took effect, or with the
// provider's error.
func Dial(ctx context.Context, p Provider, s Session) (*Conn, error) {
	url, header, err := p.Endpoint(ctx)
	if err != nil {
		return nil, err
	}
	ws, resp, err := websocket.DefaultDialer.DialContext(ctx, url, header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("%s refused the connection: %s", p.Label, resp.Status)
		}
		return nil, fmt.Errorf("could not reach %s: %w", p.Label, err)
	}
	c := &Conn{Provider: p, ws: ws}
	stop := context.AfterFunc(ctx, func() { ws.Close() })
	defer stop()
	for p.Caps.QueuesBeforeSession {
		ev, err := c.Next()
		if err != nil {
			ws.Close()
			return nil, errors.Join(ctx.Err(), err)
		}
		if ev.Kind == Error {
			ws.Close()
			return nil, fmt.Errorf("%s refused the connection: %s", p.Label, ev.Text)
		}
		if ev.Kind == SessionQueued {
			break
		}
	}
	if err := c.Send(p.Protocol.SessionUpdate(p.Session(s))); err != nil {
		ws.Close()
		return nil, err
	}
	for p.Caps.AcknowledgesSessionUpdate {
		ev, err := c.Next()
		if err != nil {
			ws.Close()
			return nil, errors.Join(ctx.Err(), err)
		}
		if ev.Kind == Error {
			ws.Close()
			return nil, fmt.Errorf("%s refused the session: %s", p.Label, ev.Text)
		}
		if ev.Kind == SessionReady {
			break
		}
	}
	return c, nil
}

// Send writes one client event. It is safe for concurrent use.
func (c *Conn) Send(event any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteJSON(event)
}

// Next reads the next server event. Only one goroutine may call it.
func (c *Conn) Next() (Event, error) {
	m, multi := c.Provider.Protocol.(multiDecoder)
	for len(c.queued) == 0 {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return Event{}, err
		}
		if !multi {
			return c.Provider.Protocol.Decode(data)
		}
		if c.queued, err = m.DecodeAll(data); err != nil {
			return Event{}, err
		}
	}
	ev := c.queued[0]
	c.queued = c.queued[1:]
	return ev, nil
}

// Close ends the connection.
func (c *Conn) Close() error { return c.ws.Close() }
