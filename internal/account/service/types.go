// Package service serves redacted account and model views to profiles and Usage.
package service

import (
	"time"

	"dyna-agent/internal/account/service/adapters"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
)

type Plan = store.Plan
type Window = store.Window
type Effort = adapters.Effort
type Account struct {
	usageCheckedAt       time.Time
	UsageError           string            `json:"usageError,omitempty"`
	UsageReason          store.UsageReason `json:"usageReason,omitempty"`
	ExpiresAt            *time.Time        `json:"expiresAt,omitempty"`
	RefreshMessage       string            `json:"refreshMessage,omitempty"`
	QuotaBlocked         bool              `json:"quotaBlocked"`
	QuotaBlockedUntil    *time.Time        `json:"quotaBlockedUntil,omitempty"`
	BlockingQuotaWindows []string          `json:"blockingQuotaWindows,omitempty"`
	ID                   string            `json:"id"`
	Harness              string            `json:"harness"`
	Provider             store.Provider    `json:"provider,omitempty"`
	Kind                 store.Kind        `json:"kind,omitempty"`
	Repos                []string          `json:"repos"`
	Label                string            `json:"label"`
	ProviderID           string            `json:"providerId"`
	RouteID              string            `json:"routeId,omitempty"`
	Active               bool              `json:"active"`
	Status               string            `json:"status"`
	Plan                 *Plan             `json:"plan,omitempty"`
	Source               string            `json:"source"`
	ObservedAt           time.Time         `json:"observedAt"`
	RateLimitedUntil     *time.Time        `json:"rateLimitedUntil,omitempty"`
	Stale                bool              `json:"stale"`
	Profiles             []string          `json:"profiles"`
	Windows              []Window          `json:"windows"`
	Integration          string            `json:"integration,omitempty"`
	NeedsLogin           bool              `json:"needsLogin"`
}
type Snapshot struct {
	Accounts []Account `json:"accounts"`
	// Problems names each saved row the list skipped, so one bad row cannot hide
	// the accounts that could be read.
	Problems  []runtime.AccountProblem `json:"problems,omitempty"`
	CheckedAt time.Time                `json:"checkedAt"`
	Message   string                   `json:"message,omitempty"`
}
type Limit struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Source string `json:"source"`
}
type Model struct {
	ID            string  `json:"id"`
	Label         string  `json:"label"`
	ProviderID    string  `json:"providerId,omitempty"`
	IntegrationID string  `json:"integrationId,omitempty"`
	Publisher     string  `json:"publisher,omitempty"`
	Context       int64   `json:"context,omitempty"`
	Input         int64   `json:"input,omitempty"`
	Output        int64   `json:"output,omitempty"`
	Category      string  `json:"category"`
	Source        string  `json:"source"`
	Description   string  `json:"description,omitempty"`
	Limits        []Limit `json:"limits"`
	// Efforts lists reasoning levels with the extraArgs that select each one.
	Efforts []Effort `json:"efforts,omitempty"`
}
type Catalog struct {
	Models []Model `json:"models"`
	// Compact is what the harness accepts for the profile's compact field.
	Compact    profile.CompactRule `json:"compact"`
	Source     string              `json:"source"`
	ObservedAt time.Time           `json:"observedAt"`
	Message    string              `json:"message,omitempty"`
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
			if w.ID != "five_hour" && w.ID != "seven_day" && !(a.Harness == "muse" && w.ID == "subscription") {
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
