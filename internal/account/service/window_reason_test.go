package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	accountstore "dyna-agent/internal/account/store"
)

// D-124: a window's missing reading crossed the API as the reader's own sentence,
// so the UI rendered reader wording and the client compared that sentence as a
// literal. Window reasons are typed codes now, and each client shows its copy.
func TestWindowReasonsAreCodesNotReaderText(t *testing.T) {
	// Accounts saved before the change carry the reader's prose. It loads as the
	// not-reported code, so every client can show its own sentence.
	var legacy accountstore.Window
	if err := json.Unmarshal([]byte(`{"id":"grok-weekly","label":"Weekly","unavailableReason":"Grok does not report remaining allowance."}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Reason != accountstore.WindowReasonNotReported {
		t.Fatalf("legacy window reason = %q, want %q", legacy.Reason, accountstore.WindowReasonNotReported)
	}
	var account Account
	if err := json.Unmarshal([]byte(`{"windows":[{"id":"grok-weekly","label":"Weekly period","resetsAt":"2027-01-01T00:00:00Z","reason":"not-reported"}]}`), &account); err != nil {
		t.Fatal(err)
	}
	summary := WindowsSummary(account)
	if strings.Contains(summary, "Grok does not report") || !strings.Contains(summary, "Remaining allowance not reported.") {
		t.Fatalf("summary did not use designed copy: %q", summary)
	}
	// A passed reset marks the code the app reads to say when it last checked.
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	value := 99.0
	a := Account{ObservedAt: now.Add(-time.Hour), Windows: []Window{{Label: "Weekly", UsedPercent: &value, ResetsAt: now.Add(-time.Minute).Format(time.RFC3339)}}}
	resetWindows(&a, now)
	if a.Windows[0].Reason != accountstore.WindowReasonProbableReset {
		t.Fatalf("projected window reason = %q, want %q", a.Windows[0].Reason, accountstore.WindowReasonProbableReset)
	}
}
