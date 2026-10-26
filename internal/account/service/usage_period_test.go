package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUsageSummaryPreservesPeriodWithoutAllowance(t *testing.T) {
	var account Account
	if err := json.Unmarshal([]byte(`{"windows":[{"id":"grok-weekly","label":"Weekly period","resetsAt":"2027-01-01T00:00:00Z","unavailableReason":"Grok does not report remaining allowance."}]}`), &account); err != nil {
		t.Fatal(err)
	}
	summary := WindowsSummary(account)
	for _, want := range []string{"Weekly period", "resets", "Remaining allowance not reported."} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary hid period or reason: %q", summary)
		}
	}
	if strings.Contains(summary, "0%") {
		t.Fatal("invented zero usage")
	}
}
