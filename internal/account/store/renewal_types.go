package store

import "time"

// RenewalResult is the classified outcome of one engine credential renewal.
// Providers never supply these values or the stored reason.
type RenewalResult string

const (
	RenewalResultRenewed      RenewalResult = "renewed"
	RenewalResultNoCredential RenewalResult = "no_credential"
	RenewalResultInvalidGrant RenewalResult = "invalid_grant"
	RenewalResultTransient    RenewalResult = "transient"
)

// Renewal records an engine attempt, never a renewal a harness performs alone.
// Reason is short, classified text and contains no provider response or token.
type Renewal struct {
	AttemptedAt time.Time     `json:"attemptedAt"`
	Result      RenewalResult `json:"result"`
	Reason      string        `json:"reason,omitempty"`
	AccountID   string        `json:"accountId"`
	Harness     string        `json:"harness"`
	Provider    Provider      `json:"provider,omitempty"`
}
