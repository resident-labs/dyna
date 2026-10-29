package service

import (
	"path/filepath"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/reposcope"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

func entryCaptured(e accountstore.Entry) native.Captured {
	return native.Captured{Identity: e.Identity, Integration: e.Integration, Label: e.Label, Credential: e.Credential}
}

const activeUsageTTL = 2 * time.Minute

// inactiveUsageWindow is how long a saved reading of an inactive account counts
// as current. The usage timer re-reads an inactive account when its reading
// reaches this age, so one window decides both what the page calls stale and
// when the timer probes again. Two different windows left an inactive reading
// shown as stale for most of every cycle (D-148).
const inactiveUsageWindow = 10 * time.Minute

func savedTTL(string) time.Duration {
	return inactiveUsageWindow
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
		a.usageCheckedAt = o.ObservedAt
		a.Stale = o.Unavailable || o.ObservedAt.IsZero() || now.Sub(o.ObservedAt) >= ttl
		if o.Unavailable && !e.NeedsLogin {
			a.Status = "unavailable"
		}
	}
	if e.Credential.Schema != "" {
		setCredentialExpiry(a, entryCaptured(e))
	}
	resetWindows(a, now)
}

const probablyResetText = "probably reset"

func resetWindows(a *Account, now time.Time) {
	a.Windows = append([]Window{}, a.Windows...)
	for i, w := range a.Windows {
		reset, err := time.Parse(time.RFC3339Nano, w.ResetsAt)
		if err == nil && !now.Before(reset) && a.ObservedAt.Before(reset) {
			a.Windows[i].UsedPercent = nil
			a.Windows[i].Used = nil
			a.Windows[i].Reason = accountstore.WindowReasonProbableReset
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
			a.Stale = a.Stale || a.ObservedAt.IsZero() || now.Sub(a.ObservedAt) >= ttl
			resetWindows(a, now)
		}
		applyStatedFailoverLimits(out.Accounts, s.o.Store, now)
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
	applyStatedFailoverLimits(out.Accounts, store, now)
	ApplyBlockingQuota(out.Accounts, now)
	return out
}

// applyStatedFailoverLimits reflects a z.ai 1308 response in the saved account
// view. A newer provider observation replaces the failure-derived reading.
func applyStatedFailoverLimits(accounts []Account, store *accountstore.Store, now time.Time) {
	if store == nil {
		return
	}
	scopes, err := reposcope.Open(filepath.Dir(store.Root))
	if err != nil {
		return
	}
	resets, err := scopes.Stated(now)
	if err != nil || len(resets) == 0 {
		return
	}
	recorded, err := scopes.LimitRecordedAt(now)
	if err != nil {
		return
	}
	spent := 100.0
	for i := range accounts {
		a := &accounts[i]
		reset, ok := resets[a.ID]
		if a.Provider != accountstore.ProviderZAI || !ok || a.ObservedAt.After(recorded[a.ID]) {
			continue
		}
		window := -1
		for j := range a.Windows {
			if a.Windows[j].ID == "five_hour" {
				window = j
				break
			}
		}
		if window < 0 {
			a.Windows = append(a.Windows, Window{ID: "five_hour", Label: "5-hour"})
			window = len(a.Windows) - 1
		}
		w := &a.Windows[window]
		w.UsedPercent, w.Used, w.Limit = &spent, nil, nil
		w.Reason, w.Unit = "", "percent"
		w.ResetsAt = reset.UTC().Format(time.RFC3339)
	}
}
