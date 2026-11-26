package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// DynaRequestQuotaOnly marks a managed credential with a usage reader. Quota
// refusals for that credential affect this request only; other cooling stays on.
const DynaRequestQuotaOnly = "dyna_request_quota_only"

func dynaQuotaRefusal(auth *Auth, err *Error) bool {
	if auth.Metadata[DynaRequestQuotaOnly] != true || err == nil || err.Code == ErrorCodeForceCooldown || shouldSkipCredentialCooldown(err) || isCloudflareChallengeResultError(err) {
		return false
	}
	switch statusCodeFromResult(err) {
	case http.StatusTooManyRequests:
		return !isTransientRateLimitResultError(err)
	case http.StatusPaymentRequired:
		return err.Code == ErrorCodeInsufficientBalance || strings.Contains(strings.ToLower(err.Message), "insufficient balance")
	case http.StatusBadRequest:
		return isCreditBalanceTooLowResultError(err)
	default:
		return false
	}
}

// dynaRecordQuotaRefusal asks for a reading without recording a denial or
// changing a previous non-quota block (e.g. a concurrent 403).
func (m *Manager) dynaRecordQuotaRefusal(ctx context.Context, auth *Auth, err *Error, now time.Time) bool {
	quota := dynaQuotaRefusal(auth, err)
	if attempts, ok := ctx.Value(dynaQuotaAttemptsKey{}).(*atomic.Uint32); ok {
		if quota {
			attempts.Or(dynaAttemptQuota)
		} else {
			attempts.Or(dynaAttemptOther)
		}
	}
	if quota {
		m.quotaBoard.requestRefusalRefresh(auth, now)
	}
	return quota
}

type dynaQuotaAttemptsKey struct{}

const (
	dynaAttemptQuota = 1 << iota
	dynaAttemptOther
)

func withDynaQuotaAttempts(ctx context.Context) context.Context {
	if ctx.Value(dynaEligibleContextKey{}) == nil {
		return ctx
	}
	return context.WithValue(ctx, dynaQuotaAttemptsKey{}, new(atomic.Uint32))
}

// dynaTerminalQuota marks only a terminal failure from a pool that reported
// quota refusals, not one with an independent transient failure to retry.
func dynaTerminalQuota(ctx context.Context, err error) error {
	attempts, ok := ctx.Value(dynaQuotaAttemptsKey{}).(*atomic.Uint32)
	if !ok || attempts.Load() != dynaAttemptQuota {
		return err
	}
	switch statusCodeFromError(err) {
	case http.StatusTooManyRequests, http.StatusPaymentRequired, http.StatusBadRequest:
		return &dynaQuotaExhaustedError{cause: err}
	}
	return err
}

type dynaQuotaExhaustedError struct{ cause error }

func (e *dynaQuotaExhaustedError) Error() string {
	return "Quota exceeded: " + ExtractUpstreamErrorSummary(e.cause.Error())
}

func (e *dynaQuotaExhaustedError) Unwrap() error { return e.cause }

// IsDynaTerminalQuota reports a quota failure that should wake at its reset
// rather than immediately replaying the whole pool through client retries.
func IsDynaTerminalQuota(err error) bool {
	var exhausted *dynaQuotaExhaustedError
	var spent *dynaQuotaSpentError
	return errors.As(err, &exhausted) || errors.As(err, &spent)
}
