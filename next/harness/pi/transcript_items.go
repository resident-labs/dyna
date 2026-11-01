package pi

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
)

// TranscriptItem keeps the raw tool-call ID outside the wire contract.
type TranscriptItem struct {
	contract.TranscriptItem
	CallID string
}

// ReadTranscript decodes complete Pi records and masks their content.
func ReadTranscript(r io.Reader, deliveries map[contract.DeliverySeq]contract.TurnID, source, fullItem string, skip, limit int) ([]TranscriptItem, int, bool, error) {
	return readTranscript(r, deliveries, source, fullItem, skip, limit, 16<<20)
}

func readTranscript(r io.Reader, deliveries map[contract.DeliverySeq]contract.TurnID, source, fullItem string, skip, limit int, maxRecord int64) ([]TranscriptItem, int, bool, error) {
	return readTranscriptMode(r, deliveries, source, fullItem, skip, limit, maxRecord, false)
}

func readTranscriptMode(r io.Reader, deliveries map[contract.DeliverySeq]contract.TurnID, source, fullItem string, skip, limit int, maxRecord int64, recent bool) ([]TranscriptItem, int, bool, error) {
	reader := bufio.NewReaderSize(r, 64<<10)
	var items []*TranscriptItem
	seen := 0
	more := false
	calls := map[string]*TranscriptItem{}
	var latestText *TranscriptItem
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
				result := make([]TranscriptItem, len(items))
				for i, item := range items {
					result[i] = *item
				}
				return result, seen, more, nil
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
						items = append(items, &TranscriptItem{TranscriptItem: contract.TranscriptItem{ID: id, Index: seen, Turn: turn, Kind: contract.TranscriptTooLarge, At: lastAt, Bytes: size}})
					} else if seen >= skip {
						more = true
					}
					seen++
				} else if id == fullItem {
					items = append(items, &TranscriptItem{TranscriptItem: contract.TranscriptItem{ID: id, Index: seen, Turn: turn, Kind: contract.TranscriptTooLarge, At: lastAt, Bytes: size}})
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
				Details                      struct {
					Diff  string
					Calls []struct {
						Name, Args, Status, Error string
						DurationMs                *int64
					}
				}
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
				item := TranscriptItem{TranscriptItem: contract.TranscriptItem{ID: transcriptItemID(entry.ID + ":" + strconv.Itoa(n)), Turn: turn, At: entry.Timestamp}}
				switch b.Type {
				case "text":
					item.Kind, item.Text = contract.TranscriptText, harness.Mask(b.Text)
				case "thinking":
					item.Kind, item.Text = contract.TranscriptThinking, harness.Mask(b.Thinking)
				case "toolCall":
					item.Kind, item.ID, item.Tool, item.Arguments = contract.TranscriptTool, transcriptItemID(b.ID), clipToolName(harness.Mask(b.Name)), harness.Mask(string(b.Arguments))
					if b.Name == "codemode" {
						var args struct{ Code string }
						if json.Unmarshal(b.Arguments, &args) == nil {
							item.Script = harness.Mask(args.Code)
						}
					}
					if len(b.ID) <= 4096 {
						item.CallID = b.ID
					}
				default:
					continue
				}
				if fullItem == "" {
					if seen < skip {
						seen++
						continue
					}
					seen++
					item.Index = seen - 1
					if len(items) >= limit {
						more = true
						if !recent {
							continue
						}
						drop := 0
						if items[drop] == latestText && len(items) > 1 {
							drop++
						}
						delete(calls, items[drop].ID)
						items = append(items[:drop], items[drop+1:]...)
					}
				} else if item.ID != fullItem {
					continue
				}
				if item.ID != fullItem {
					item.More = len(item.Text) > 8192 || len(item.Arguments) > 8192 || len(item.Script) > 8192
					if len(item.Script) > 8192 {
						item.Script = strings.ToValidUTF8(item.Script[:8192], "")
					}
					if len(item.Text) > 8192 {
						item.Text = strings.ToValidUTF8(item.Text[:8192], "")
					}
					if len(item.Arguments) > 8192 {
						item.Arguments = strings.ToValidUTF8(item.Arguments[:8192], "")
					}
				}
				items = append(items, &item)
				if item.Kind == contract.TranscriptTool {
					calls[item.ID] = &item
				}
				if item.Kind == contract.TranscriptText {
					latestText = &item
				}
			}
		} else if m.Role == "toolResult" {
			item, ok := calls[transcriptItemID(m.ToolCallID)]
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
			item.Output, item.Failed = harness.Mask(strings.Join(text, "\n")), m.IsError
			if item.Tool == "codemode" {
				// Bound JSON bytes, including escaped control characters, for older-item pages.
				callBytes := 0
				for i, call := range m.Details.Calls {
					if i == 256 {
						item.CallsOmitted += len(m.Details.Calls) - i
						break
					}
					status := contract.TranscriptCallStatus(call.Status)
					switch status {
					case contract.TranscriptCallRunning, contract.TranscriptCallOK, contract.TranscriptCallError, contract.TranscriptCallCancelled:
					default:
						continue
					}
					tool := clipToolName(harness.Mask(call.Name))
					target := strings.TrimPrefix(activityLabel(event{ToolName: call.Name, Args: json.RawMessage(call.Args)}), call.Name)
					target = strings.TrimSpace(harness.Mask(target))
					if len(target) > 1024 {
						target = strings.ToValidUTF8(target[:1024], "")
					}
					callError := harness.Mask(call.Error)
					if len(callError) > 1024 {
						callError = strings.ToValidUTF8(callError[:1024], "")
					}
					preview := contract.TranscriptCall{Tool: tool, Target: target, Status: status, DurationMs: call.DurationMs, Error: callError}
					encoded, _ := json.Marshal(preview)
					if callBytes+len(encoded)+1 > 16<<10 {
						item.CallsOmitted += len(m.Details.Calls) - i
						break
					}
					callBytes += len(encoded) + 1
					item.Calls = append(item.Calls, preview)
				}
			}
			// Pi records applied edit details separately from the result text.
			if (item.Tool == "edit" || item.Tool == "write") && !m.IsError {
				item.Diff = harness.Mask(m.Details.Diff)
			}
			item.Ended = &entry.Timestamp
			if item.ID != fullItem && len(item.Diff) > 8192 {
				item.Diff = strings.ToValidUTF8(item.Diff[:8192], "")
				item.More = true
			}
			if item.ID != fullItem && len(item.Output) > 1024 {
				item.Output = strings.ToValidUTF8(item.Output[:1024], "")
				item.More = true
			}
		}
	}
}

