package harness

import (
	"fmt"
	"regexp"
	"strings"

	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/providerfailure"
)

var antigravityACPLogPrefix = regexp.MustCompile(`^[EF][0-9]{4} [0-9:.]+[ \t]+[0-9]+ [^\r\n]+:[0-9]+\] `)

// TODO: legacy/shim remove later — preserve ACP 1.3.0 failures until the server reports structured prompt errors.
func antigravityACPPromptError(session, stderr string) error {
	if session == "" {
		return nil
	}
	markers := map[string]bool{
		"Antigravity SDK error processing prompt for session " + session:                true,
		"Agent connection lost for session " + session + " after rebuild retry":         true,
		"Failed to rebuild agent for session " + session + "; connection unrecoverable": true,
	}
	for _, line := range strings.Split(stderr, "\n") {
		message := strings.TrimSpace(line)
		if location := antigravityACPLogPrefix.FindStringIndex(message); location != nil {
			message = message[location[1]:]
		}
		message = strings.TrimPrefix(message, "ERROR:root:")
		if markers[message] {
			// The server names no reason, so the marker line and the stderr tail
			// around it are the evidence. Model output is not involved.
			return providerfailure.TagStderr(fmt.Errorf("Antigravity ACP prompt failed: %s", tail(strings.TrimSpace(stderr), 2000)), stderr)
		}
	}
	return nil
}

// finishStderr drains this attachment's pending diagnostics before the snapshot,
// without stopping its retained child.
func (s *acpSession) finishStderr() string {
	if !s.antigravity || s.host == nil {
		return ""
	}
	if local, ok := s.host.(sessionhost.LocalExtension); ok {
		return local.StderrTail()
	}
	return ""
}
