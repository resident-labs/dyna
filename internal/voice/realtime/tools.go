package realtime

// Ported from qwen-audio-agent server/src/frontend/tools/frontend-tool-loop.mjs
// and tool-call-handler.mjs.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Tool is a Go function the model may call.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage // a JSON Schema object
	Run         func(ctx context.Context, args json.RawMessage) (string, error)
	// Later marks work whose answer comes later, without a receipt reply.
	Later bool
	// Silent returns an empty tool output and asks for no follow-up reply.
	Silent bool
}

// laterTool reports whether the tool named name answers later.
func laterTool(tools []Tool, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return t.Later
		}
	}
	return false
}

func silentTool(tools []Tool, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return t.Silent
		}
	}
	return false
}

// ToolTimeout bounds one tool run, and MaxToolOutput the bytes of its
// answer, so a slow or chatty tool cannot hold up or flood the call.
const (
	ToolTimeout   = 10 * time.Second
	MaxToolOutput = 4096
)

// RunTool returns a tool's JSON result or error. A successful Silent tool
// returns empty output. Unknown tools and timeouts are errors too.
func RunTool(ctx context.Context, tools []Tool, call Event) string {
	var tool *Tool
	for i := range tools {
		if tools[i].Name == call.Name {
			tool = &tools[i]
		}
	}
	if tool == nil {
		return toolOutput("", fmt.Errorf("there is no tool %q", call.Name))
	}
	ctx, cancel := context.WithTimeout(ctx, ToolTimeout)
	defer cancel()
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, err := tool.Run(ctx, call.Args)
		done <- result{text, err}
	}()
	select {
	case r := <-done:
		if tool.Silent && r.err == nil {
			return ""
		}
		return toolOutput(r.text, r.err)
	case <-ctx.Done():
		return toolOutput("", fmt.Errorf("%s did not answer in time", call.Name))
	}
}

// failed reports whether out, from toolOutput, is an error.
func failed(out string) bool { return strings.HasPrefix(out, `{"error":`) }

// toolOutput is {"result": text} or {"error": message}, cut to MaxToolOutput.
func toolOutput(text string, err error) string {
	key := "result"
	if err != nil {
		key, text = "error", err.Error()
	}
	b, _ := json.Marshal(map[string]string{key: text})
	for len(b) > MaxToolOutput {
		cut := len(text) - (len(b) - MaxToolOutput) - len("…")
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:max(cut, 0)] + "…"
		b, _ = json.Marshal(map[string]string{key: text})
	}
	return string(b)
}
