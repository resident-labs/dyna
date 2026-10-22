package store

import (
	"math"
	"testing"
	"time"
)

func scoreFixture(now time.Time, used float64, hoursToReset float64, plan *Plan) *Observation {
	window := Window{ID: "seven_day", Label: "Weekly (7-day)", UsedPercent: &used, Unit: "percent"}
	if !math.IsNaN(hoursToReset) {
		window.ResetsAt = now.Add(time.Duration(hoursToReset * float64(time.Hour))).Format(time.RFC3339Nano)
	}
	return &Observation{Plan: plan, Windows: []Window{window}, ObservedAt: now}
}

// Room that is about to reset is worth spending before room that will keep,
// even when the account about to reset has less of it left.
func TestScorePrefersRoomThatResetsSoon(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	soon := scoreFixture(now, 40, 14, &Plan{ID: "max_20x", Label: "max_20x"})
	later := scoreFixture(now, 10, 6*24, &Plan{ID: "max", Label: "max"})
	if soonScore, laterScore := Score(soon, now), Score(later, now); soonScore <= laterScore {
		t.Fatalf("score %v (resets in 14h) must outrank %v (resets in 6d)", soonScore, laterScore)
	}
	// The values match the owner's swap tool: (1-0.4)*20/(14/24)² and
	// (1-0.1)*5/6².
	if got, want := Score(soon, now), 0.6*20/(14.0/24*14.0/24); math.Abs(got-want) > 1e-9 {
		t.Fatalf("soon score = %v, want %v", got, want)
	}
	if got, want := Score(later, now), 0.9*5/36; math.Abs(got-want) > 1e-9 {
		t.Fatalf("later score = %v, want %v", got, want)
	}
}

func TestScoreNeverEligibleNearACap(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	weekly98 := scoreFixture(now, 98, 24, nil)
	if Score(weekly98, now) != -1 {
		t.Fatal("an account at 98% weekly use must be ineligible")
	}
	used := 95.0
	sessionCapped := &Observation{Windows: []Window{
		{ID: "five_hour", UsedPercent: &used, ResetsAt: now.Add(time.Hour).Format(time.RFC3339Nano)},
		{ID: "seven_day", UsedPercent: ptr(10.0), ResetsAt: now.Add(48 * time.Hour).Format(time.RFC3339Nano)},
	}, ObservedAt: now}
	if Score(sessionCapped, now) != -1 {
		t.Fatal("an account at 95% session use must be ineligible")
	}
	blocked := scoreFixture(now, 10, 24, nil)
	until := now.Add(2 * time.Hour)
	blocked.RateLimitedUntil = &until
	if Score(blocked, now) != -1 {
		t.Fatal("an account with an uncleared rate limit must be ineligible")
	}
	if Score(&Observation{ObservedAt: now}, now) != -1 || Score(nil, now) != -1 {
		t.Fatal("an account without a weekly reading must be ineligible")
	}
}

func TestScoreBlockedAtExactlyNinetyPercent(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	atCap := scoreFixture(now, 90, 24, nil)
	if Score(atCap, now) != -1 {
		t.Fatal("an account at exactly 90% weekly use must be ineligible")
	}
	below := scoreFixture(now, 89.9, 24, nil)
	if Score(below, now) <= 0 {
		t.Fatalf("score below the cap = %v, want a positive rank", Score(below, now))
	}
}

func TestScoreWithoutResetTimeAssumesAWeek(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	noReset := scoreFixture(now, 50, math.NaN(), &Plan{ID: "max"})
	if got, want := Score(noReset, now), 0.5*5/49; math.Abs(got-want) > 1e-9 {
		t.Fatalf("score = %v, want %v (a full week until reset)", got, want)
	}
	// A reset less than half a day away cannot inflate the score further.
	justReset := scoreFixture(now, 50, 1, nil)
	if got, want := Score(justReset, now), 0.5*1/0.25; math.Abs(got-want) > 1e-9 {
		t.Fatalf("score = %v, want %v (days clamped to 0.5)", got, want)
	}
}

func TestScorePlanSize(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		plan *Plan
		want float64
	}{
		{&Plan{ID: "max_20x"}, 20},
		{&Plan{ID: "Max 5x", Label: "Max 5x"}, 5},
		{&Plan{ID: "max"}, 5},
		{&Plan{ID: "pro"}, 1},
		{nil, 1},
	} {
		o := scoreFixture(now, 50, 24, tc.plan)
		if got := Score(o, now); math.Abs(got-tc.want*0.5) > 1e-9 {
			t.Fatalf("plan %v score = %v, want plan size %v", tc.plan, got, tc.want)
		}
	}
}

func TestScoreFindsWindowsAcrossHarnesses(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	used := 20.0
	for _, tc := range []struct {
		name    string
		session string
		weekly  string
	}{
		{"claude", "five_hour", "seven_day"},
		{"codex", "five_hour", "seven_day"},
		{"muse", "subscription", "weekly"},
		{"grok", "", "grok-weekly"},
		{"opencode", "fiveHour", "week"},
	} {
		windows := []Window{}
		if tc.session != "" {
			windows = append(windows, Window{ID: tc.session, UsedPercent: &used, ResetsAt: now.Add(time.Hour).Format(time.RFC3339Nano)})
		}
		windows = append(windows, Window{ID: tc.weekly, UsedPercent: ptr(20.0), ResetsAt: now.Add(24 * time.Hour).Format(time.RFC3339Nano)})
		o := &Observation{Windows: windows, ObservedAt: now}
		if got := Score(o, now); got <= 0 {
			t.Fatalf("%s: score = %v, want a positive rank", tc.name, got)
		}
		if WeeklyWindow(o) == nil || WeeklyWindow(o).ID != tc.weekly {
			t.Fatalf("%s: WeeklyWindow = %v, want %s", tc.name, WeeklyWindow(o), tc.weekly)
		}
	}
	// Buckets without a weekly window (Antigravity) have no score.
	if WeeklyWindow(&Observation{Windows: []Window{{ID: "requests"}}}) != nil {
		t.Fatal("a non-weekly window must not read as weekly")
	}
}

func ptr(f float64) *float64 { return &f }
