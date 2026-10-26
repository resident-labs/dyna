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
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	ParentID         *string         `json:"parentId"`
	Timestamp        string          `json:"timestamp"`
	Message          *piMessage      `json:"message"`
	Summary          string          `json:"summary"`
	FirstKeptEntryID string          `json:"firstKeptEntryId"`
	TargetID         string          `json:"targetId"`
	Replacement      json.RawMessage `json:"replacement"`
}

// piMessage is the AgentMessage payload of a Pi message entry.
type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	IsError    bool            `json:"isError"`
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
	var edits = make(map[string]json.RawMessage)
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
		if entry.Type == "context_edit" && entry.TargetID != "" && len(entry.Replacement) > 0 {
			edits[entry.TargetID] = entry.Replacement
		}
	}
	if err := scanner.Err(); err != nil {
		return Conversation{}, err
	}
	if header.Type == "" {
		return Conversation{}, errors.New("pi session file is empty")
	}

	leaf := piLeaf(order)
	if leaf == nil {
		return Conversation{Summary: "", Messages: nil}, nil
	}
	pathEntries, err := piChain(entries, leaf)
	if err != nil {
		return Conversation{}, err
	}
	return piConversation(pathEntries, edits)
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

// piConversation converts the oldest-first chain, honoring the latest
// compaction and context edits on that chain.
func piConversation(path []*piEntry, edits map[string]json.RawMessage) (Conversation, error) {
	compactionIndex := -1
	for i, entry := range path {
		if entry.Type == "compaction" {
			compactionIndex = i
		}
	}
	first := 0
	summary := ""
	if compactionIndex >= 0 {
		compaction := path[compactionIndex]
		summary = compaction.Summary
		first = compactionIndex // no earlier message survives a retain-none compaction
		for i, entry := range path[:compactionIndex] {
			if entry.ID == compaction.FirstKeptEntryID {
				first = i
				break
			}
		}
	}
	conversation := Conversation{Summary: summary}
	for i := first; i < len(path); i++ {
		entry := path[i]
		if entry.Type != "message" || entry.Message == nil || entry.Message.Role == "system" {
			continue
		}
		content := entry.Message.Content
		if replacement, ok := edits[entry.ID]; ok {
			if bytes.Equal(bytes.TrimSpace(replacement), []byte("null")) {
				continue // the edit hides this message from model context
			}
			content = replacement
		}
		message, ok, err := piMessageToCommon(entry.Message, content, entry.Timestamp)
		if err != nil {
			return Conversation{}, err
		}
		if ok {
			conversation.Messages = append(conversation.Messages, message)
		}
	}
	return conversation, nil
}

func piMessageToCommon(raw *piMessage, content json.RawMessage, stamp string) (Message, bool, error) {
	blocks, err := piBlocks(content)
	if err != nil {
		return Message{}, false, err
	}
	when, _ := time.Parse(time.RFC3339Nano, stamp)
	switch raw.Role {
	case "user":
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
		return Message{Role: RoleUser, Blocks: out, Timestamp: when}, len(out) > 0, nil
	case "assistant":
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
		if len(out) == 0 {
			out = append(out, textBlock("(no output)"))
		}
		return Message{Role: RoleTool, Blocks: []Block{{Type: BlockToolResult, Result: &ToolResult{CallID: raw.ToolCallID, Content: out, IsError: raw.IsError}}}, Timestamp: when}, true, nil
	}
	return Message{}, false, nil
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
