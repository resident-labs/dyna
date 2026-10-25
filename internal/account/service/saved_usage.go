package service

import (
	"strings"
	"time"

	"dyna-agent/internal/account/native"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

func entryCaptured(e accountstore.Entry) native.Captured {
	return native.Captured{Identity: e.Identity, Integration: e.Integration, Label: e.Label, Credential: e.Credential}
}

const activeUsageTTL = 2 * time.Minute

func savedTTL(string) time.Duration {
	return 10 * time.Minute
}
func applySavedObservation(a *Account, e accountstore.Entry, now time.Time) {
	a.ObservedAt = time.Time{}
	if o := e.Observation; o != nil {
		a.UsageError = o.UsageError
		a.UsageReason = o.Reason
		if o.Plan != nil {
			a.Plan = o.Plan
		}
		a.Windows = append([]Window{}, o.Windows...)
		a.ObservedAt, a.RateLimitedUntil = o.ObservedAt, o.RateLimitedUntil
		ttl := savedTTL(e.Harness)
		if a.Active {
			ttl = activeUsageTTL
		}
		checked := o.ObservedAt
		if museIdle(a) {
			checked = o.LastAttemptAt
		}
		a.usageCheckedAt = checked
		a.Stale = o.Unavailable || checked.IsZero() || now.Sub(checked) >= ttl
		if o.Unavailable && !e.NeedsLogin {
			a.Status = "unavailable"
		}
	}
	if e.Credential.Schema != "" {
		setCredentialExpiry(a, entryCaptured(e))
	}
	resetWindows(a, now)
}

const probablyResetReason = "probably reset"

func resetWindows(a *Account, now time.Time) {
	a.Windows = append([]Window{}, a.Windows...)
	for i, w := range a.Windows {
		reset, err := time.Parse(time.RFC3339Nano, w.ResetsAt)
		if err == nil && !now.Before(reset) && a.ObservedAt.Before(reset) {
			a.Windows[i].UsedPercent = nil
			a.Windows[i].Used = nil
			a.Windows[i].UnavailableReason = probablyResetReason
		}
	}
}

// AccountUsageCached returns saved observations without network or native credential I/O.
func (s *Service) AccountUsageCached() Snapshot {
	locked := s.mu.TryLock()
	if locked {
		defer s.mu.Unlock()
	}
	now := s.o.Now().UTC()
	if locked && len(s.last.Accounts) > 0 {
		out := s.last
		out.Accounts = append([]Account{}, out.Accounts...)
		for i := range out.Accounts {
			a := &out.Accounts[i]
			ttl := savedTTL(a.Harness)
			if a.Active {
				ttl = activeUsageTTL
			}
			checked := a.ObservedAt
			if museIdle(a) {
				checked = a.usageCheckedAt
			}
			a.Stale = a.Stale || checked.IsZero() || now.Sub(checked) >= ttl
			resetWindows(a, now)
		}
		ApplyBlockingQuota(out.Accounts, now)
		return out
	}
	return CachedUsage(s.o.Store, now)
}

// CachedUsage reads saved observations directly, without a service or provider calls.
func CachedUsage(store *accountstore.Store, now time.Time) Snapshot {
	now = now.UTC()
	out := Snapshot{Accounts: []Account{}, CheckedAt: now}
	if store == nil {
		return out
	}
	st, err := store.Read()
	if err != nil {
		out.Message = err.Error()
		return out
	}
	accounts := &runtime.Accounts{Root: store.Root}
	for _, e := range st.Entries {
		if e.NativeHome == "" {
			continue
		}
		active, _ := accounts.Active(runtime.SelectionProvider(e.Harness, e.Provider), e.Harness)
		a := Account{ID: e.ID, Harness: e.Harness, Provider: e.Provider, Label: e.Label, Integration: e.Integration, ProviderID: e.Harness, NeedsLogin: e.NeedsLogin, Status: "ready", Source: "Dyna private native account", Active: active.ID == e.ID, Windows: []Window{}, Profiles: []string{}}
		if e.NeedsLogin {
			a.Status = "needs_auth"
		}
		applySavedObservation(&a, e, now)
		out.Accounts = append(out.Accounts, a)
	}
	ApplyBlockingQuota(out.Accounts, now)
	return out
}

func museIdle(a *Account) bool {
	if a.Harness != "muse" {
		return false
	}
	for _, w := range a.Windows {
		if w.ID == "subscription" && strings.HasSuffix(w.Label, " (idle)") {
			return true
		}
	}
	return false
}
