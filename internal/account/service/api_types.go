package service

import (
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"time"
)

type Account struct {
	usageCheckedAt       time.Time
	UsageError           string            `json:"usageError,omitempty"`
	UsageReason          store.UsageReason `json:"usageReason,omitempty"`
	ExpiresAt            *time.Time        `json:"expiresAt,omitempty"`
	RefreshMessage       string            `json:"refreshMessage,omitempty"`
	LastRenewal          *store.Renewal    `json:"lastRenewal,omitempty"`
	QuotaBlocked         bool              `json:"quotaBlocked"`
	QuotaBlockedUntil    *time.Time        `json:"quotaBlockedUntil,omitempty"`
	BlockingQuotaWindows []string          `json:"blockingQuotaWindows,omitempty"`
	ID                   string            `json:"id"`
	Harness              string            `json:"harness"`
	Provider             store.Provider    `json:"provider,omitempty"`
	Kind                 store.Kind        `json:"kind,omitempty"`
	Repos                []string          `json:"repos" tstype:"string[] | null"`
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
	Profiles             []string          `json:"profiles" tstype:"string[] | null"`
	Windows              []Window          `json:"windows" tstype:"Window[] | null"`
	Integration          string            `json:"integration,omitempty"`
	NeedsLogin           bool              `json:"needsLogin"`
}
type Snapshot struct {
	Accounts []Account `json:"accounts" tstype:"Account[] | null"`
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
	Limits        []Limit `json:"limits" tstype:"Limit[] | null"`
	// Efforts lists reasoning levels with the extraArgs that select each one.
	Efforts []Effort `json:"efforts,omitempty"`
}
type Catalog struct {
	Models []Model `json:"models" tstype:"Model[] | null"`
	// Compact is what the harness accepts for the profile's compact field.
	Compact    profile.CompactRule `json:"compact"`
	Source     string              `json:"source"`
	ObservedAt time.Time           `json:"observedAt"`
	Message    string              `json:"message,omitempty"`
}
