package sessionhost

import (
	"strings"
	"testing"
)

// Keep the reader unscheduled until the diagnostic request. The child still
// owns an open writer, so EOF is not available as a completion boundary.
func TestStderrTailDrainsPendingDiagnostics(t *testing.T) {
	c := &child{}
	var err error
	c.stderrPipe, err = newStderrPipe(&c.stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.stderrPipe.reader.Close()
	defer c.stderrPipe.writer.Close()
	const diagnostic = "Antigravity SDK error processing prompt for session last-turn\n"
	if _, err := c.stderrPipe.writer.Write([]byte(diagnostic)); err != nil {
		t.Fatal(err)
	}
	s := &localSession{last: c}
	if got := s.StderrTail(); !strings.Contains(got, diagnostic) {
		t.Fatalf("last diagnostic was not drained: %q", got)
	}
}
