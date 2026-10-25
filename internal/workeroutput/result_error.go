package workeroutput

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"dyna-agent/internal/providerfailure"
)

// AntigravityResultError follows the explicit result status. Partial text
// from an earlier result cannot hide a later terminal ERROR or FAILED.
func AntigravityResultError(stdout string) error {
	var failure error
	for _, line := range strings.Split(stdout, "\n") {
		record, ok := parseRecord[antigravityRecord]([]byte(line))
		if !ok || record.Event != "result" {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(record.Result.Status)) {
		case "ERROR", "FAILED":
			// A failed result can carry its diagnostic only in error. The reason
			// comes from that structured field, never from free response text.
			detail := strings.TrimSpace(record.Result.Response + " " + record.Result.Error)
			failure = providerfailure.TagDetail(fmt.Errorf("Antigravity protocol result %s: %s", record.Result.Status, detail), record.Result.Error)
		case "SUCCESS", "COMPLETED":
			failure = nil
		}
	}
	return failure
}

// OpenCodeError preserves a native top-level error event, including its
// structured provider diagnostic. Text parts and tool errors are ignored.
func OpenCodeError(stdout string) error {
	var failure error
	for _, line := range strings.Split(stdout, "\n") {
		record, ok := parseRecord[openCodeRecord]([]byte(line))
		if !ok || record.Type != "error" {
			continue
		}
		failure = fmt.Errorf("OpenCode protocol error: %s", record.Error)
		if reason, ok := openCodeFailureReason(record.Error); ok {
			failure = providerfailure.Tag(reason, failure)
		}
	}
	return failure
}

// openCodeFailureReason reads the provider reason from the structured error
// event: its status, its name, the status and type of its data, and only then
// the text of the diagnostic.
func openCodeFailureReason(raw json.RawMessage) (providerfailure.Reason, bool) {
	var event struct {
		Name   string          `json:"name"`
		Status json.RawMessage `json:"status"`
		Data   struct {
			Message string          `json:"message"`
			Status  json.RawMessage `json:"status"`
			Type    string          `json:"type"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &event) == nil {
		for _, status := range []json.RawMessage{event.Status, event.Data.Status} {
			code, err := strconv.Atoi(strings.TrimSpace(scalarText(status)))
			if err != nil {
				continue
			}
			// A 402 asks for payment, so it names no provider failure whatever
			// the type of the record says.
			if code == 402 {
				return "", false
			}
			if reason, ok := providerfailure.StatusReason(code); ok {
				return reason, true
			}
		}
		for _, field := range []string{event.Name, event.Data.Type} {
			if reason, ok := providerfailure.ClassifyField(field); ok {
				return reason, true
			}
		}
	}
	return providerfailure.Classify(string(raw))
}
