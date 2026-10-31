package pi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// ReadSteps reads complete Pi JSONL records. An oversized record is skipped rather than
// allowing one tool output to consume unbounded memory.
func ReadSteps(r io.Reader, deliveries map[contract.DeliverySeq]contract.TurnID, turn contract.TurnID, limit int) ([]contract.TranscriptStep, bool, error) {
	const maxLine = 16 << 20
	reader := bufio.NewReaderSize(r, 64<<10)
	steps := make([]contract.TranscriptStep, 0, limit)
	truncated := false
	var current contract.TurnID
	var ids []string
	for {
		var line []byte
		oversized := false
		for {
			part, err := reader.ReadSlice('\n')
			if !oversized && len(line)+len(part) <= maxLine {
				line = append(line, part...)
			} else {
				oversized = true
				line = nil
			}
			if err == bufio.ErrBufferFull {
				continue
			}
			if err != nil && err != io.EOF {
				return nil, truncated, err
			}
			if err == io.EOF {
				return steps, truncated, nil
			} // Ignore an incomplete trailing record.
			break
		}
		if oversized {
			truncated = true
			continue
		}
		var entry struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Message   struct {
				Role       string          `json:"role"`
				DeliveryID string          `json:"deliveryId"`
				ToolCallID string          `json:"toolCallId"`
				IsError    bool            `json:"isError"`
				Content    json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type != "message" {
			continue
		}
		m := entry.Message
		if m.Role == "user" {
			id, err := strconv.ParseInt(m.DeliveryID, 10, 64)
			current = ""
			if err == nil {
				current = deliveries[contract.DeliverySeq(id)]
			}
			continue
		}
		if m.Role == "assistant" && current != "" && (turn == "" || current == turn) {
			var blocks []struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(m.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type != "toolCall" {
					continue
				}
				label := activityLabel(event{ToolName: b.Name, Args: b.Arguments})
				if runes := []rune(label); len(runes) > 80 {
					label = string(runes[:79]) + "…"
				}
				if len(steps) == limit {
					steps = steps[1:]
					truncated = true
				}
				steps = append(steps, contract.TranscriptStep{Turn: current, Tool: b.Name, Label: label, Started: entry.Timestamp})
				// Keep IDs in a parallel bounded list by recording them below.
				ids = appendID(ids, b.ID, limit)
			}
		} else if m.Role == "toolResult" {
			for i := len(ids) - 1; i >= 0; i-- {
				if ids[i] != m.ToolCallID {
					continue
				}
				steps[i].Ended = &entry.Timestamp
				steps[i].Failed = m.IsError
				var blocks []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if json.Unmarshal(m.Content, &blocks) == nil {
					var out strings.Builder
					for _, b := range blocks {
						if b.Type == "text" {
							if out.Len() > 0 {
								out.WriteByte('\n')
							}
							out.WriteString(b.Text)
						}
					}
					preview := out.String()
					if len(preview) > 1024 {
						preview = string(bytes.ToValidUTF8([]byte(preview[:1024]), nil))
					}
					steps[i].Preview = preview
				}
				break
			}
		}
	}
}

func appendID(ids []string, id string, limit int) []string {
	if len(ids) == limit {
		ids = ids[1:]
	}
	return append(ids, id)
}
