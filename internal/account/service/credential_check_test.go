package service

import (
	"errors"
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/harness/runtime"
)

// Each login is looked at again just before it is due, sooner when its state
// is unsettled, and never when it cannot be refreshed.
func TestCredentialCheckAt(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	expires := now.Add(2 * time.Hour)
	for _, tc := range []struct {
		name string
		res  runtime.CredentialRefreshResult
		err  error
		want time.Time
	}{
		{"known expiry", runtime.CredentialRefreshResult{Supported: true, ExpiresAt: expires}, nil, expires.Add(-credentialLead)},
		{"just refreshed", runtime.CredentialRefreshResult{Supported: true, Refreshed: true, ExpiresAt: expires}, nil, now.Add(credentialRetry)},
		{"due", runtime.CredentialRefreshResult{Supported: true, Due: true}, nil, now.Add(credentialRetry)},
		{"unknown expiry", runtime.CredentialRefreshResult{Supported: true}, nil, now.Add(credentialRetry)},
		{"failed", runtime.CredentialRefreshResult{}, errors.New("provider down"), now.Add(credentialRetry)},
		{"refresh time passed, not due yet", runtime.CredentialRefreshResult{Supported: true, ExpiresAt: now.Add(credentialLead - time.Second)}, nil, now.Add(credentialRetry)},
		{"cannot refresh", runtime.CredentialRefreshResult{Supported: false}, nil, time.Time{}},
		{"unsupported error", runtime.CredentialRefreshResult{}, native.ErrUnsupported, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := credentialCheckAt(now, tc.res, tc.err); !got.Equal(tc.want) {
				t.Fatalf("credentialCheckAt = %v, want %v", got, tc.want)
			}
		})
	}
}
