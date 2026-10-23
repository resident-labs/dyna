package service

import (
	"context"
	"path/filepath"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

const inactiveUsageTTL = 30 * time.Minute

// activeUsageRepoll re-reads the active account's usage while its turns run,
// so failover always ranks a current reading.
const activeUsageRepoll = 5 * time.Minute

// clearUsageCache drops the cached snapshot after a timer probe persisted a
// new reading, without holding the service lock during provider I/O.
func (s *Service) clearUsageCache() {
	s.mu.Lock()
	s.last = Snapshot{}
	s.mu.Unlock()
}

// usageProbeAt honors both the normal interval and provider backoff. Inactive
// accounts get one earlier attempt after each reset that has not been checked.
func usageProbeAt(o *store.Observation, active bool) time.Time {
	if o == nil {
		return time.Time{}
	}
	attempted := o.LastAttemptAt
	if attempted.IsZero() {
		attempted = o.ObservedAt
	}
	ttl := inactiveUsageTTL
	if active {
		ttl = activeUsageTTL
	}
	next := attempted.Add(ttl)
	if !active {
		for _, w := range o.Windows {
			reset, err := time.Parse(time.RFC3339Nano, w.ResetsAt)
			if err == nil && reset.After(o.ObservedAt) && reset.After(attempted) && reset.Before(next) {
				next = reset
			}
		}
	}
	if o.RateLimitedUntil != nil && o.RateLimitedUntil.After(next) {
		next = *o.RateLimitedUntil
	}
	return next
}

// RefreshInactiveUsage probes saved grants without activating their accounts
// or changing native files. The active account of a harness with running
// turns is re-polled at activeUsageRepoll so its reading stays fresh.
// Persisted attempt times survive engine restarts.
func (s *Service) RefreshInactiveUsage(ctx context.Context, now time.Time) time.Time {
	next := now.Add(inactiveUsageTTL)
	if s.o.Store == nil || ctx.Err() != nil {
		return next
	}
	accounts := &runtime.Accounts{Root: s.o.Store.Root}
	rows, err := accounts.List()
	if err != nil {
		return now.Add(credentialRetry)
	}
	state, err := s.o.Store.Read()
	if err != nil {
		return now.Add(credentialRetry)
	}
	for _, n := range rows {
		if ctx.Err() != nil {
			return next
		}
		entry := state.Find(n.ID)
		if n.Status == "not signed in" || entry == nil {
			continue
		}
		if n.Active {
			// The reading only matters while turns run; an idle harness keeps
			// whatever reading it had.
			if _, running := accounts.SwitchStatus(n.Harness); running == 0 {
				continue
			}
			at := activeUsageDue(entry.Observation, now)
			if !at.After(now) {
				at = s.probeActiveUsage(ctx, accounts, n, now)
			}
			if !at.After(now) {
				at = now.Add(credentialRetry)
			}
			if at.Before(next) {
				next = at
			}
			continue
		}
		at := usageProbeAt(entry.Observation, false)
		if !at.After(now) {
			at = s.probeInactiveUsage(ctx, accounts, n, now)
		}
		if !at.After(now) {
			at = now.Add(credentialRetry)
		}
		if at.Before(next) {
			next = at
		}
	}
	return next
}

// activeUsageDue is when the active account's reading is read again. A
// recorded rate limit delays the next attempt past its reset.
func activeUsageDue(o *store.Observation, now time.Time) time.Time {
	if o == nil {
		return time.Time{}
	}
	attempted := o.LastAttemptAt
	if attempted.IsZero() {
		attempted = o.ObservedAt
	}
	next := attempted.Add(activeUsageRepoll)
	if o.RateLimitedUntil != nil && o.RateLimitedUntil.After(next) {
		next = *o.RateLimitedUntil
	}
	return next
}

// probeActiveUsage reads the active account's usage through its live native
// home, whose tokens the running CLI keeps current. It never changes native
// files or the active selection.
func (s *Service) probeActiveUsage(ctx context.Context, accounts *runtime.Accounts, n runtime.NativeAccount, now time.Time) time.Time {
	retry := now.Add(credentialRetry)
	bundles := &runtime.BundleManager{Root: filepath.Join(filepath.Dir(s.o.Store.Root), "harnesses")}
	b, err := bundles.Resolve(n.Harness, "")
	if err != nil {
		return retry
	}
	m, err := accounts.Manager(n.ID, b.Executable)
	if err != nil {
		return retry
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	captured, err := m.Capture(ctx, "")
	if err != nil || len(captured) == 0 {
		return retry
	}
	row := Account{ID: n.ID, Harness: n.Harness, Status: "ready", Active: true}
	prober, _ := m.(native.SavedProber)
	err = s.refreshNativeUsage(ctx, &row, prober, captured[0])
	// Provider I/O must not hold the service lock used by model and usage reads.
	// The next cached read must include this persisted result.
	s.clearUsageCache()
	if err != nil {
		return retry
	}
	state, err := s.o.Store.Read()
	if err != nil {
		return retry
	}
	if saved := state.Find(n.ID); saved != nil {
		return activeUsageDue(saved.Observation, now)
	}
	return retry
}

func (s *Service) probeInactiveUsage(ctx context.Context, accounts *runtime.Accounts, n runtime.NativeAccount, now time.Time) time.Time {
	retry := now.Add(credentialRetry)
	m, err := accounts.SavedManager(n.ID, "")
	if err != nil {
		return retry
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	captured, err := m.Capture(ctx, "")
	if err != nil || len(captured) == 0 {
		return retry
	}
	row := Account{ID: n.ID, Harness: n.Harness, Status: "ready"}
	prober, _ := m.(native.SavedProber)
	err = s.refreshNativeUsage(ctx, &row, prober, captured[0])
	// Provider I/O must not hold the service lock used by model and usage reads.
	// The next cached read must include this persisted result.
	s.clearUsageCache()
	if err != nil {
		return retry
	}
	state, err := s.o.Store.Read()
	if err != nil {
		return retry
	}
	if saved := state.Find(n.ID); saved != nil {
		return usageProbeAt(saved.Observation, false)
	}
	return retry
}