// TranscriptFindCalls marks candidate IDs found in an acknowledged range.
func TranscriptFindCalls(r io.Reader, ids map[string]bool) error {
	reader := bufio.NewReaderSize(r, 64<<10)
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
				return nil
			}
			if err != nil {
				return err
			}
			break
		}
		if oversized {
			continue
		}
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					ID   string `json:"id"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type != "message" || entry.Message.Role != "assistant" {
			continue
		}
		for _, block := range entry.Message.Content {
			if _, wanted := ids[block.ID]; block.Type == "toolCall" && wanted {
				ids[block.ID] = true
			}
		}
	}
}

// TranscriptStep keeps the Work tab compatible until the thread replaces it.
func TranscriptStep(item contract.TranscriptItem) contract.TranscriptStep {
	label := harness.Mask(activityLabel(event{ToolName: item.Tool, Args: json.RawMessage(item.Arguments)}))
	if runes := []rune(label); len(runes) > 80 {
		label = string(runes[:79]) + "…"
	}
	return contract.TranscriptStep{Turn: item.Turn, Tool: item.Tool, Label: label, Started: item.At, Ended: item.Ended, Failed: item.Failed, Preview: item.Output}
}

func transcriptItemID(id string) string {
	if len(id) <= 256 {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("sha256:%x", sum)
}

func clipToolName(name string) string {
	if len(name) <= 256 {
		return name
	}
	return strings.ToValidUTF8(name[:256], "")
}
