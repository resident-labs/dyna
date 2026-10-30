package roommcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"dyna-agent/internal/untrusted"
)

const resultLimit = 64 << 10

// Content is a remote MCP content item.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Result contains the supported MCP tool result fields.
type Result struct {
	Content           []Content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

// WrapResult marks remote content as untrusted and caps the output.
func WrapResult(source string, result Result, secrets []string) (string, error) {
	open, closing := untrusted.Tags("mcp:" + source)
	if len(open)+len(closing)+64 > resultLimit {
		return "", errors.New("MCP source name is too long")
	}
	var body strings.Builder
	dropped := 0
	for _, item := range result.Content {
		if item.Type != "text" {
			dropped++
			continue
		}
		body.WriteString(item.Text)
		body.WriteByte('\n')
	}
	if dropped > 0 {
		fmt.Fprintf(&body, "[%d non-text content items omitted]\n", dropped)
	}
	if len(result.StructuredContent) > 0 {
		body.Write(result.StructuredContent)
		body.WriteByte('\n')
	}
	value := Scrub(body.String(), secrets)
	available := resultLimit - len(open) - len(closing)
	if len(value) > available {
		// Reserve space for the marker, and never split a UTF-8 character.
		n := available - 64
		for n > 0 && !utf8.RuneStart(value[n]) {
			n--
		}
		cut := len(value) - n
		value = value[:n] + fmt.Sprintf("\n[truncated: %d bytes cut]\n", cut)
	}
	return open + value + closing, nil
}
