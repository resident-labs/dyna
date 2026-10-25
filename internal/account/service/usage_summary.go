package service

import (
	"fmt"
	"strings"
	"time"
)

const (
	noteThreshold = 50.0
	noteStaleAge  = time.Hour
)

// WindowPercent is a window's used share, from the percent or from used and limit.
func WindowPercent(w Window) (float64, bool) {
	if w.UsedPercent != nil {
		return *w.UsedPercent, true
	}
	if w.Used != nil && w.Limit != nil && *w.Limit > 0 {
		return *w.Used / *w.Limit * 100, true
	}
	return 0, false
}

// WindowsSummary renders "5-hour 12% · weekly 41%, resets Sat 20:00".
// Muse includes every reset; other providers show the busiest window’s reset.
// Readings from before a passed reset show uncertainty and their check time.
func WindowsSummary(a Account) string { return windowsSummary(a, time.Now()) }

func windowsSummary(a Account, now time.Time) string {
	resetWindows(&a, now)
	parts, busiest, resets := []string{}, -2.0, ""
	reasons := []string{}
	for _, w := range a.Windows {
		if w.UnavailableReason == probablyResetReason {
			checked := "unknown"
			if !a.ObservedAt.IsZero() {
				checked = a.ObservedAt.Local().Format("Mon 3:04 PM")
			}
			parts = append(parts, w.Label+": "+probablyResetReason+" · last checked "+checked)
			continue
		}
		if w.Balance != nil {
			parts = append(parts, w.Label+" "+money(*w.Balance, w.Unit))
			continue
		}
		p, ok := WindowPercent(w)
		if ok {
			parts = append(parts, fmt.Sprintf("%s %.0f%%", w.Label, p))
		} else {
			parts = append(parts, w.Label)
			reason := w.UnavailableReason
			if reason == "" {
				reason = "Remaining allowance not reported."
			}
			reasons = append(reasons, reason)
			p = -1
		}
		if t, err := time.Parse(time.RFC3339Nano, w.ResetsAt); err == nil {
			if !t.After(now) {
				if resets == "" {
					resets = "unknown (refreshing)"
				}
			} else if a.Harness == "muse" {
				parts[len(parts)-1] += ", resets " + t.Local().Format("Mon 15:04")
			} else if p > busiest || resets == "unknown (refreshing)" {
				busiest, resets = p, t.Local().Format("Mon 15:04")
			}
		}
	}
	if a.UsageError != "" {
		message := a.UsageError
		if a.RateLimitedUntil == nil {
			message = "Usage unavailable: " + message
		}
		parts = append(parts, message)
	}
	out := strings.Join(parts, " · ")
	if resets != "" {
		out += ", resets " + resets
	}
	if len(reasons) > 0 {
		out += ". " + strings.Join(reasons, " ")
	}
	return out
}

// money formats a balance in its unit. USD gets a dollar sign.
func money(amount float64, unit string) string {
	if strings.EqualFold(unit, "USD") {
		return fmt.Sprintf("$%.2f", amount)
	}
	if unit == "" {
		return fmt.Sprintf("%.2f", amount)
	}
	return fmt.Sprintf("%.2f %s", amount, unit)
}

// UsageNote is the one-line turn header for an account. It is empty unless a
// window is at 50% or more, or the cached reading is more than an hour old.
func UsageNote(a Account, now time.Time) string {
	resetWindows(&a, now)
	high, advice := false, ""
	for _, w := range a.Windows {
		if p, ok := WindowPercent(w); ok && p >= noteThreshold {
			high = true
			if p > 80 {
				advice = " Use a cheaper profile and check `dyna accounts list` before large fan-outs."
			}
		}
	}
	age := ""
	switch {
	case a.ObservedAt.IsZero():
		age = " (no reading yet; run `dyna accounts list --refresh`)"
	case now.Sub(a.ObservedAt) > noteStaleAge:
		age = fmt.Sprintf(" (reading %d h old; run `dyna accounts list --refresh`)", int(now.Sub(a.ObservedAt)/time.Hour))
	case !high && a.UsageError == "":
		return ""
	}
	return fmt.Sprintf("[DYNA USAGE] %s (%s): %s%s.%s", a.Harness, a.Label, windowsSummary(a, now), age, advice)
}

// OwnNote is UsageNote for the active account of a harness, from cached data only.
func (s *Service) OwnNote(harness string) string {
	for _, a := range s.AccountUsageCached().Accounts {
		if a.Harness == harness && a.Active {
			return UsageNote(a, s.o.Now())
		}
	}
	return ""
}
