package workeroutput

import (
	"fmt"
	"strings"
)

// CodexTurnError reads protocol status, never assistant prose. A successful
// terminal clears an earlier retryable error; a final error remains a failure
// even if the CLI exits zero and left partial text in its output file.
func CodexTurnError(stdout string) error {
	_, failure := codexTurnStatus(stdout)
	return failure
}

// CodexTurnCompleted reports an explicit successful terminal for the latest turn.
// Empty assistant messages and completed tools do not establish completion.
func CodexTurnCompleted(stdout string) bool {
	completed, failure := codexTurnStatus(stdout)
	return completed && failure == nil
}

func codexTurnStatus(stdout string) (bool, error) {
	var completed bool
	var failure error
	for _, line := range strings.Split(stdout, "\n") {
		record, ok := parseRecord[codexRecord]([]byte(line))
		if !ok {
			continue
		}

		switch record.Type {
		case "turn.started":
			completed = false
		case "turn.completed":
			completed, failure = true, nil
		case "turn.failed", "error":
			detail := record.Message
			if record.Error.Message != "" {
				detail = record.Error.Message
			}
			failure = fmt.Errorf("Codex protocol %s: %s", record.Type, detail)
		}
	}
	return completed, failure
}
