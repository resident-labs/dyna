package sessionconv

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// claudeEntry is one record of a Claude Code session file. The fields are the
// ones Claude Code 2.1.309 reads to rebuild a resumed conversation: a chained
// uuid/parentUuid record whose message holds the role and content.
type claudeEntry struct {
	ParentUUID  *string       `json:"parentUuid"`
	IsSidechain bool          `json:"isSidechain"`
	Type        string        `json:"type"`
	Message     claudeMessage `json:"message"`
	UUID        string        `json:"uuid"`
	Timestamp   string        `json:"timestamp"`
	UserType    string        `json:"userType,omitempty"`
	Entrypoint  string        `json:"entrypoint,omitempty"`
	Cwd         string        `json:"cwd"`
	SessionID   string        `json:"sessionId"`
	Version     string        `json:"version"`
}

type claudeMessage struct {
	Role       string        `json:"role"`
	Type       string        `json:"type,omitempty"`
	Model      string        `json:"model,omitempty"`
	ID         string        `json:"id,omitempty"`
	Content    []claudeBlock `json:"content"`
	StopReason string        `json:"stop_reason,omitempty"`
	Usage      *claudeUsage  `json:"usage,omitempty"`
}

type claudeBlock struct {
	Type      string             `json:"type"`
	Text      string             `json:"text,omitempty"`
	ID        string             `json:"id,omitempty"`
	Name      string             `json:"name,omitempty"`
	Input     json.RawMessage    `json:"input,omitempty"`
	ToolUseID string             `json:"tool_use_id,omitempty"`
	Content   []claudeBlock      `json:"content,omitempty"`
	IsError   bool               `json:"is_error,omitempty"`
	Source    *claudeImageSource `json:"source,omitempty"`
}

type claudeImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
}

// claudeProjectKey is how Claude Code 2.1.309 names a project folder: every
// character that is not a letter or digit becomes a dash. A path longer than
// the folder name it accepts makes the converter decline instead of guessing.
func claudeProjectKey(cwd string) (string, error) {
	var builder strings.Builder
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
	}
	key := builder.String()
	if key == "" || len(key) > 200 {
		return "", fmt.Errorf("%w: claude project folder for %q", ErrUnsupportedShape, cwd)
	}
	return key, nil
}

// WriteClaude writes conversation as a Claude Code session file named id under
// dir, the resolved CLAUDE_CONFIG_DIR of the target account. It returns the
// session file path. cwd is the directory the resumed turn will run in, which
// also selects Claude's project folder.
func WriteClaude(dir, cwd, id, model string, conversation Conversation) (string, error) {
	if dir == "" || cwd == "" || id == "" {
		return "", fmt.Errorf("%w: claude session needs a directory, cwd and id", ErrUnsupportedShape)
	}
	key, err := claudeProjectKey(cwd)
	if err != nil {
		return "", err
	}
	entries, err := claudeEntries(cwd, id, model, conversation)
	if err != nil {
		return "", err
	}
	projectDir := filepath.Join(dir, "projects", key)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(projectDir, id+".jsonl")
	temp, err := os.CreateTemp(projectDir, ".transfer-*.jsonl")
	if err != nil {
		return "", err
	}
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(temp.Name())
	}
	writer := jsonEncoder(temp)
	for _, entry := range entries {
		if err := writer(entry); err != nil {
			cleanup()
			return "", err
		}
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return "", err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(temp.Name())
		return "", err
	}
	if err := os.Chmod(temp.Name(), 0o600); err != nil {
		_ = os.Remove(temp.Name())
		return "", err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		_ = os.Remove(temp.Name())
		return "", err
	}
	return path, nil
}

// jsonEncoder writes one JSON record per line.
func jsonEncoder(file *os.File) func(any) error {
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	return encoder.Encode
}

// claudeEntries turns the common conversation into chained Claude records.
func claudeEntries(cwd, id, model string, conversation Conversation) ([]claudeEntry, error) {
	builder := claudeBuilder{cwd: cwd, sessionID: id, model: model}
	if conversation.Summary != "" {
		builder.addUser([]claudeBlock{{Type: "text", Text: "[Conversation summary carried from the previous harness]\n" + conversation.Summary}}, time.Time{})
	}
	if len(conversation.Messages) > 0 && conversation.Messages[0].Role != RoleUser {
		builder.addUser([]claudeBlock{{Type: "text", Text: "[The conversation below continues from another harness.]"}}, time.Time{})
	}
	for _, message := range conversation.Messages {
		switch message.Role {
		case RoleUser:
			blocks := claudeContent(message.Blocks)
			if len(blocks) == 0 {
				continue
			}
			if err := builder.flushTools(); err != nil {
				return nil, err
			}
			builder.addUser(blocks, message.Timestamp)
		case RoleAssistant:
			if err := builder.flushTools(); err != nil {
				return nil, err
			}
			if err := builder.addAssistant(message); err != nil {
				return nil, err
			}
		case RoleTool:
			if err := builder.addToolResult(message); err != nil {
				return nil, err
			}
		}
	}
	if err := builder.flushTools(); err != nil {
		return nil, err
	}
	return builder.entries, nil
}

