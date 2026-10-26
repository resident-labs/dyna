package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

// nativeHomeAccounts reads the same private native homes as the launcher. It
// never changes the owner's own sign-in. A storage error comes back with the
// rows that could be read, and a saved row it could not read comes back as a
// problem about that row only.
func (s *Service) nativeHomeAccounts(ctx context.Context, explicitRefresh bool) ([]Account, []runtime.AccountProblem, error) {
	out := []Account{}
	if s.o.Store == nil {
		return out, []runtime.AccountProblem{}, nil
	}
	accounts := &runtime.Accounts{Root: s.o.Store.Root}
	rows, problems, err := accounts.ListWithProblems()
	if err != nil {
		return out, problems, err
	}
	rules := reposcope.Rules{}
	if scopes, scopeErr := reposcope.Open(filepath.Dir(s.o.Store.Root)); scopeErr == nil {
		rules, _ = scopes.Rules()
	}
	bundles := &runtime.BundleManager{Root: filepath.Join(filepath.Dir(s.o.Store.Root), "harnesses")}
	state, _ := s.o.Store.Read()
	saved := map[string]store.Entry{}
	for _, entry := range state.Entries {
		saved[entry.ID] = entry
	}
	var saveErr error
	for _, n := range rows {
		a := Account{Active: n.Active, ID: n.ID, Harness: n.Harness, Provider: n.Provider, Kind: n.Kind, Label: n.Label, Status: n.Status, Source: "Dyna private native account", ObservedAt: time.Now().UTC(), Profiles: []string{}, Windows: []Window{}, Repos: append([]string{}, rules[n.ID]...)}
		a.NeedsLogin = n.Status == "not signed in"
		if entry, ok := saved[n.ID]; ok {
			applySavedObservation(&a, entry, s.o.Now())
		}
		if !n.Active {
			if m, err := accounts.SavedManager(n.ID, ""); err == nil {
				if captured, err := m.Capture(ctx, ""); err == nil && len(captured) > 0 {
					setCredentialExpiry(&a, captured[0])
					if explicitRefresh {
						prober, _ := m.(native.SavedProber)
						saveErr = errors.Join(saveErr, s.refreshNativeUsage(ctx, &a, prober, captured[0]))
					}
				}
			}
			out = append(out, a)
			continue
		}
		b, err := bundles.Resolve(n.Harness, "")
		if err != nil && !runtime.RunsAsHarness(n.Harness) {
			// A saved file-based login of a retired harness still refreshes
			// and reports usage; it needs no bundle or executable.
			b = runtime.Bundle{Harness: n.Harness}
			err = nil
		}
		if err != nil {
			a.Status = "unavailable"
			out = append(out, a)
			continue
		}
		var refreshErr error
		if explicitRefresh {
			_, refreshErr = accounts.RefreshCredentials(ctx, n.ID, b.Executable)
			if errors.Is(refreshErr, native.ErrUnsupported) {
				refreshErr = nil
			}
		}
		m, err := accounts.Manager(n.ID, b.Executable)
		if err != nil {
			a.Status = "unavailable"
			out = append(out, a)
			continue
		}
		capture, err := m.Capture(ctx, "")
		if err != nil || len(capture) == 0 {
			a.Status = "unavailable"
			a.NeedsLogin = err == nil || runtime.NativeLoginNeedsSignIn(err)
			if a.NeedsLogin {
				a.Status = "needs_auth"
			}
			out = append(out, a)
			continue
		}
		if n.Identity != "" && capture[0].Identity != n.Identity {
			a.Status = "needs_auth"
			a.NeedsLogin = true
			out = append(out, a)
			continue
		}
		a.Status = "ready"
		setCredentialExpiry(&a, capture[0])
		if explicitRefresh {
			if refreshErr != nil {
				saveErr = errors.Join(saveErr, s.saveNativeUsageObservation(&a, capture[0], store.Observation{}, refreshErr))
			} else {
				prober, _ := m.(native.SavedProber)
				saveErr = errors.Join(saveErr, s.refreshNativeUsage(ctx, &a, prober, capture[0]))
			}
		}
		out = append(out, a)
	}
	return out, problems, saveErr
}

// Credential checks wake at each login's refresh time instead of every
// minute. A login is refreshed this long before it expires (the window
// runtime.CredentialStatus uses).
const credentialLead = 5 * time.Minute

// credentialRetry is how soon a login is looked at again after a refresh, a
// failure, or when its expiry is unknown.
const credentialRetry = time.Minute

// credentialRecheck is the latest next check, since an account can be added
// without telling the engine.
// TODO: legacy/shim remove later — a ceiling only because a CLI can add or change an account without telling the engine; remove when item 2 moves account writes into the engine, which can then arm the timer.
const credentialRecheck = 10 * time.Minute

