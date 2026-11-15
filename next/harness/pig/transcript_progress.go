package pig

import (
	"bufio"
	"encoding/json"
	"io"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// TranscriptProgress counts complete records, including tool calls before a turn ends
// and nested calls reported by codemode.
// Usage is cumulative across assistant messages, as in the worker's final usage.
type TranscriptProgress struct {
	ToolCalls    int            `json:"toolCalls"`
	Usage        contract.Usage `json:"usage"`
	LastActivity time.Time      `json:"lastActivity,omitzero"`
}

func readProgress(r io.Reader) (TranscriptProgress, error) {
	var progress TranscriptProgress
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			return progress, nil
		}
		if err != nil {
			return progress, err
		}
		var record struct {
			Type      string
			Timestamp time.Time
			Message   struct {
				Role        string
				NestedCalls struct {
					Calls []struct{ ID string }
				}
				Usage   struct{ Input, Output, CacheRead, CacheWrite int64 }
				Content []struct{ Type string }
			}
		}
		if json.Unmarshal(line, &record) != nil || record.Type != "message" {
			continue
		}
		if record.Timestamp.After(progress.LastActivity) {
			progress.LastActivity = record.Timestamp
		}
		if record.Message.Role == "toolResult" {
			progress.ToolCalls += len(record.Message.NestedCalls.Calls)
		}
		if record.Message.Role != "assistant" {
			continue
		}
		for _, part := range record.Message.Content {
			if part.Type == "toolCall" {
				progress.ToolCalls++
			}
		}
		progress.Usage.InputTokens += record.Message.Usage.Input
		progress.Usage.OutputTokens += record.Message.Usage.Output
		progress.Usage.CacheReadTokens += record.Message.Usage.CacheRead
		progress.Usage.CacheWriteTokens += record.Message.Usage.CacheWrite
	}
}

func (r TranscriptReader) Progress(path string) (TranscriptProgress, error) {
	file, section, err := r.open(path, 0, nil)
	if err != nil || file == nil {
		return TranscriptProgress{}, err
	}
	defer file.Close()
	return readProgress(section)
}
