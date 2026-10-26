package engineclient

import (
	"net/http"
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

// FakeClock replaces the client's clock: now reads at, and each wait moves at
// forward by the wait and records it in waits.
func FakeClock(at *time.Time, waits *[]time.Duration) Option {
	return func(c *Client) {
		c.now = func() time.Time { return *at }
		c.after = func(d time.Duration) <-chan time.Time {
			*waits = append(*waits, d)
			*at = at.Add(d)
			done := make(chan time.Time, 1)
			done <- *at
			return done
		}
	}
}

// RoundTrip replaces the client's transport, so a test answers each send itself.
func RoundTrip(rt http.RoundTripper) Option {
	return func(c *Client) { c.http = &http.Client{Transport: rt} }
}
