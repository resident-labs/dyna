package engineclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"

	"github.com/gorilla/websocket"

	"dyna-agent/internal/callspec"
)

// Stream opens a WebSocket to the engine route at path over the engine socket,
// for a command that talks to the engine in both directions at once. It does
// not wait for a restarting engine: the stream is live, so a cut is the
// caller's to handle. A refusal comes back as a *RemoteError.
func (c *Client) Stream(ctx context.Context, path string) (*websocket.Conn, error) {
	dialer := websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := c.http.Transport.(*http.Transport).DialContext(ctx, "unix", "engine")
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
				return nil, ErrNotRunning
			}
			return conn, err
		},
	}
	header := http.Header{}
	if c.build != "" {
		header.Set(callspec.VersionHeader, c.build)
	}
	conn, res, err := dialer.DialContext(ctx, "ws://engine"+path, header)
	if err != nil {
		if errors.Is(err, ErrNotRunning) {
			return nil, ErrNotRunning
		}
		if res != nil {
			defer res.Body.Close()
			var reply struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			if json.NewDecoder(res.Body).Decode(&reply) != nil || reply.Error == "" {
				return nil, fmt.Errorf("engine: %s", res.Status)
			}
			return nil, &RemoteError{Status: res.StatusCode, Message: reply.Error, Code: reply.Code}
		}
		return nil, err
	}
	return conn, nil
}
