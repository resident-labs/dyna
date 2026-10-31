package pi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
)

// ReadTranscript decodes complete Pi records and masks their content.
func ReadTranscript(r io.Reader, deliveries map[contract.DeliverySeq]contract.TurnID, source, fullItem string, skip, limit int) ([]contract.TranscriptItem, int, bool, error) {
	return readTranscript(r, deliveries, source, fullItem, skip, limit, 16<<20)
}

func readTranscript(r io.Reader, deliveries map[contract.DeliverySeq]contract.TurnID, source, fullItem string, skip, limit int, maxRecord int64) ([]contract.TranscriptItem, int, bool, error) {
	reader := bufio.NewReaderSize(r, 64<<10)
	var items []contract.TranscriptItem
	seen := 0
	more := false
	calls := map[string]int{}
	var turn contract.TurnID
	var lastAt time.Time
	for lineNo := 1; ; lineNo++ {
		var line []byte
		var size int64
		oversized := false
		for {
			part, err := reader.ReadSlice('\n')
			size += int64(len(part))
			if !oversized && size <= maxRecord {
				line = append(line, part...)
			} else {
				oversized = true
				line = nil
			}
			if err == bufio.ErrBufferFull {
				continue
			}
			if err == io.EOF {
				return items, seen, more, nil
			}
			if err != nil {
				return nil, seen, more, err
			}
			break
		}
		if oversized {
			if turn != "" {
				id := fmt.Sprintf("large:%s:%d", source, lineNo)
				if fullItem == "" {
					if seen >= skip && len(items) < limit {
						items = append(items, contract.TranscriptItem{ID: id, Turn: turn, Kind: contract.TranscriptTooLarge, At: lastAt, Bytes: size})
					} else if seen >= skip {
						more = true
					}
					seen++
				} else if id == fullItem {
					items = append(items, contract.TranscriptItem{ID: id, Turn: turn, Kind: contract.TranscriptTooLarge, At: lastAt, Bytes: size})
				}
			}
			continue
		}
		var entry struct {
			Type, ID  string
			Timestamp time.Time
			Message   struct {
				Role, DeliveryID, ToolCallID string
				IsError                      bool
				Content                      json.RawMessage
			}
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type != "message" {
			continue
		}
		lastAt = entry.Timestamp
		m := entry.Message
		if m.Role == "user" {
			id, _ := strconv.ParseInt(m.DeliveryID, 10, 64)
			turn = deliveries[contract.DeliverySeq(id)]
			continue
		}
		if m.Role == "assistant" && turn != "" {
			var blocks []struct {
				Type, ID, Name, Text, Thinking string
				Arguments                      json.RawMessage
			}
			if json.Unmarshal(m.Content, &blocks) != nil {
				continue
			}
			for n, b := range blocks {
				item := contract.TranscriptItem{ID: entry.ID + ":" + strconv.Itoa(n), Turn: turn, At: entry.Timestamp}
				switch b.Type {
				case "text":
					item.Kind, item.Text = contract.TranscriptText, harness.Mask(b.Text)
				case "thinking":
					item.Kind, item.Text = contract.TranscriptThinking, harness.Mask(b.Thinking)
				case "toolCall":
					item.Kind, item.ID, item.Tool, item.Arguments = contract.TranscriptTool, b.ID, harness.Mask(b.Name), harness.Mask(string(b.Arguments))
				default:
					continue
				}
				if fullItem == "" {
					if seen < skip {
						seen++
						continue
					}
					seen++
					if len(items) >= limit {
						more = true
						continue
					}
				} else if item.ID != fullItem {
					continue
				}
				if item.Kind == contract.TranscriptTool {
					calls[b.ID] = len(items)
				}
				if item.ID != fullItem {
					item.More = len(item.Text) > 8192 || len(item.Arguments) > 8192
					if len(item.Text) > 8192 {
						item.Text = strings.ToValidUTF8(item.Text[:8192], "")
					}
					if len(item.Arguments) > 8192 {
						item.Arguments = strings.ToValidUTF8(item.Arguments[:8192], "")
					}
				}
				items = append(items, item)
			}
		} else if m.Role == "toolResult" {
			i, ok := calls[m.ToolCallID]
			if !ok {
				continue
			}
			var blocks []struct{ Type, Text string }
			if json.Unmarshal(m.Content, &blocks) != nil {
				continue
			}
			var text []string
			for _, b := range blocks {
				if b.Type == "text" {
					text = append(text, b.Text)
				}
			}
			items[i].Output, items[i].Failed = harness.Mask(strings.Join(text, "\n")), m.IsError
			items[i].Ended = &entry.Timestamp
			if items[i].ID != fullItem && len(items[i].Output) > 1024 {
				items[i].Output = strings.ToValidUTF8(items[i].Output[:1024], "")
				items[i].More = true
			}
		}
	}
}

// TranscriptHasPost checks whether a bash result exactly identifies a post.
func TranscriptHasPost(r *io.SectionReader, id contract.EventID) (bool, error) {
	type message struct {
		Type    string `json:"type"`
		Message struct {
			Role       string `json:"role"`
			ToolCallID string `json:"toolCallId"`
			Content    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content"`
		} `json:"message"`
	}
	scan := func(visit func(message) bool) (bool, error) {
		reader := bufio.NewReaderSize(io.NewSectionReader(r, 0, r.Size()), 64<<10)
		for {
			var line []byte
			oversized := false
			for {
				part, err := reader.ReadSlice('\n')
				if !oversized && len(line)+len(part) <= 16<<20 {
					line = append(line, part...)
				} else {
					oversized = true
					line = nil
				}
				if err == bufio.ErrBufferFull {
					continue
				}
				if err == io.EOF {
					return false, nil
				}
				if err != nil {
					return false, err
				}
				break
			}
			if oversized {
				continue
			}
			var entry message
			if json.Unmarshal(line, &entry) == nil && entry.Type == "message" && visit(entry) {
				return true, nil
			}
		}
	}
	var scanErr error
	found, err := scan(func(entry message) bool {
		if entry.Message.Role != "toolResult" {
			return false
		}
		var parts []string
		for _, b := range entry.Message.Content {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		if strings.TrimSpace(strings.Join(parts, "\n")) != string(id) {
			return false
		}
		call := entry.Message.ToolCallID
		matched, e := scan(func(candidate message) bool {
			if candidate.Message.Role != "assistant" {
				return false
			}
			for _, b := range candidate.Message.Content {
				if b.Type == "toolCall" && b.ID == call && b.Name == "bash" {
					return true
				}
			}
			return false
		})
		scanErr = e
		return matched || e != nil
	})
	if err != nil {
		return false, err
	}
	if scanErr != nil {
		return false, scanErr
	}
	return found, nil
}

// TranscriptStep keeps the Work tab compatible until the thread replaces it.
func TranscriptStep(item contract.TranscriptItem) contract.TranscriptStep {
	label := harness.Mask(activityLabel(event{ToolName: item.Tool, Args: json.RawMessage(item.Arguments)}))
	if runes := []rune(label); len(runes) > 80 {
		label = string(runes[:79]) + "…"
	}
	return contract.TranscriptStep{Turn: item.Turn, Tool: item.Tool, Label: label, Started: item.At, Ended: item.Ended, Failed: item.Failed, Preview: item.Output}
}
