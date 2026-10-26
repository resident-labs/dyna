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
// A saved reading is replaced on the next read, so a reason Dyna no longer knows
// needs no migration: both clients treat it as not reported.
func TestWindowReasonsAreCodesNotReaderText(t *testing.T) {
	var account Account
	if err := json.Unmarshal([]byte(`{"windows":[{"id":"grok-weekly","label":"Weekly period","resetsAt":"2027-01-01T00:00:00Z"},{"id":"other","label":"Other","reason":"a-code-dyna-dropped"}]}`), &account); err != nil {
		t.Fatal(err)
	}
	summary := WindowsSummary(account)
	if strings.Contains(summary, "a-code-dyna-dropped") || strings.Count(summary, "Remaining allowance not reported.") != 2 {
		t.Fatalf("summary did not use designed copy for absent and unknown reasons: %q", summary)
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
