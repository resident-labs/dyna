package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	accountnative "dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

// CredentialRefreshResult contains status only. It never exposes credentials.
type CredentialRefreshResult struct {
	Supported bool      `json:"supported"`
	Refreshed bool      `json:"refreshed"`
	Active    bool      `json:"active"`
	Due       bool      `json:"due"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// CredentialStatus reads expiry and support without a switch gate or provider
// request. Due also covers a saved rotation that has not reached native state.
func (a *Accounts) CredentialStatus(ctx context.Context, id, executable string) (result CredentialRefreshResult, err error) {
	err = a.transaction(func(state *store.State) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := state.Find(id)
		n, err := a.native(entry)
		if err != nil {
			return err
		}
		if err = checkAccountFiles(n.Harness, n.Home, false); err != nil {
			return err
		}
		c, err := readAccountLogin(n.Home)
		if err != nil {
			return err
		}
		if c.Identity != entry.Identity || c.Integration != entry.Integration {
			return accountnative.ErrNoIdentity
		}
		m, native, err := a.refreshManager(n, executable)
		if err != nil {
			return err
		}
		result.Active, result.Supported = n.Active, accountnative.SupportsRefresh(m, c)
		result.ExpiresAt = accountnative.CredentialExpiry(c.Credential)
		if native {
			if _, ok := m.(accountnative.ActiveRefresher); !ok {
				result.Supported = false
			}
			// Native readers can fail during an external login. Let the gated
			// operation retry rather than assume that a saved token is active.
			rows, captureErr := m.Capture(ctx, "")
			actual := capturedFor(rows, entry.Integration)
			if captureErr != nil || actual == nil || actual.Identity != c.Identity {
				result.Due = true
			} else if accountnative.NewerCredential(c, *actual) {
				result.Due = true
			} else {
				result.ExpiresAt = accountnative.CredentialExpiry(actual.Credential)
			}
		}
		result.Due = result.Due || entry.NeedsLogin || result.ExpiresAt.IsZero() || time.Until(result.ExpiresAt) <= 5*time.Minute
		if !result.Supported {
			result.Due = false
			result.Reason = "This login cannot be refreshed automatically."
		}
		return ctx.Err()
	}, false)
	return result, err
}

// RefreshCredentials renews a private login without changing its selection.
//
// A login that lives in the account's own home is renewed there, under the
// account lock and the harness's own credential lock, and it never waits for the
// harness's turns: Pi renews that same file during a turn, so a refresh and a
// turn already order their writes with Pi's lock. A login in a shared harness
// home is different, because its renewal writes a file every turn of that
// harness reads, so it takes the switch gate and waits for those turns to end.
func (a *Accounts) RefreshCredentials(ctx context.Context, id, executable string) (result CredentialRefreshResult, err error) {
	if err = ctx.Err(); err != nil {
		return result, err
	}
	result, err = a.CredentialStatus(ctx, id, executable)
	if err != nil {
		return result, err
	}
	if !result.Supported {
		return result, accountnative.ErrUnsupported
	}
	if !result.Due {
		return result, nil
	}
	n, err := a.Find(id)
	if err != nil {
		return result, err
	}
	var attemptedAt time.Time
	defer func() {
		if attemptedAt.IsZero() {
			return
		}
		outcome, reason := renewalOutcome(result, err)
		recordErr := (&store.Store{Root: a.Root, Quiet: a.Quiet}).RecordRenewal(context.Background(), store.Renewal{
			AccountID: n.ID, Harness: n.Harness, Provider: n.Provider,
			AttemptedAt: attemptedAt, Result: outcome, Reason: reason,
		})
		if recordErr != nil {
			err = errors.Join(err, recordErr)
		}
	}()
	if !pinsCredentials(n.Harness) && !ownHomeLogin(n) {
		release, err := a.switchGate(ctx, n.Harness)
		if err != nil {
			return result, err
		}
		defer release()
	}
	// Use the same path checks as Accounts.transaction before opening the store.
	for _, path := range []string{a.Root, filepath.Join(a.Root, "accounts.lock"), filepath.Join(a.Root, ".refresh-locks")} {
		if err = safeAccountPath(path); err != nil {
			return result, err
		}
	}
	s := &store.Store{Root: a.Root, Quiet: a.Quiet}
	err = s.Transaction(func(state *store.State, checkpoint func() error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := state.Find(id)
		current, err := a.native(entry)
		if err != nil {
			return err
		}
		if current.Harness != n.Harness {
			return errAccountPath
		}
		result.Active = current.Active
		if err = checkAccountFiles(current.Harness, current.Home, false); err != nil {
			return err
		}
		home, err := a.HarnessHome(current.Harness)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(home, 0700); err != nil {
			return err
		}
		if err = checkAccountFiles(current.Harness, home, true); err != nil {
			return err
		}
		manager, native, err := a.refreshManager(current, executable)
		if err != nil {
			return err
		}
		return s.WithAccountLock(ctx, current.Harness, entry.Integration, entry.Identity, func() error {
			c, err := readAccountLogin(current.Home)
			if err != nil {
				return err
			}
			valid := func(next accountnative.Captured) bool {
				return next.Identity == entry.Identity && next.Integration == entry.Integration && next.Identity != ""
			}
			if !valid(c) {
				return accountnative.ErrNoIdentity
			}
			expiry := func(c accountnative.Captured) time.Time {
				if reader, ok := manager.(accountnative.CredentialExpirer); ok {
					v, _ := reader.CredentialExpiry(c)
					return v
				}
				return accountnative.CredentialExpiry(c.Credential)
			}
			result.ExpiresAt = expiry(c)
			result.Due = entry.NeedsLogin || result.ExpiresAt.IsZero() || time.Until(result.ExpiresAt) <= 5*time.Minute
			result.Supported = accountnative.SupportsRefresh(manager, c)
			activeRefresher, activeOK := manager.(accountnative.ActiveRefresher)
			if native && !activeOK {
				result.Supported = false
			}
			save := func(next accountnative.Captured) error {
				if !valid(next) {
					return accountnative.ErrNoIdentity
				}
				// Capture prefers a pending login. Keep it current until Switch
				// completes that login and removes the pending snapshot.
				pending := filepath.Join(current.Home, ".login-pending.json")
				if _, err := os.Lstat(pending); err == nil {
					if err = writePrivateAccountJSON(pending, next); err != nil {
						return err
					}
				} else if !os.IsNotExist(err) {
					return errAccountPath
				}
				if err := writeAccountLogin(current.Home, next); err != nil {
					return err
				}
				entry.NeedsLogin = false
				entry.UpdatedAt = time.Now().UTC()
				result.ExpiresAt = expiry(next)
				result.Due = result.ExpiresAt.IsZero() || time.Until(result.ExpiresAt) <= 5*time.Minute
				return checkpoint()
			}
			recover := func() (bool, error) {
				r, ok := manager.(accountnative.CredentialRecoverer)
				if !ok {
					return false, nil
				}
				next, err := r.RecoverCredential(ctx, c)
				if errors.Is(err, accountnative.ErrNotSignedIn) || errors.Is(err, accountnative.ErrUnsupported) {
					return false, nil
				}
				if err != nil {
					return false, err
				}
				if err = save(next); err != nil {
					return false, err
				}
				c = next
				return true, nil
			}
			repairNative := false
			if native {
				rows, err := manager.Capture(ctx, "")
				if err != nil {
					return err
				}
				actual := capturedFor(rows, entry.Integration)
				if actual == nil || !valid(*actual) {
					return accountnative.ErrNoIdentity
				}
				// Preserve a newer checkpoint left by a failed native write.
				repairNative = accountnative.NewerCredential(c, *actual)
				if !repairNative {
					c = *actual
					if err = save(c); err != nil {
						return err
					}
				}
			} else if _, err = recover(); err != nil {
				return err
			}
			result.ExpiresAt = expiry(c)
			result.Supported = accountnative.SupportsRefresh(manager, c) && (!native || activeOK)
			if !result.Supported {
				result.Reason = "This login cannot be refreshed automatically."
				return accountnative.ErrUnsupported
			}
			if !repairNative && !result.ExpiresAt.IsZero() && time.Until(result.ExpiresAt) > 5*time.Minute && !entry.NeedsLogin {
				return nil
			}
			refresh := func() (accountnative.Captured, error) {
				if !accountnative.SupportsRefresh(manager, c) {
					return c, accountnative.ErrUnsupported
				}
				if native {
					return activeRefresher.RefreshActive(ctx, c, save)
				}
				return manager.Refresh(ctx, c)
			}
			attemptedAt = time.Now().UTC()
			next, err := refresh()
			if errors.Is(err, accountnative.ErrInvalidGrant) {
				recovered, recoveryErr := recover()
				if recoveryErr != nil {
					return recoveryErr
				}
				if recovered {
					next, err = refresh()
				}
			}
			if errors.Is(err, accountnative.ErrInvalidGrant) {
				entry.NeedsLogin = true
				entry.UpdatedAt = time.Now().UTC()
				result.Reason = "This login expired. Sign in again."
				return errors.Join(err, checkpoint())
			}
			if errors.Is(err, accountnative.ErrUnsupported) {
				result.Supported = false
				result.Reason = "This login cannot be refreshed automatically."
			}
			if err != nil {
				return err
			}
			if err = save(next); err != nil {
				return err
			}
			result.Refreshed = true
			return nil
		})
	})
	return result, err
}

// renewalOutcome maps provider and local errors to public, non-secret facts.
// The reason names the exact error class so a stored record shows which path
// failed; every reason is a fixed label, never provider or reader text.
func renewalOutcome(result CredentialRefreshResult, err error) (store.RenewalResult, string) {
	switch {
	case errors.Is(err, accountnative.ErrInvalidGrant):
		return store.RenewalResultInvalidGrant, "invalid_grant"
	case errors.Is(err, accountnative.ErrNotSignedIn):
		return store.RenewalResultNoCredential, "not signed in"
	case errors.Is(err, accountnative.ErrNoIdentity):
		return store.RenewalResultNoCredential, "identity mismatch"
	case errors.Is(err, accountnative.ErrUnsupported):
		return store.RenewalResultNoCredential, "unsupported"
	case errors.Is(err, os.ErrNotExist):
		return store.RenewalResultNoCredential, "login file missing"
	case errors.Is(err, context.DeadlineExceeded):
		return store.RenewalResultTransient, "deadline exceeded"
	case errors.Is(err, context.Canceled):
		return store.RenewalResultTransient, "canceled"
	case err != nil:
		return store.RenewalResultTransient, "transient"
	case result.Refreshed:
		return store.RenewalResultRenewed, ""
	default:
		return store.RenewalResultTransient, "no renewed credential"
	}
}

// ownHomeLogin reports a login that its account holds in its own home, which a
// refresh rewrites in place: a Pi account's auth.json. A Pi turn reads that same
// file, and Pi renews it there under its own lock, so such a refresh needs no
// drain of the harness's turns.
func ownHomeLogin(n NativeAccount) bool {
	if n.Harness != "pi" {
		return false
	}
	_, err := os.Lstat(filepath.Join(n.Home, "auth.json"))
	return err == nil
}

// refreshManager returns the manager for the account's native login. native
// reports whether that login lives in native files: the active account's
// shared home, or a pinned account's own folder.
func (a *Accounts) refreshManager(n NativeAccount, executable string) (accountnative.Manager, bool, error) {
	// A Pi account keeps its own login file, one provider entry per account, so a
	// refresh works in that account's home and never in the shared Pi home. An
	// inactive account is renewed there too, because that is where its login is.
	if n.Harness == "pi" {
		// Only an account that holds its own login file is renewed there; one
		// without it has no grant to renew in this home.
		if _, statErr := os.Lstat(filepath.Join(n.Home, "auth.json")); statErr == nil {
			m, _, err := a.scopedManager(n.Harness, n.Home, executable)
			return m, true, err
		}
	}
	if pinnedLogin(n) {
		m, err := a.pinManager(n, executable)
		return m, true, err
	}
	home, err := a.HarnessHome(n.Harness)
	if err != nil {
		return nil, false, err
	}
	m, _, err := a.scopedManager(n.Harness, home, executable)
	return m, n.Active && !pinsCredentials(n.Harness), err
}