// RefreshCredentials renews private credentials that are due, without quota
// requests, and returns when to look again: just before the earliest known
// refresh time, and never later than credentialRecheck. Independent hosts
// coalesce through registry and account locks.
func (s *Service) RefreshCredentials(ctx context.Context, now time.Time) time.Time {
	next := now.Add(credentialRecheck)
	if s.o.Store == nil || ctx.Err() != nil {
		return next
	}
	// Account locks serialize renewal; provider I/O must not hold the service
	// lock used by model and usage reads.
	renewed := false
	defer func() {
		if renewed {
			s.clearUsageCache()
		}
	}()
	accounts := &runtime.Accounts{Root: s.o.Store.Root, Quiet: true}
	rows, err := accounts.List()
	if err != nil {
		return now.Add(credentialRetry)
	}
	bundles := &runtime.BundleManager{Root: filepath.Join(filepath.Dir(s.o.Store.Root), "harnesses")}
	for _, n := range rows {
		if ctx.Err() != nil {
			return next
		}
		b, err := bundles.Resolve(n.Harness, "")
		if err != nil && !runtime.RunsAsHarness(n.Harness) {
			// A saved login of a retired harness still renews its own token.
			b = runtime.Bundle{Harness: n.Harness}
			err = nil
		}
		if err != nil {
			continue
		}
		refreshCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		res, err := accounts.RefreshCredentials(refreshCtx, n.ID, b.Executable)
		cancel()
		renewed = renewed || res.Refreshed
		if at := credentialCheckAt(now, res, err); !at.IsZero() && at.Before(next) {
			next = at
		}
	}
	return next
}

// credentialCheckAt is when one login needs its next look, or zero when it
// cannot be refreshed automatically.
func credentialCheckAt(now time.Time, res runtime.CredentialRefreshResult, err error) time.Time {
	switch {
	case errors.Is(err, native.ErrUnsupported) || (err == nil && !res.Supported):
		return time.Time{}
	case err != nil || res.Refreshed || res.Due || res.ExpiresAt.IsZero():
		return now.Add(credentialRetry)
	}
	// A refresh time that has passed while the runtime does not yet call the
	// login due (a boundary or clock skew) waits like a due login, rather
	// than arming a timer in the past.
	if at := res.ExpiresAt.Add(-credentialLead); at.After(now) {
		return at
	}
	return now.Add(credentialRetry)
}

func setCredentialExpiry(a *Account, c native.Captured) {
	m, _ := runtime.NativeRuntimeManager(a.Harness, native.Host{})
	setCredentialExpiryForManager(a, c, m)
}

func setCredentialExpiryForManager(a *Account, c native.Captured, m native.Manager) {
	a.RefreshMessage = ""
	a.ExpiresAt = nil
	expiry := native.CredentialExpiry(c.Credential)
	if expiry.IsZero() {
		expiry = c.ExpiresAt
	}
	if !expiry.IsZero() {
		a.ExpiresAt = &expiry
	}
	if native.CredentialUsesAPIKey(c) {
		a.RefreshMessage = "API key · no expiry"
		return
	}
	if m == nil || !native.SupportsRefresh(m, c) {
		expired := native.CredentialExpired(c, time.Now())
		if expired {
			a.NeedsLogin = true
			a.Status = "needs_auth"
		}
		a.RefreshMessage = "This login cannot be refreshed automatically."
		if expired {
			a.RefreshMessage += " This login has expired. Sign in again."

		} else if !expiry.IsZero() {
			a.RefreshMessage += " Saved access token expiry: " + expiry.UTC().Format(time.RFC3339) + "."
		} else {
			a.RefreshMessage += " Its expiry is not available; sign in again when the harness requests it."
		}
	}
}

// refreshNativeUsage owns provider observation updates for both browser and CLI refreshes.
func (s *Service) refreshNativeUsage(ctx context.Context, a *Account, prober native.SavedProber, captured native.Captured) error {
	// Snapshot serializes browser and CLI refreshes. Persisted attempt times also
	// preserve the interval when the engine restarts.
	state, err := s.o.Store.Read()
	if err != nil {
		return err
	}
	if entry := state.Find(a.ID); entry != nil && entry.Identity == captured.Identity && entry.Observation != nil {
		next := usageProbeAt(entry.Observation, a.Active)
		if s.o.Now().Before(next) {
			applySavedObservation(a, *entry, s.o.Now())
			return nil
		}
	}
	var observation store.Observation
	probeErr := fmt.Errorf("Usage is not available for %s: this harness has no saved usage probe.", a.Harness)
	if prober != nil {
		observation, probeErr = prober.ProbeSaved(ctx, captured)
		if probeErr == nil && len(observation.Windows) == 0 {
			probeErr = errors.New("The provider returned no usage windows.")
		}
	}
	return s.saveNativeUsageObservation(a, captured, observation, probeErr)
}

