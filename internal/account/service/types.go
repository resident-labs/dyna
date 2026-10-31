// Package service serves redacted account and model views to profiles and Usage.
package service

import (
	"dyna-agent/internal/account/service/adapters"
	"dyna-agent/internal/account/store"
	"strings"
	"time"
)

type Plan = store.Plan
type Window = store.Window
type Effort = adapters.Effort

// hideUnusedModelWindows drops a model-specific window while it shows 0% used.
// Once the model has any use, the window stays visible.
func hideUnusedModelWindows(accounts []Account) {
	for i := range accounts {
		a := &accounts[i]
		kept := make([]Window, 0, len(a.Windows))
		for _, w := range a.Windows {
			if p, ok := WindowPercent(w); ok && p == 0 && strings.HasPrefix(w.ID, store.ModelWindowPrefix) {
				continue
			}
			kept = append(kept, w)
		}
		a.Windows = kept
	}
}

// ApplyBlockingQuota derives blocking metadata from exhausted future quota windows.
// The latest reset is when every blocking window has cleared.
func ApplyBlockingQuota(accounts []Account, now time.Time) {
	for i := range accounts {
		a := &accounts[i]
		a.QuotaBlocked = false
		a.QuotaBlockedUntil = nil
		a.BlockingQuotaWindows = nil
		for _, w := range a.Windows {
			if w.ID != "five_hour" && w.ID != "seven_day" {
				continue
			}
			reset, err := time.Parse(time.RFC3339Nano, w.ResetsAt)
			if err != nil || !reset.After(now) {
				continue
			}
			exhausted := w.UsedPercent != nil && *w.UsedPercent >= 100
			exhausted = exhausted || w.Used != nil && w.Limit != nil && *w.Limit > 0 && *w.Used >= *w.Limit
			if !exhausted {
				continue
			}
			a.QuotaBlocked = true
			a.BlockingQuotaWindows = append(a.BlockingQuotaWindows, w.ID)
			if a.QuotaBlockedUntil == nil || reset.After(*a.QuotaBlockedUntil) {
				a.QuotaBlockedUntil = &reset
			}
		}
	}
}
