package pi

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// Recover reads only messages from the first delivery of the interrupted turn.
// A final assistant message, or a tool batch that ended the turn, proves that the turn finished before reporting.
func (h *Harness) Recover(id contract.TurnID, inputs []contract.TurnInput) (contract.TurnEnded, bool, error) {
	result := contract.TurnEnded{Turn: id}
	if len(inputs) == 0 {
		return result, false, errors.New("recovery needs at least one input")
	}
	file, err := os.Open(h.session)
	if errors.Is(err, os.ErrNotExist) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	defer file.Close()
	first := strconv.FormatInt(int64(inputs[0].Delivery), 10)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	scanner.Split(completeLines)
	found := false
	settled := false
	var batch endBatch
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Role          string        `json:"role"`
				DeliveryID    string        `json:"deliveryId"`
				Model         string        `json:"model"`
				ResponseModel string        `json:"responseModel"`
				StopReason    string        `json:"stopReason"`
				IsError       bool          `json:"isError"`
				Details       resultDetails `json:"details"`
				Content       contentBlocks `json:"content"`
				Usage         struct {
					Input      int64 `json:"input"`
					Output     int64 `json:"output"`
					CacheRead  int64 `json:"cacheRead"`
					CacheWrite int64 `json:"cacheWrite"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // Match Pi when an interrupted write left a malformed line.
		}
		if entry.Type != "message" {
			continue
		}
		m := entry.Message
		if m.Role == "user" && m.DeliveryID == first {
			found = true
		}
		if !found {
			continue
		}
		if m.Role == "user" || m.Role == "toolResult" {
			settled = false
		}
		if m.Role == "user" {
			batch = endBatch{}
		}
		if m.Role == "toolResult" {
			batch.toolResult(m.Details, m.IsError)
		}
		if m.Role != "assistant" {
			continue
		}
		batch.assistant(m.Content)
		result.Usage.InputTokens += m.Usage.Input
		result.Usage.OutputTokens += m.Usage.Output
		result.Usage.CacheReadTokens += m.Usage.CacheRead
		result.Usage.CacheWriteTokens += m.Usage.CacheWrite
		result.Usage.Model = m.Model
		if m.ResponseModel != "" {
			result.Usage.Model = m.ResponseModel
		}
		var text []string
		for _, block := range m.Content {
			if block.Type == "text" {
				text = append(text, block.Text)
			}
		}
		result.Final = strings.Join(text, "\n")
		settled = m.StopReason == "stop" || m.StopReason == "end_turn"
		for _, block := range m.Content {
			if block.Type == "toolCall" {
				settled = false
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return result, false, err
	}
	if batch.apply(&result) {
		settled = true
	}
	if settled {
		result.Outcome = contract.TurnOutcome("completed")
	}
	return result, found && settled, nil
}
