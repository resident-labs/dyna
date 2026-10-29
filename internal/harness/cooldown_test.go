package harness

import (
	"testing"
	"time"
)

func TestUsageResetAtReadsZAIQuotaResetAsBeijingTime(t *testing.T) {
	const body = `{"code":"1308","message":"Usage limit reached for 5 hour. Your limit will reset at 2026-10-29 18:46:09"}`
	now := time.Date(2026, time.October, 29, 7, 19, 38, 0, time.UTC)
	want := time.Date(2026, time.October, 29, 10, 46, 9, 0, time.UTC)

	if got := UsageResetAt(body, now); !got.Equal(want) {
		t.Fatalf("UsageResetAt() = %v, want %v", got, want)
	}
	if got := UnavailableUntil(body, now); !got.Equal(want) {
		t.Fatalf("UnavailableUntil() = %v, want the stated reset %v", got, want)
	}
}
