package service

import (
	"errors"
	"regexp"
	"strings"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

// readerSecretDetail is the typed result of a usage reader that copied the
// account's credential, or a value that looks like one, into its output.
const readerSecretDetail = "Reader output rejected: contained a secret."

// secretLike matches the shapes of API keys and tokens. A usage window may
// name a plan or a period; it never needs a high-entropy token.
var secretLike = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:sk|pk|rk|xai|aiza)[-_][A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{16,}`),
	regexp.MustCompile(`\b[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
	regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`),
	regexp.MustCompile(`\b[A-Za-z0-9_-]{32,}\b`),
}

// guardUsageResult rejects a reader result that copies the account's credential
// or a token-like value. The rejection is a typed reason, never the leaked text.
func guardUsageResult(o store.Observation, probeErr error, c native.Captured) (store.Observation, error) {
	leaked := usageOutputLeaks(o, c)
	if probeErr != nil && textContainsSecret(probeErr.Error(), c) {
		leaked = true
	}
	if !leaked {
		return o, probeErr
	}
	return store.Observation{}, &native.UsageUnavailableError{
		Reason: store.UsageReasonSecret,
		Detail: readerSecretDetail,
	}
}

// usageOutputLeaks checks every string a reader result can show the owner.
func usageOutputLeaks(o store.Observation, c native.Captured) bool {
	fields := []string{o.UsageError}
	if o.Plan != nil {
		fields = append(fields, o.Plan.ID, o.Plan.Label)
	}
	for _, w := range o.Windows {
		fields = append(fields, w.ID, w.Label, w.Period, w.PeriodStart, w.Unit, w.ResetsAt, string(w.Reason))
	}
	for _, field := range fields {
		if textContainsSecret(field, c) {
			return true
		}
	}
	return false
}

// isReaderSecret reports the typed rejection of a reader result that leaked a
// credential or a token-like value.
func isReaderSecret(err error) bool {
	var unavailable *native.UsageUnavailableError
	return errors.As(err, &unavailable) && unavailable.Reason == store.UsageReasonSecret
}

// textContainsSecret reports a raw credential value or a token-like string.
func textContainsSecret(text string, c native.Captured) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	for _, secret := range native.CredentialSecretValues(c.Credential) {
		if len(secret) >= 8 && strings.Contains(text, secret) {
			return true
		}
	}
	for _, pattern := range secretLike {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}
