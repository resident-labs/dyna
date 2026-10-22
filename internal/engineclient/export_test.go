package engineclient

import (
	"testing"
	"time"
)

// SetDeadlines shortens the client's dial deadline and every call deadline
// for one test. The engine side keeps its own deadlines.
func SetDeadlines(t testing.TB, dial, call time.Duration) {
	oldDial, oldDeadline := dialTimeout, deadline
	dialTimeout, deadline = dial, func(string) time.Duration { return call }
	t.Cleanup(func() { dialTimeout, deadline = oldDial, oldDeadline })
}
