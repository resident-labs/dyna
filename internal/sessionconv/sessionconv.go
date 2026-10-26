// Package sessionconv converts one harness conversation into another
// harness's session file. It reads the active branch of the source session and
// writes a new session for the target harness, so a transfer can resume the
// same conversation instead of starting from a written handoff.
//
// The package keeps one typed conversation between the readers and writers.
// Only user text and images, assistant text and tool calls, and tool results
// cross the boundary. Thinking blocks, provider signatures, usage, model
// attribution and system prompts do not: the target harness rebuilds its own.
package sessionconv

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Harness names a session file format this package converts.
type Harness string

const (
	HarnessPi     Harness = "pi"
	HarnessClaude Harness = "claude-code"
)

// Supported harness versions. A session is converted only when the installed
// source and target versions match these, so a format change in either harness
// falls back to the written handoff instead of writing a file it cannot read.
const (
	PiSupportedVersion     = "1.4.0"
	ClaudeSupportedVersion = "2.1.309"

	// PiSessionFormat is the version field of the Pi session header this
	// reader accepts (the current tree format).
	PiSessionFormat = 3

	// ClaudeSessionVersion is the version field a Claude entry records.
	ClaudeSessionVersion = "2.1.309"

	maxSessionBytes = 32 << 20
	maxSessionLines = 20000
)

// Errors that make a conversion fall back to the written handoff.
var (
	ErrUnsupportedVersion = errors.New("session format version is not supported")
	ErrUnsupportedShape   = errors.New("session holds a shape this converter does not map")
	ErrBrokenChain        = errors.New("session entries do not form one conversation chain")
	ErrTooLarge           = errors.New("session is too large to convert")
)

// Supported reports whether version is the pinned version for harness.
func Supported(harness Harness, version string) bool {
	switch harness {
	case HarnessPi:
		return version == PiSupportedVersion
	case HarnessClaude:
		return version == ClaudeSupportedVersion
	}
	return false
}

// Role is one conversation speaker.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// BlockType names one content block.
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockImage      BlockType = "image"
	BlockToolCall   BlockType = "tool_call"
	BlockToolResult BlockType = "tool_result"
)

// Message is one conversation message in the common model.
type Message struct {
	Role   Role
	Blocks []Block
	// Timestamp is the source entry timestamp when it had one.
	Timestamp time.Time
}

// Block is one typed content block. Only the field named by Type is set.
type Block struct {
	Type   BlockType
	Text   string
	Image  *Image
	Call   *ToolCall
	Result *ToolResult
}

// Image is one inline base64 image.
type Image struct {
	MIME string
	Data string
}

// ToolCall is one assistant tool request. Arguments stay JSON because every
// tool has its own schema.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ToolResult is one tool answer, in a tool-role message.
type ToolResult struct {
	CallID  string
	Content []Block
	IsError bool
}

// Conversation is the active conversation of one source session.
type Conversation struct {
	// Summary is the latest compaction summary, when the source compacted.
	// It is carried as one labelled user message, never as a native entry.
	Summary  string
	Messages []Message
}

// textBlock is a small helper for callers that build text content.
func textBlock(text string) Block { return Block{Type: BlockText, Text: text} }

// newUUID returns a version 4 UUID. crypto/rand.Read always fills the buffer
// completely on a supported host.
func newUUID() string {
	var value [16]byte
	_, _ = rand.Read(value[:])
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

// NewSessionID returns a fresh session id for a converted target session.
func NewSessionID() string { return newUUID() }
