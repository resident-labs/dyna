package store

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SavedUsageWindow is the refresh interval for saved account usage.
const SavedUsageWindow = 10 * time.Minute

// NearCapUsedPercent marks a window so used that the automatic account pick
// skips the account. It matches the owner's claude-accounts-usage swap tool
// (NEAR_CAP_SESSION and NEAR_CAP_WEEKLY, both 0.90).
const NearCapUsedPercent = 90.0

// UsageDecision separates whether an account can take work from whether its
// saved usage has enough weekly data for a numeric rank. Score is -1 when the
// account is ineligible or its usage cannot be ranked.
type UsageDecision struct {
	Eligible bool
	Score    float64
}

// DecideUsage applies the shared account-eligibility rules. A missing reading
// or weekly window is eligible but unranked. A weekly window without a
// percentage is also eligible but unranked. An unavailable reading or active
// rate limit is ineligible. With a weekly window, a reported session or weekly
// window at NearCapUsedPercent is also ineligible.
func DecideUsage(o *Observation, now time.Time) UsageDecision {
	decision := UsageDecision{Score: -1}
	if o == nil {
		decision.Eligible = true
		return decision
	}
	if o.Unavailable || o.RateLimitedUntil != nil && o.RateLimitedUntil.After(now) {
		return decision
	}
	weekly := WeeklyWindow(o)
	if weekly == nil {
		decision.Eligible = true
		return decision
	}
	if w := SessionWindow(o); w != nil && w.UsedPercent != nil && *w.UsedPercent >= NearCapUsedPercent {
		return decision
	}
	if weekly.UsedPercent != nil && *weekly.UsedPercent >= NearCapUsedPercent {
		return decision
	}
	decision.Eligible = true
	if weekly.UsedPercent == nil {
		return decision
	}
	// Without a readable reset time, a full week is the honest guess.
	days := 7.0
	if reset, err := time.Parse(time.RFC3339Nano, weekly.ResetsAt); err == nil {
		days = reset.Sub(now).Hours() / 24
	}
	if days < 0.5 {
		days = 0.5
	}
	spare := 1 - *weekly.UsedPercent/100
	if spare < 0 {
		spare = 0
	}
	decision.Score = spare * planSize(o.Plan) / (days * days)
	return decision
}

// Score ranks how much work an account can still take: spare weekly room ×
// plan size ÷ (days to the weekly reset)². Squaring the days spends room that
// is about to reset before room that will keep: an untouched account's weekly
// clock has not started, so it can wait. The shape comes from the owner's
// claude-accounts-usage swap tool.
func Score(o *Observation, now time.Time) float64 {
	return DecideUsage(o, now).Score
}

// WeeklyWindow returns the 7-day usage window: seven_day (Claude),
// weekly (Muse), grok-weekly (Grok) or week (OpenCode). Nil when the reading
// has none.
func WeeklyWindow(o *Observation) *Window {
	if o == nil {
		return nil
	}
	for i := range o.Windows {
		if id := o.Windows[i].ID; id == "seven_day" || strings.Contains(id, "week") {
			return &o.Windows[i]
		}
	}
	return nil
}

// SessionWindow returns the 5-hour session window: five_hour (Claude),
// fiveHour (OpenCode) or subscription (Muse).
func SessionWindow(o *Observation) *Window {
	if o == nil {
		return nil
	}
	for i := range o.Windows {
		switch o.Windows[i].ID {
		case "five_hour", "fiveHour", "subscription":
			return &o.Windows[i]
		}
	}
	return nil
}

var maxPlanTier = regexp.MustCompile(`max[ _-]?(\d+)x`)

// planSize is what a percentage of this plan is worth: Max 20x → 20, Max 5x →
// 5, and 1 when the plan is unknown.
func planSize(p *Plan) float64 {
	if p == nil {
		return 1
	}
	for _, text := range []string{p.ID, p.Label} {
		if m := maxPlanTier.FindStringSubmatch(strings.ToLower(text)); m != nil {
			if size, err := strconv.ParseFloat(m[1], 64); err == nil && size > 0 {
				return size
			}
		}
	}
	if strings.EqualFold(p.ID, "max") {
		return 5
	}
	return 1
}
