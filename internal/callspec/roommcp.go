package callspec

import "encoding/json"

// RoomMCPCallRequest is one tool call to a room's outside MCP server. It names
// no caller: the engine takes the session from its authenticated socket.
type RoomMCPCallRequest struct {
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}
