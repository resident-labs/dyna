package harness

import (
	"testing"
	"time"

	"dyna-agent/internal/account/reposcope"
)

const kimiQuotaText = `HTTP 429: {"message":"You've reached your 5-hour usage limit. Your quota will reset when the current 5-hour window ends.","type":"exceeded_current_quota_error"}; usage limit reached`

const creditsText = `HTTP 402: "You have run out of credits or need a Grok subscription."`

// Without a stated reset, each failure after the last hold ended waits longer:
// 1 h, 2 h, 4 h, then the 5 h window length, which caps the wait.
func TestQuotaHoldBacksOffOneTwoFourThenFiveHours(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	var hold reposcope.Hold
	var got []time.Duration
	for range 5 {
		hold = quotaHold(kimiQuotaText, at, hold, time.Time{})
		got = append(got, hold.Until.Sub(at))
		at = hold.Until // the next failure comes when the hold ends
	}
	want := []time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour, 5 * time.Hour, 5 * time.Hour}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hold %d = %v, want %v (all holds %v)", i+1, got[i], want[i], got)
		}
	}
}

// A reset read from the provider's current window replaces the backoff, and it
// is a stated end.
func TestQuotaHoldUsesTheCurrentWindowReset(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	reset := at.Add(3 * time.Hour)
	hold := quotaHold(kimiQuotaText, at, reposcope.Hold{}, reset)
	if !hold.Until.Equal(reset) || !hold.Stated || hold.Level != 0 {
		t.Fatalf("quotaHold = %+v, want the stated reset %v with level 0", hold, reset)
	}
}

// A 402 credit failure has no end time. Only a later account refresh or probe clears it.
func TestQuotaHoldForCreditsHasNoTimer(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	hold := quotaHold(creditsText, at, reposcope.Hold{}, time.Time{})
	if !hold.Until.Equal(reposcope.OpenEnded) {
		t.Fatalf("quotaHold for 402 until %v, want no timer (%v)", hold.Until, reposcope.OpenEnded)
	}
}

// A hold that has not ended is kept as it is, so concurrent failures do not step the level again.
func TestQuotaHoldKeepsAnActiveHoldWithoutStepping(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	active := reposcope.Hold{Until: at.Add(30 * time.Minute), Level: 2}
	if got := quotaHold(kimiQuotaText, at, active, time.Time{}); got != active {
		t.Fatalf("quotaHold = %+v, want the active hold %+v", got, active)
	}
}

// The Kimi wording names a 5-hour window length. It does not name the time left.
func TestQuotaWindowReadsTheKimiWindowLength(t *testing.T) {
	if got := quotaWindow(kimiQuotaText); got != 5*time.Hour {
		t.Fatalf("quotaWindow = %v, want 5h0m0s", got)
	}
}

// Dyna's own "resets Mon 22:09" tail has no zone or year, so it never parses as a reset.
func TestUsageResetAtIgnoresTheDynaTail(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	text := "usage limit reached; pi native login reports no reset time. resets Mon 22:09"
	if got := UsageResetAt(text, at); !got.IsZero() {
		t.Fatalf("UsageResetAt = %v, want zero", got)
	}
}
