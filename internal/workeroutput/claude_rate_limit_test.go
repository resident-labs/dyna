package workeroutput

import (
	"testing"
	"time"
)

// The sample matches the records Claude Code writes during real runs; the
// utilization there is a fraction of the cap.
func TestClaudeRateLimitWindows(t *testing.T) {
	stdout := "{\"type\":\"system\",\"subtype\":\"init\"}\n" +
		"{\"type\":\"rate_limit_event\",\"rate_limit_info\":{\"status\":\"allowed\",\"resetsAt\":1792348800,\"rateLimitType\":\"five_hour\",\"unifiedWindows\":{\"five_hour\":{\"utilization\":0.18,\"resetsAt\":1792348800},\"seven_day\":{\"utilization\":0.15,\"resetsAt\":1792890000}}}}\n" +
		"{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"work\"}]}}\n"
	windows := ClaudeRateLimitWindows(stdout)
	if len(windows) != 2 {
		t.Fatalf("windows = %#v, want five_hour and seven_day", windows)
	}
	if windows[0].ID != "five_hour" || windows[0].UsedPercent != 18 || !windows[0].ResetsAt.Equal(time.Unix(1792348800, 0).UTC()) {
		t.Fatalf("five_hour = %#v, want 18%% resetting at 1792348800", windows[0])
	}
	if windows[1].ID != "seven_day" || windows[1].UsedPercent != 15 || !windows[1].ResetsAt.Equal(time.Unix(1792890000, 0).UTC()) {
		t.Fatalf("seven_day = %#v, want 15%% resetting at 1792890000", windows[1])
	}
}

func TestClaudeRateLimitWindowsTakesTheNewestEventAndSkipsOldShapes(t *testing.T) {
	stdout := "{\"type\":\"rate_limit_event\",\"rate_limit_info\":{\"status\":\"allowed\",\"unifiedWindows\":{\"five_hour\":{\"utilization\":0.9,\"resetsAt\":1792348800},\"seven_day\":{\"utilization\":0.8,\"resetsAt\":1792890000}}}}\n" +
		"{\"type\":\"rate_limit_event\",\"rate_limit_info\":{\"status\":\"allowed\"}}\n" +
		"{\"type\":\"rate_limit_event\",\"rate_limit_info\":{\"status\":\"blocked\",\"unifiedWindows\":{\"five_hour\":{\"utilization\":0.42,\"resetsAt\":1792348800},\"seven_day\":{\"utilization\":0.3,\"resetsAt\":1792890000}}}}\n"
	windows := ClaudeRateLimitWindows(stdout)
	if len(windows) != 2 || windows[0].ID != "five_hour" || windows[0].UsedPercent != 42 || windows[1].UsedPercent != 30 {
		t.Fatalf("windows = %#v, want the newest event (42%%, 30%%)", windows)
	}
	if ClaudeRateLimitWindows("{\"type\":\"rate_limit_event\",\"rate_limit_info\":{\"status\":\"allowed\"}}\n") != nil {
		t.Fatal("an event without unifiedWindows must report nothing")
	}
	if ClaudeRateLimitWindows("not json\n") != nil {
		t.Fatal("unparsable output must report nothing")
	}
}
