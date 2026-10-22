package workeroutput

import (
	"fmt"
	"strings"
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
			// A failed result can carry its diagnostic only in error.
			detail := strings.TrimSpace(record.Result.Response + " " + record.Result.Error)
			failure = fmt.Errorf("Antigravity protocol result %s: %s", record.Result.Status, detail)
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
	}
	return failure
}
