package auth

import (
	"net/http"
	"strings"
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
func (m *Manager) dynaRecordQuotaRefusal(auth *Auth, err *Error, now time.Time) bool {
	if !dynaQuotaRefusal(auth, err) {
		return false
	}
	m.quotaBoard.startRefresh(strings.ToLower(strings.TrimSpace(auth.Provider)), auth.ID, now)
	return true
}
