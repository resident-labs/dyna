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

// visibleSnapshot returns a copy of out without model-specific windows whose
// pool shows 0% used everywhere. A pool is one model window of one provider,
// across its accounts. While any account in the pool has use, every account
// keeps its window, so the pool mean counts each account. The cached snapshot
// keeps every window, so a hidden window can return after its reset as a
// probable-reset window.
func visibleSnapshot(out Snapshot) Snapshot {
	type pool struct {
		provider store.Provider
		id       string
	}
	inUse := map[pool]bool{}
	for _, a := range out.Accounts {
		for _, w := range a.Windows {
			if p, ok := WindowPercent(w); strings.HasPrefix(w.ID, store.ModelWindowPrefix) && (!ok || p != 0) {
				inUse[pool{a.Provider, w.ID}] = true
			}
		}
	}
	// The zero-capacity slices keep nil and empty values as they were, and
	// appending to them never writes into the source arrays.
	accounts := out.Accounts[:0:0]
	for _, a := range out.Accounts {
		kept := a.Windows[:0:0]
		for _, w := range a.Windows {
			if p, ok := WindowPercent(w); ok && p == 0 && strings.HasPrefix(w.ID, store.ModelWindowPrefix) && !inUse[pool{a.Provider, w.ID}] {
				continue
			}
			kept = append(kept, w)
		}
		a.Windows = kept
		accounts = append(accounts, a)
	}
	out.Accounts = accounts
	return out
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
