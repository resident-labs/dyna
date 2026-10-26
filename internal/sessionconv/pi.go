package sessionconv

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// piHeader is the first line of a Pi session file.
type piHeader struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
}

// piEntry is one Pi session entry. Only the fields the converter uses are
// declared, so an unknown entry type decodes without error and is skipped.
type piEntry struct {
	Type             string     `json:"type"`
	ID               string     `json:"id"`
	ParentID         *string    `json:"parentId"`
	Timestamp        string     `json:"timestamp"`
	Message          *piMessage `json:"message"`
	Summary          string     `json:"summary"`
	FirstKeptEntryID string     `json:"firstKeptEntryId"`
	// context_edit fields.
	TargetID    string          `json:"targetId"`
	Replacement json.RawMessage `json:"replacement"`
	// custom_message fields.
	CustomType string          `json:"customType"`
	Content    json.RawMessage `json:"content"`
	Display    bool            `json:"display"`
	// branch_summary field.
	FromID string `json:"fromId"`
}

// piMessage is the AgentMessage payload of a Pi message entry.
type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	IsError    bool            `json:"isError"`
	// bashExecution fields.
	Command            string `json:"command"`
	Output             string `json:"output"`
	ExitCode           *int   `json:"exitCode"`
	Cancelled          bool   `json:"cancelled"`
	Truncated          bool   `json:"truncated"`
	FullOutputPath     string `json:"fullOutputPath"`
	ExcludeFromContext bool   `json:"excludeFromContext"`
}

// piBlock is one Pi content block. Thinking and provider fields are decoded
// only so the whole block is understood, then dropped.
type piBlock struct {
	Type              string          `json:"type"`
	Text              string          `json:"text"`
	Data              string          `json:"data"`
	MIMEType          string          `json:"mimeType"`
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Arguments         json.RawMessage `json:"arguments"`
	Thinking          string          `json:"thinking"`
	ThinkingSignature string          `json:"thinkingSignature"`
}

// piReplacement is the persisted shape of a non-null context edit.
type piReplacement struct {
	Content json.RawMessage `json:"content"`
}

