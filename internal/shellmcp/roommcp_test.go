package shellmcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"dyna-agent/internal/callspec"
)

// listNames returns the tool names a Serve exchange lists for tools.
func listNames(t *testing.T, tools *Tools) []string {
	t.Helper()
	var out strings.Builder
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"
	if err := Serve(context.Background(), strings.NewReader(input), &out, tools); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	var reply struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &reply); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range reply.Result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestRoomMCPToolsListOnlyForGrantedCallers(t *testing.T) {
	access := func(granted bool, err error) *RoomMCP {
		return &RoomMCP{Access: func(context.Context) (bool, error) { return granted, err }}
	}
	for name, tools := range map[string]*Tools{
		"no room MCP":   {},
		"not granted":   {RoomMCP: access(false, nil)},
		"access errors": {RoomMCP: access(true, errors.New("engine down"))},
	} {
		for _, listed := range listNames(t, tools) {
			if strings.HasPrefix(listed, "room_mcp") {
				t.Errorf("%s: listed %s", name, listed)
			}
		}
	}
	names := listNames(t, &Tools{RoomMCP: access(true, nil)})
	if len(names) < 2 || names[len(names)-2] != "room_mcp_tools" || names[len(names)-1] != "room_mcp_call" {
		t.Fatalf("granted caller sees %v", names)
	}
}

func TestRoomMCPCallPassesOnlyTheNamedRequest(t *testing.T) {
	var got callspec.RoomMCPCallRequest
	tools := &Tools{RoomMCP: &RoomMCP{Invoke: func(_ context.Context, in callspec.RoomMCPCallRequest) (string, error) {
		got = in
		return "wrapped answer", nil
	}}}
	text, err := tools.roomMCPCall(context.Background(), callspec.RoomMCPCallRequest{Server: "board", Tool: "get", Arguments: json.RawMessage(`{"id":1}`)})
	if err != nil || text != "wrapped answer" {
		t.Fatalf("call = %q, %v", text, err)
	}
	if got.Server != "board" || got.Tool != "get" || string(got.Arguments) != `{"id":1}` {
		t.Fatalf("engine received %+v", got)
	}
	if _, err := tools.roomMCPCall(context.Background(), callspec.RoomMCPCallRequest{Server: "board"}); err == nil {
		t.Fatal("a call without a tool was sent to the engine")
	}
	if _, err := (&Tools{}).roomMCPCall(context.Background(), callspec.RoomMCPCallRequest{Server: "a", Tool: "b"}); err == nil {
		t.Fatal("a session without room MCP made a call")
	}
	if _, err := (&Tools{}).roomMCPTools(context.Background()); err == nil {
		t.Fatal("a session without room MCP listed tools")
	}
}
