package workeroutput

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// MuseRunError preserves the explicit terminal status even when stdout has
// partial answer text and the CLI exits zero. Tool events and prose do not
// decide whether the run failed; a later successful run terminal clears it.
func MuseRunError(stdout string) error {
	var failure error
	for _, line := range strings.Split(stdout, "\n") {
		record, ok := parseRecord[museRecord]([]byte(line))
		if !ok {
			continue
		}

		switch record.PayloadType {
		case "run.terminal.completed":
			failure = nil
		case "run.terminal.failed":
			detail := record.Payload.Text
			if strings.TrimSpace(detail) == "" {
				detail = string(record.Payload.raw)
			}
			failure = fmt.Errorf("Muse protocol run.terminal.failed: %s", detail)
		}
	}
	return failure
}

// MuseQuotaReset returns only the reset time of an explicit subscription error.
// Assistant prose and tool output cannot create an account quota observation.
func MuseQuotaReset(stdout, stderr string) (time.Time, bool) {
	// A completed terminal clears quota errors from an earlier retry.
	completed := false
	for _, line := range strings.Split(stdout, "\n") {
		record, ok := parseRecord[museRecord]([]byte(line))
		if !ok {
			continue
		}
		switch record.PayloadType {
		case "run.terminal.completed":
			completed = true
		case "run.terminal.failed":
			completed = false
		}
	}
	if completed {
		return time.Time{}, false
	}
	detail := stderr
	if failure := MuseRunError(stdout); failure != nil {
		detail += "\n" + failure.Error()
	}
	match := museQuotaResetPattern.FindStringSubmatch(detail)
	if len(match) != 2 {
		return time.Time{}, false
	}
	reset, err := time.Parse(time.RFC3339Nano, match[1])
	return reset.UTC(), err == nil
}

var museQuotaResetPattern = regexp.MustCompile(`429 Subscription quota exhausted\. Your usage window resets at ([0-9T:.+Z-]+)`)
