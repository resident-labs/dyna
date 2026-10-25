// Package acp implements the client half of the Agent Client Protocol
// (ACP) on top of the shared JSON-RPC connection in internal/jsonrpc. Grok
// is driven over a WebSocket connection to its `grok agent serve` process;
// Antigravity's server speaks the same messages over stdio. One connection
// runs one agent conversation; a caller that keeps the agent process alive
// gets one long-lived agent per Dyna session.
package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"dyna-agent/internal/jsonrpc"
)

// ProtocolVersion is the ACP version Dyna speaks. Grok 1.0.62 negotiates 1.
const ProtocolVersion = 1

// Update is one agent notification (session/update and agent extensions).
// Raw is the complete JSON-RPC notification frame, for decoders that read
// the method and params envelope.
type Update struct {
	Method  string
	Params  json.RawMessage
	Session string
	Raw     json.RawMessage
}

// PermissionRequest is a session/request_permission the agent asks the client.
type PermissionRequest struct {
	SessionID  string             `json:"sessionId"`
	ToolCallID string             `json:"toolCallId"`
	Options    []PermissionOption `json:"options"`
}

// PermissionOption is one outcome the agent offers for a permission request.
type PermissionOption struct {
	Kind     string `json:"kind"`
	OptionID string `json:"optionId,omitempty"`
	Name     string `json:"name"`
}

// PermissionBehavior is the outcome a client takes on a permission request.
type PermissionBehavior string

const (
	// PermissionAllowOnce allows this one call.
	PermissionAllowOnce PermissionBehavior = "allow_once"
	// PermissionAllowAlways allows every call of this kind.
	PermissionAllowAlways PermissionBehavior = "allow_always"
	// PermissionRejectOnce rejects this one call.
	PermissionRejectOnce PermissionBehavior = "reject_once"
	// PermissionCancel cancels the question itself.
	PermissionCancel PermissionBehavior = "cancel"
	// PermissionCancelled is the outcome Dyna sends when nothing decides.
	PermissionCancelled PermissionBehavior = "cancelled"
)

// PermissionResponse answers a permission request with one behavior kind.
type PermissionResponse struct {
	Behavior PermissionBehavior
	OptionID string
	Updated  json.RawMessage
}

// PermissionOutcome is the outcome of a permission request on the wire.
type PermissionOutcome struct {
	Behavior PermissionBehavior `json:"behavior"`
	OptionID string             `json:"optionId,omitempty"`
}

// PermissionResult is the reply to session/request_permission.
type PermissionResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// ElicitationCancel is the default reply to an unanswered elicitation request.
type ElicitationCancel struct {
	Action string `json:"action"`
}

// Handlers answers the requests an ACP agent sends its client. Every field is
// optional; unhandled requests get a protocol-safe default (permission asks
// are cancelled, elicitation is cancelled, file access is denied).
type Handlers struct {
	// Update observes every notification. It runs on the reader goroutine.
	Update func(Update)
	// Permission answers a permission request. Nil cancels the request.
	Permission func(context.Context, PermissionRequest) PermissionResponse
	// Elicitation answers a session/elicitation or elicitation/create
	// request. Nil cancels the request.
	Elicitation func(context.Context, json.RawMessage) any
	// Stderr observes the agent's raw stderr lines (stdio transport only).
	Stderr func(string)
	// UnknownRequest observes requests without a handler. It must answer
	// with a JSON-serializable result or an error.
	UnknownRequest func(context.Context, string, json.RawMessage) (any, error)
}

// NotificationHandler adapts an Update observer to a jsonrpc notification
// handler. It decodes the session id the agent puts in the params.
func NotificationHandler(update func(Update)) func(jsonrpc.Notification) {
	if update == nil {
		return nil
	}
	return func(n jsonrpc.Notification) {
		var params struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(n.Params, &params)
		update(Update{Method: n.Method, Params: n.Params, Session: params.SessionID, Raw: n.Raw})
	}
}

// wire maps the ACP handlers to generic JSON-RPC handlers.
func (h Handlers) wire() jsonrpc.Handlers {
	return jsonrpc.Handlers{Notification: NotificationHandler(h.Update), Request: h.answerRequest}
}

// AttachTransport runs ACP over a framed transport owned by the session host.
func AttachTransport(tr jsonrpc.Transport, handlers Handlers) *jsonrpc.Conn {
	return jsonrpc.NewConn(tr, handlers.wire(), nil)
}

// AttachStdio runs ACP over a child process's stdio pipes. kill stops the
// process tree.
func AttachStdio(stdin io.WriteCloser, stdout, stderr io.ReadCloser, kill func(), handlers Handlers) *jsonrpc.Conn {
	return jsonrpc.AttachStdio(stdin, stdout, stderr, kill, handlers.Stderr, handlers.wire())
}

// DialWebSocket connects to a grok agent serve endpoint. The URL already
// carries the server key (ws://host:port/ws?server-key=...).
func DialWebSocket(ctx context.Context, rawURL string, handlers Handlers) (*jsonrpc.Conn, error) {
	return jsonrpc.DialWebSocket(ctx, rawURL, handlers.wire())
}

func (h Handlers) answerRequest(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "session/request_permission":
		var req PermissionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid params"}
		}
		if h.Permission != nil {
			resp := h.Permission(ctx, req)
			return PermissionResult{Outcome: PermissionOutcome{Behavior: resp.Behavior, OptionID: resp.OptionID}}, nil
		}
		return PermissionResult{Outcome: PermissionOutcome{Behavior: PermissionCancelled}}, nil
	case "session/elicitation", "elicitation/create", "_session/elicitation":
		if h.Elicitation != nil {
			return h.Elicitation(ctx, params), nil
		}
		return ElicitationCancel{Action: "cancel"}, nil
	}
	if h.UnknownRequest != nil {
		return h.UnknownRequest(ctx, method, params)
	}
	return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: fmt.Sprintf("dyna does not implement %s", method)}
}