// claudeBuilder keeps the chain tail and the tool results waiting for a user
// record.
type claudeBuilder struct {
	cwd       string
	sessionID string
	model     string
	entries   []claudeEntry
	lastUUID  string
	lastAt    time.Time
	calls     []pendingCall
}

type pendingCall struct {
	piID     string
	claudeID string
	result   *ToolResult
}

// addUser emits one user record.
func (b *claudeBuilder) addUser(blocks []claudeBlock, when time.Time) {
	b.emit("user", claudeMessage{Role: "user", Content: blocks}, when, true)
}

// addAssistant emits one assistant record and remembers its tool calls so the
// following tool results can answer them.
func (b *claudeBuilder) addAssistant(message Message) error {
	var content []claudeBlock
	var calls []pendingCall
	for _, block := range message.Blocks {
		switch block.Type {
		case BlockText:
			if block.Text != "" {
				content = append(content, claudeBlock{Type: "text", Text: block.Text})
			}
		case BlockToolCall:
			if block.Call == nil || block.Call.ID == "" || block.Call.Name == "" {
				return fmt.Errorf("%w: tool call without id or name", ErrUnsupportedShape)
			}
			input := block.Call.Arguments
			if len(strings.TrimSpace(string(input))) == 0 {
				input = json.RawMessage("{}")
			}
			callID := "toolu_" + strings.ReplaceAll(newUUID(), "-", "")
			content = append(content, claudeBlock{Type: "tool_use", ID: callID, Name: block.Call.Name, Input: input})
			calls = append(calls, pendingCall{piID: block.Call.ID, claudeID: callID})
		}
	}
	if len(content) == 0 {
		return nil
	}
	stop := "end_turn"
	if len(calls) > 0 {
		stop = "tool_use"
	}
	b.emit("assistant", claudeMessage{
		Role: "assistant", Type: "message", Model: b.model, ID: "msg_" + strings.ReplaceAll(newUUID(), "-", ""),
		Content: content, StopReason: stop, Usage: &claudeUsage{},
	}, message.Timestamp, false)
	b.calls = calls
	return nil
}

// addToolResult collects one tool answer. Results stay buffered until the next
// conversation message, so all answers to one assistant call form one user
// record, which is what the target model expects.
func (b *claudeBuilder) addToolResult(message Message) error {
	if len(message.Blocks) == 0 || message.Blocks[0].Result == nil {
		return fmt.Errorf("%w: tool message without a result", ErrUnsupportedShape)
	}
	result := message.Blocks[0].Result
	index := -1
	for i, call := range b.calls {
		if call.piID == result.CallID {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("%w: tool result for unknown call %q", ErrBrokenChain, result.CallID)
	}
	b.calls[index].result = result
	return nil
}

// flushTools writes the buffered tool answers as one user record. A call with
// no answer gets a synthetic error result, because Claude rejects a history
// that leaves a tool_use unanswered.
func (b *claudeBuilder) flushTools() error {
	if len(b.calls) == 0 {
		return nil
	}
	blocks := make([]claudeBlock, 0, len(b.calls))
	for _, call := range b.calls {
		content := []claudeBlock{{Type: "text", Text: "The tool did not return before the conversation moved on."}}
		isError := true
		if call.result != nil {
			content = claudeContent(call.result.Content)
			if len(content) == 0 {
				content = []claudeBlock{{Type: "text", Text: "(no output)"}}
			}
			isError = call.result.IsError
		}
		blocks = append(blocks, claudeBlock{Type: "tool_result", ToolUseID: call.claudeID, Content: content, IsError: isError})
	}
	b.calls = nil
	b.emit("user", claudeMessage{Role: "user", Content: blocks}, time.Time{}, true)
	return nil
}

// emit appends one chained record.
func (b *claudeBuilder) emit(kind string, message claudeMessage, when time.Time, user bool) {
	if when.IsZero() {
		when = time.Now().UTC()
	}
	if !when.After(b.lastAt) {
		when = b.lastAt.Add(time.Millisecond)
	}
	b.lastAt = when
	entry := claudeEntry{
		Type: kind, Message: message, UUID: newUUID(), Timestamp: when.UTC().Format(time.RFC3339Nano),
		Cwd: b.cwd, SessionID: b.sessionID, Version: ClaudeSessionVersion, IsSidechain: false,
	}
	if b.lastUUID != "" {
		parent := b.lastUUID
		entry.ParentUUID = &parent
	}
	if user {
		entry.UserType = "external"
		entry.Entrypoint = "sdk-cli"
	}
	b.lastUUID = entry.UUID
	b.entries = append(b.entries, entry)
}

// claudeContent maps common text and image blocks to Claude blocks.
func claudeContent(blocks []Block) []claudeBlock {
	var out []claudeBlock
	for _, block := range blocks {
		switch block.Type {
		case BlockText:
			if block.Text != "" {
				out = append(out, claudeBlock{Type: "text", Text: block.Text})
			}
		case BlockImage:
			if block.Image != nil && block.Image.Data != "" && block.Image.MIME != "" {
				out = append(out, claudeBlock{Type: "image", Source: &claudeImageSource{
					Type: "base64", MediaType: block.Image.MIME, Data: block.Image.Data,
				}})
			}
		}
	}
	return out
}
