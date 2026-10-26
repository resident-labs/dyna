// Package shellmcp is Dyna's MCP server for shell commands. It replaces each
// harness's native shell: a command runs in the foreground for a short time,
// then moves to the background as a shells job that outlives the turn.
package shellmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

type request struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// initializeResult is the answer to a client's initialize request.
type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    capabilities `json:"capabilities"`
	ServerInfo      serverInfo   `json:"serverInfo"`
}

// capabilities advertises what this server offers.
type capabilities struct {
	Tools struct{} `json:"tools"`
}

// serverInfo names this server to the client.
type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// toolListResult is the answer to tools/list.
type toolListResult struct {
	Tools []toolDefinition `json:"tools"`
}

// callResult is the answer to tools/call.
type callResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError"`
}

// contentItem is one piece of a tool answer.
type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Serve answers newline-delimited JSON-RPC requests until in ends. Tool calls
// run concurrently, so a long wait does not block pings or cancellations.
func Serve(ctx context.Context, in io.Reader, out io.Writer, tools *Tools) error {
	var calls sync.WaitGroup
	ctx, cancel := context.WithCancel(ctx)
	// When the client goes away, stop waiting. Jobs keep running.
	defer calls.Wait()
	defer cancel()
	var writeMu sync.Mutex
	send := func(r response) error {
		r.JSONRPC = "2.0"
		b, _ := json.Marshal(r)
		writeMu.Lock()
		defer writeMu.Unlock()
		_, err := out.Write(append(b, '\n'))
		return err
	}
	var mu sync.Mutex
	running := map[string]context.CancelFunc{}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			send(response{ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		notification := len(req.ID) == 0
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
				ClientInfo      struct {
					Name string `json:"name"`
				} `json:"clientInfo"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			send(response{ID: req.ID, Result: initializeResult{
				ProtocolVersion: p.ProtocolVersion,
				Capabilities:    capabilities{},
				ServerInfo:      serverInfo{Name: "dyna", Version: "1"},
			}})
		case "ping":
			send(response{ID: req.ID, Result: struct{}{}})
		case "tools/list":
			send(response{ID: req.ID, Result: toolListResult{Tools: tools.tools()}})
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
				Meta      struct {
					Session string `json:"ai.opencode/sessionID"`
				} `json:"_meta"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				send(response{ID: req.ID, Error: &rpcError{-32602, "invalid params"}})
				continue
			}
			callCtx, stop := context.WithCancel(ctx)
			key := string(req.ID)
			mu.Lock()
			running[key] = stop
			mu.Unlock()
			calls.Add(1)
			go func() {
				defer calls.Done()
				r := tools.Call(callCtx, p.Name, p.Arguments, p.Meta.Session)
				// Cancellation takes mu too, so it lands before or after
				// this check, never between the check and the send.
				mu.Lock()
				delete(running, key)
				deliver(callCtx, tools, r, func(r Result) error {
					return send(response{ID: req.ID, Result: callResult{Content: []contentItem{{Type: "text", Text: r.Text}}, IsError: r.Failed}})
				})
				mu.Unlock()
				stop()
				if tools.Restart != nil && tools.outdated != nil && tools.outdated.Load() {
					// The answer above is on the wire, so the model sees the
					// error once; the restart puts the next request on the
					// current binary with stdio inherited (D-171).
					tools.Restart()
				}
			}()
		case "notifications/cancelled":
			var p struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			_ = json.Unmarshal(req.Params, &p)
			mu.Lock()
			if stop := running[string(p.RequestID)]; stop != nil {
				stop()
			}
			mu.Unlock()
		default:
			if !notification {
				send(response{ID: req.ID, Error: &rpcError{-32601, "method not found: " + req.Method}})
			}
		}
	}
	cancel()
	calls.Wait()
	return errors.Join(scanner.Err(), tools.Close())
}

// deliver sends a result unless its request was cancelled, and commits what
// it showed only after the client has it. A cancelled request gets no
// response, so its exit still wakes the session and its output stays unread.
func deliver(ctx context.Context, tools *Tools, r Result, send func(Result) error) {
	if ctx.Err() != nil || send(r) != nil {
		_ = tools.Abort(r)
		return
	}
	_ = tools.Commit(r)
}
