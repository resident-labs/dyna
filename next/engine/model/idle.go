package model

import (
	"context"
	"net"
	"net/http"
	"time"
)

const streamIdleTimeout = 300 * time.Second

// idleConn measures wire silence, including the wait for response headers. Every
// successful read (including SSE comments) buys another full idle interval.
// HTTP transport cancellation closes the connection and interrupts Read.
type idleConn struct {
	net.Conn
	idle time.Duration
}

func (c *idleConn) Read(p []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.idle)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}

func idleTransport(socket string, idle time.Duration) *http.Transport {
	return &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err != nil {
			return nil, err
		}
		return &idleConn{Conn: conn, idle: idle}, nil
	}}
}
