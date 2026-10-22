package jsonrpc

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

// wsTransport carries one JSON-RPC message per WebSocket text frame.
type wsTransport struct {
	conn *websocket.Conn
}

// DialWebSocket connects to a WebSocket endpoint that speaks JSON-RPC. The
// URL carries any credentials the server needs.
func DialWebSocket(ctx context.Context, rawURL string, handlers Handlers) (*Conn, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("jsonrpc: url: %w", err)
	}
	dialer := &websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, "ws://"+parsed.Host+parsed.RequestURI(), nil)
	if err != nil {
		return nil, &TransportError{Cause: fmt.Errorf("dial %s: %w", parsed.Host, err)}
	}
	return NewConn(&wsTransport{conn: conn}, handlers, nil), nil
}

func (t *wsTransport) Read() ([]byte, error) {
	for {
		kind, data, err := t.conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		if kind == websocket.TextMessage || kind == websocket.BinaryMessage {
			return data, nil
		}
	}
}

func (t *wsTransport) Write(b []byte) error {
	return t.conn.WriteMessage(websocket.TextMessage, b)
}

func (t *wsTransport) Close() error {
	_ = t.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second))
	return t.conn.Close()
}
