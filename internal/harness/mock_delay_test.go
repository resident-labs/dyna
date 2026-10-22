package harness

import (
	"testing"
	"time"
)

func TestMockWorkDelayUsesSleepDirective(t *testing.T) {
	for _, tc := range []struct {
		name, prompt string
		want         time.Duration
	}{
		{"default", "RESPOND: ready", 400 * time.Millisecond},
		{"immediate", "SLEEP:0\nRESPOND: ready", 0},
		{"explicit", "SLEEP:7\nRESPOND: ready", 7 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mockWorkDelay(tc.prompt); got != tc.want {
				t.Fatalf("mock work delay=%s, want %s", got, tc.want)
			}
		})
	}
}