// ReadPi reads the active conversation branch of a Pi session file.
func ReadPi(path string) (Conversation, error) {
	file, err := os.Open(path)
	if err != nil {
		return Conversation{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	var header piHeader
	entries := make(map[string]*piEntry)
	var order []*piEntry
	var bytesRead, lines int
	for scanner.Scan() {
		lines++
		bytesRead += len(scanner.Bytes()) + 1
		if lines > maxSessionLines || bytesRead > maxSessionBytes {
			return Conversation{}, ErrTooLarge
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if header.Type == "" {
			if err := json.Unmarshal(line, &header); err != nil {
				return Conversation{}, fmt.Errorf("pi session header: %w", err)
			}
			if header.Type != "session" || header.Version != PiSessionFormat || header.ID == "" {
				return Conversation{}, fmt.Errorf("%w: pi session header version %d", ErrUnsupportedVersion, header.Version)
			}
			continue
		}
		var entry piEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			// Pi skips malformed lines; a broken line cannot define the chain.
			continue
		}
		if entry.ID == "" {
			continue
		}
		// Every entry with an id is a chain node, including model changes,
		// labels and usage records that contribute no message. The leaf may be
		// one of them, and a message can parent a later entry.
		entries[entry.ID] = &entry
		order = append(order, &entry)
	}
	if err := scanner.Err(); err != nil {
		return Conversation{}, err
	}
	if header.Type == "" {
		return Conversation{}, errors.New("pi session file is empty")
	}
	leaf := piLeaf(order)
	if leaf == nil {
		return Conversation{}, nil
	}
	pathEntries, err := piChain(entries, leaf)
	if err != nil {
		return Conversation{}, err
	}
	return piConversation(pathEntries)
}

// piLeaf returns the newest entry with an id: the session's active leaf. It
// can be any entry type, because Pi appends model changes, edits and usage
// records after messages and the leaf is the last written entry.
func piLeaf(order []*piEntry) *piEntry {
	if len(order) == 0 {
		return nil
	}
	return order[len(order)-1]
}

// piChain walks parentId links from leaf to root and returns them oldest first.
func piChain(entries map[string]*piEntry, leaf *piEntry) ([]*piEntry, error) {
	var reverse []*piEntry
	seen := make(map[string]bool)
	for current := leaf; current != nil; {
		if seen[current.ID] {
			return nil, fmt.Errorf("%w: cycle at %s", ErrBrokenChain, current.ID)
		}
		seen[current.ID] = true
		reverse = append(reverse, current)
		if current.ParentID == nil || *current.ParentID == "" {
			break
		}
		parent, ok := entries[*current.ParentID]
		if !ok {
			return nil, fmt.Errorf("%w: missing parent %s", ErrBrokenChain, *current.ParentID)
		}
		current = parent
	}
	for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
		reverse[i], reverse[j] = reverse[j], reverse[i]
	}
	return reverse, nil
}

// piContextEntries mirrors Pi's buildContextEntries: the latest compaction on
// the path, then the kept entries from firstKeptEntryId, then everything after
// the compaction. Only these entries participate in model context, so context
// edits on an abandoned branch can never reach the conversion.
func piContextEntries(path []*piEntry) []*piEntry {
	compactionIndex := -1
	for i, entry := range path {
		if entry.Type == "compaction" {
			compactionIndex = i
		}
	}
	if compactionIndex < 0 {
		return path
	}
	compaction := path[compactionIndex]
	out := []*piEntry{compaction}
	firstKept := false
	for _, entry := range path[:compactionIndex] {
		if entry.ID == compaction.FirstKeptEntryID {
			firstKept = true
		}
		if !firstKept {
			continue
		}
		if entry.Type == "message" && entry.Message != nil && entry.Message.Role == "system" {
			continue
		}
		out = append(out, entry)
	}
	out = append(out, path[compactionIndex+1:]...)
	return out
}

// piConversation converts the active, compaction-aware entries. Edits are
// collected from those entries only, so a branch that is not on the active
// path cannot change the conversation.
func piConversation(path []*piEntry) (Conversation, error) {
	context := piContextEntries(path)
	edits := make(map[string]json.RawMessage)
	for _, entry := range context {
		if entry.Type == "context_edit" && entry.TargetID != "" && len(entry.Replacement) > 0 {
			edits[entry.TargetID] = entry.Replacement
		}
	}
	conversation := Conversation{}
	for i, entry := range context {
		switch entry.Type {
		case "compaction":
			// The leading compaction is the newest one. A retained range can
			// contain an older compaction; Pi suppresses it from model context,
			// so it must not overwrite the newest summary either.
			if i == 0 {
				conversation.Summary = entry.Summary
			}
		case "message", "custom_message", "branch_summary":
			replacement, edited := edits[entry.ID]
			if edited && isNullJSON(replacement) {
				continue
			}
			message, ok, err := piEntryToCommon(entry, replacement, edited)
			if err != nil {
				return Conversation{}, err
			}
			if ok {
				conversation.Messages = append(conversation.Messages, message)
			}
		}
	}
	return conversation, nil
}

func isNullJSON(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// piEntryToCommon converts one context-producing entry. replacement is the
// latest edit on the active path, when one targets this entry.
func piEntryToCommon(entry *piEntry, replacement json.RawMessage, edited bool) (Message, bool, error) {
	when, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
	switch entry.Type {
	case "branch_summary":
		// Pi converts a branch summary to a user message with this wrapper and
		// does not let an edit replace it.
		if entry.Summary == "" {
			return Message{}, false, nil
		}
		text := "The following is a summary of a branch that this conversation came back from:\n\n<summary>\n" +
			entry.Summary + "\n</summary>"
		return Message{Role: RoleUser, Blocks: []Block{textBlock(text)}, Timestamp: when}, true, nil
	case "custom_message":
		content := entry.Content
		if edited {
			editedContent, err := piReplacementContent(replacement)
			if err != nil {
				return Message{}, false, err
			}
			content = editedContent
		}
		blocks, err := piBlocks(content)
		if err != nil {
			return Message{}, false, err
		}
		out := make([]Block, 0, len(blocks)+1)
		if label := strings.TrimSpace(entry.CustomType); label != "" {
			out = append(out, textBlock("[Custom message: "+label+"]"))
		}
		out = append(out, piContentBlocks(blocks)...)
		if len(out) == 0 {
			return Message{}, false, nil
		}
		return Message{Role: RoleUser, Blocks: out, Timestamp: when}, true, nil
	}
	if entry.Message == nil {
		return Message{}, false, nil
	}
	raw := entry.Message
	content := raw.Content
	if edited {
		editedContent, err := piReplacementContent(replacement)
		if err != nil {
			return Message{}, false, err
		}
		content = editedContent
	}
	switch raw.Role {
	case "user":
		blocks, err := piBlocks(content)
		if err != nil {
			return Message{}, false, err
		}
		out := piContentBlocks(blocks)
		return Message{Role: RoleUser, Blocks: out, Timestamp: when}, len(out) > 0, nil
	case "assistant":
		blocks, err := piBlocks(content)
		if err != nil {
			return Message{}, false, err
		}
		var out []Block
		for _, block := range blocks {
			switch block.Type {
			case "text":
				if block.Text != "" {
					out = append(out, textBlock(block.Text))
				}
			case "toolCall":
				arguments := block.Arguments
				if len(bytes.TrimSpace(arguments)) == 0 {
					arguments = json.RawMessage("{}")
				}
				out = append(out, Block{Type: BlockToolCall, Call: &ToolCall{ID: block.ID, Name: block.Name, Arguments: arguments}})
			case "thinking":
				// Provider signatures cannot cross harnesses.
			}
		}
		return Message{Role: RoleAssistant, Blocks: out, Timestamp: when}, len(out) > 0, nil
	case "toolResult":
		blocks, err := piBlocks(content)
		if err != nil {
			return Message{}, false, err
		}
		out := piContentBlocks(blocks)
		if len(out) == 0 {
			out = append(out, textBlock("(no output)"))
		}
		return Message{Role: RoleTool, Blocks: []Block{{Type: BlockToolResult, Result: &ToolResult{CallID: raw.ToolCallID, Content: out, IsError: raw.IsError}}}, Timestamp: when}, true, nil
	case "bashExecution":
		// Pi does not let an edit replace a direct shell command either.
		if raw.ExcludeFromContext {
			return Message{}, false, nil
		}
		return Message{Role: RoleUser, Blocks: []Block{textBlock(bashExecutionText(raw))}, Timestamp: when}, true, nil
	case "system":
		// The target harness builds its own system prompt and tool set.
		return Message{}, false, nil
	}
	return Message{}, false, fmt.Errorf("%w: pi message role %q", ErrUnsupportedShape, raw.Role)
}

// piReplacementContent decodes the persisted edit shape {"content": ...}.
func piReplacementContent(raw json.RawMessage) (json.RawMessage, error) {
	var replacement piReplacement
	if err := json.Unmarshal(raw, &replacement); err != nil || len(bytes.TrimSpace(replacement.Content)) == 0 {
		return nil, fmt.Errorf("%w: pi context edit replacement", ErrUnsupportedShape)
	}
	return replacement.Content, nil
}

// piContentBlocks maps Pi text and image blocks, dropping everything else.
func piContentBlocks(blocks []piBlock) []Block {
	var out []Block
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				out = append(out, textBlock(block.Text))
			}
		case "image":
			if block.Data != "" && block.MIMEType != "" {
				out = append(out, Block{Type: BlockImage, Image: &Image{MIME: block.MIMEType, Data: block.Data}})
			}
		}
	}
	return out
}

// bashExecutionText mirrors Pi's own rendering of a direct shell command.
func bashExecutionText(message *piMessage) string {
	text := "Ran `" + message.Command + "`\n"
	if message.Output != "" {
		text += "```\n" + message.Output + "\n```"
	} else {
		text += "(no output)"
	}
	if message.Cancelled {
		text += "\n\n(command cancelled)"
	} else if message.ExitCode != nil && *message.ExitCode != 0 {
		text += fmt.Sprintf("\n\nCommand exited with code %d", *message.ExitCode)
	}
	if message.Truncated && message.FullOutputPath != "" {
		text += "\n\n[Output truncated. Full output: " + message.FullOutputPath + "]"
	}
	return text
}

// piBlocks decodes Pi message content, which is either a plain string or an
// array of content blocks.
func piBlocks(raw json.RawMessage) ([]piBlock, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, fmt.Errorf("%w: pi text content", ErrUnsupportedShape)
		}
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		return []piBlock{{Type: "text", Text: text}}, nil
	}
	var blocks []piBlock
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return nil, fmt.Errorf("%w: pi content blocks", ErrUnsupportedShape)
	}
	return blocks, nil
}