// Both credential refresh and usage probe failures use the same saved result.
func (s *Service) saveNativeUsageObservation(a *Account, captured native.Captured, observation store.Observation, probeErr error) error {
	observation, probeErr = guardUsageResult(observation, probeErr, captured)
	if errors.Is(probeErr, native.ErrUsageIdle) {
		// Idle mint responses omit quota. Keep its real observation time and values.
		return s.o.Store.Update(func(state *store.State) error {
			entry := state.Find(a.ID)
			if entry == nil || entry.Identity != captured.Identity {
				return errors.New("account changed during usage refresh")
			}
			if entry.Observation == nil {
				entry.Observation = &store.Observation{}
			}
			saved := entry.Observation
			if observation.Plan != nil {
				saved.Plan = observation.Plan
			}
			if len(saved.Windows) == 0 {
				saved.Windows = []store.Window{{ID: "subscription", Label: "Session (5-hour)", Reason: store.WindowReasonNotReported}}
			}
			for i := range saved.Windows {
				if saved.Windows[i].ID == "subscription" {
					saved.Windows[i].Label = strings.TrimSuffix(saved.Windows[i].Label, " (idle)") + " (idle)"
				}
			}
			saved.UsageError = ""
			saved.Reason = ""
			saved.Unavailable = false
			saved.Failures = 0
			saved.LastAttemptAt = s.o.Now().UTC()
			a.Status = "ready"
			applySavedObservation(a, *entry, s.o.Now().UTC())
			return nil
		})
	}

	applyNativeUsageObservation(a, observation, probeErr)
	if err := s.o.Store.Update(func(state *store.State) error {
		entry := state.Find(a.ID)
		if entry == nil || entry.Identity != captured.Identity {
			return errors.New("account changed during usage refresh")
		}
		if probeErr != nil {
			observation = store.Observation{Windows: []store.Window{}}
			if entry.Observation != nil {
				observation = *entry.Observation
			}
			observation.UsageError = a.UsageError
			observation.Reason = usageReason(probeErr)
			observation.Failures++
			observation.Unavailable = true
			if errors.Is(probeErr, native.ErrInvalidGrant) || isReaderSecret(probeErr) {
				observation.Windows, observation.Plan = []store.Window{}, nil
			}
		} else {
			observation.UsageError = ""
			observation.Reason = ""
			observation.RateLimitedUntil = nil
		}
		observation.LastAttemptAt = s.o.Now().UTC()
		var quota *native.QuotaError
		if errors.As(probeErr, &quota) && quota.Status == http.StatusTooManyRequests {
			next := observation.LastAttemptAt.Add(max(quota.RetryAfter, activeUsageTTL))
			observation.RateLimitedUntil = &next
			observation.Reason = store.UsageReasonRateLimited
			observation.UsageError = "rate limited"
			if !observation.ObservedAt.IsZero() {
				observation.UsageError += "; showing the reading from " + observation.ObservedAt.UTC().Format(time.RFC3339)
			} else {
				observation.UsageError += "; no previous reading"
			}
		} else if probeErr != nil {
			observation.RateLimitedUntil = nil
		}
		entry.Observation = &observation
		applySavedObservation(a, *entry, s.o.Now())
		return nil
	}); err != nil {
		return fmt.Errorf("save usage observation: %w", err)
	}
	return nil
}

// Apply only after native capture has proved the saved account identity.
func applyNativeUsageObservation(a *Account, observation store.Observation, err error) {
	if err == nil {
		a.UsageError, a.UsageReason = "", ""
		a.Status, a.NeedsLogin, a.Stale = "ready", false, false
		a.Plan, a.Windows, a.ObservedAt = observation.Plan, observation.Windows, observation.ObservedAt
	} else {
		a.UsageError, a.Stale = err.Error(), true
		a.UsageReason = usageReason(err)
		if isReaderSecret(err) {
			a.Windows, a.Plan = []Window{}, nil
		}
		if errors.Is(err, native.ErrInvalidGrant) {
			a.Status, a.NeedsLogin = "needs_auth", true
			a.Windows, a.Plan = []Window{}, nil
		}
	}
}

// usageReason classifies a failed usage reading for the account view.
func usageReason(err error) store.UsageReason {
	var unavailable *native.UsageUnavailableError
	if errors.As(err, &unavailable) {
		return unavailable.Reason
	}
	var quota *native.QuotaError
	if errors.As(err, &quota) {
		if quota.Status == http.StatusTooManyRequests {
			return store.UsageReasonRateLimited
		}
		return store.UsageReasonProviderError
	}
	if errors.Is(err, native.ErrInvalidGrant) {
		return store.UsageReasonExpired
	}
	if errors.Is(err, native.ErrUnsupported) {
		return store.UsageReasonNotReadable
	}
	return store.UsageReasonProviderError
}
