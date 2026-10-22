package sessionlife

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSnapshotClosedState(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		closed     bool
	}{
		{"open", `{}`, false},
		{"closed", `{"closed":"2026-10-01T00:00:00Z"}`, true},
		{"legacy dormant", `{"closed":"2026-10-01T00:00:00Z","done":true}`, false},
		{"legacy false", `{"closed":"2026-10-01T00:00:00Z","done":false}`, true},
		{"legacy invalid", `{"closed":"2026-10-01T00:00:00Z","done":"true"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s Snapshot
			if err := json.Unmarshal([]byte(tc.body), &s); err != nil {
				t.Fatal(err)
			}
			if s.IsClosed() != tc.closed {
				t.Fatalf("closed=%v, want %v", s.IsClosed(), tc.closed)
			}
			if tc.closed && !s.ClosedAt().Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
				t.Fatalf("close time=%v", s.ClosedAt())
			}
		})
	}
}

func TestTerminalTurnStatuses(t *testing.T) {
	for _, status := range []string{"ok", "error", "stopped", "queued", "running", "unknown", ""} {
		want := status == "ok" || status == "error" || status == "stopped"
		if TerminalTurn(status) != want {
			t.Errorf("terminal(%q)=%v", status, TerminalTurn(status))
		}
	}
}
