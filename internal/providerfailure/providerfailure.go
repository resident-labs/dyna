// Package providerfailure names why a provider refused a turn. Recovery routes
// on the named reason, so text the model wrote can never move an account, a
// model or a profile.
package providerfailure

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Reason is a provider failure that a trusted boundary observed: a protocol
// error field or a process's standard error. The zero Reason means that no
// trusted boundary named a provider failure; it never means "unknown provider
// failure".
type Reason string

const (
	// Auth means the provider rejected the account, the key or the sign-in.
	// Another model cannot fix it, so the account needs a new sign-in.
	Auth Reason = "auth"
	// Quota means the account ran out of quota, credit or rate limit. Another
	// account, and after that another model, can continue the work.
	Quota Reason = "quota"
	// Capacity means the provider is busy for a moment. Inside a turn it keeps
	// the same model after a backoff, then moves model. A turn that still ends at
	// capacity has spent that chain, so the profile steps aside; the account is
	// never recorded as out of quota.
	Capacity Reason = "capacity"
	// Unavailable means the provider failed on its own side, such as a 502.
	Unavailable Reason = "unavailable"
)

// BlocksProfile reports whether the reason keeps a profile out until someone
// fixes it or the provider recovers. Every named reason does: a capacity
// failure that ends a turn has already spent its backoff and model chain, so
// starting the same provider again would only loop.
func (r Reason) BlocksProfile() bool { return r != "" }

// Failure is an error with the trusted reason a boundary observed.
type Failure struct {
	Reason Reason
	Err    error
}

func (f *Failure) Error() string { return f.Err.Error() }

// Unwrap keeps the original error readable to errors.Is and errors.As.
func (f *Failure) Unwrap() error { return f.Err }

// Tag wraps err with reason. It returns err unchanged when the reason is empty
// or err already carries a reason deeper in its chain.
func Tag(reason Reason, err error) error {
	if reason == "" || err == nil {
		return err
	}
	if _, ok := ReasonOf(err); ok {
		return err
	}
	return &Failure{Reason: reason, Err: err}
}

// TagDetail wraps err with the reason a protocol error field names. Callers
// pass only a structured diagnostic; stderr goes through TagStderr, and model
// output and persisted display text never reach either.
func TagDetail(err error, detail string) error {
	reason, ok := Classify(detail)
	if !ok {
		return err
	}
	return Tag(reason, err)
}

// ReasonOf returns the reason an error chain carries.
func ReasonOf(err error) (Reason, bool) {
	var f *Failure
	if errors.As(err, &f) && f.Reason != "" {
		return f.Reason, true
	}
	return "", false
}

// Is reports whether err carries reason.
func Is(err error, reason Reason) bool {
	got, ok := ReasonOf(err)
	return ok && got == reason
}

// TagStderr wraps err with the reason a process's standard error names. It is
// the only entry point for stderr evidence, and it bounds how much stderr it
// reads. Standard output never reaches it, because a worker's standard output
// can hold model text that talks about limits and credit.
func TagStderr(err error, stderr string) error {
	reason, ok := ClassifyStderr(stderr)
	if !ok {
		return err
	}
	return Tag(reason, err)
}

// stderrTail is how much of a process's standard error is classified. Provider
// diagnostics are short; the tail keeps the classification bounded.
const stderrTail = 2000

// ClassifyStderr classifies a process's standard error, which is a trusted
// boundary. It reads only the tail and matches only an actual provider
// diagnostic, so prose such as "reading line 429" or "checking quota
// configuration" names nothing.
func ClassifyStderr(stderr string) (Reason, bool) {
	if len(stderr) > stderrTail {
		stderr = stderr[len(stderr)-stderrTail:]
	}
	return match(stderrPatterns, stderr)
}

// protocolPatterns hold the provider vocabulary of a protocol error field, in
// match order. Such a field holds one provider diagnostic, so it may use the
// bare words. A busy provider is checked before a quota: "429 overloaded" is
// capacity, not a spent account.
var protocolPatterns = []struct {
	reason  Reason
	pattern *regexp.Regexp
}{
	{Capacity, regexp.MustCompile(`(?i)overloaded|at capacity|over capacity|service unavailable|server is busy`)},
	{Auth, regexp.MustCompile(`(?i)authentication[_ ]error|unauthorized|invalid api key|invalid[_ ]api[_ ]key|invalid x-api-key|not logged in|not signed in|signed out|reauthenticate`)},
	{Quota, regexp.MustCompile(`(?i)\bquotas?\b|credit balance|insufficient credits?|out of credits?|no credits? left|\b429\b|rate[ _-]?limit|usage[ _-]?limit|session limit|weekly limit|too many requests`)},
	{Unavailable, regexp.MustCompile(`(?i)internal server error|bad gateway|gateway timeout`)},
}

// stderrPatterns hold the narrower stderr vocabulary, in match order. Stderr
// mixes provider diagnostics with log lines, so a bare number or word is not
// evidence: a status code needs its HTTP, status, code or error label, and a
// quota word needs the provider's own outcome word.
var stderrPatterns = []struct {
	reason  Reason
	pattern *regexp.Regexp
}{
	{Capacity, regexp.MustCompile(`(?i)\bprovider (?:is )?overloaded\b|\boverloaded_error\b|\bat capacity\b|\bservice unavailable\b|\bserver is busy\b`)},
	{Auth, regexp.MustCompile(`(?i)\bauthentication[_ ]error\b|\binvalid api[_ ]key\b|\binvalid[_ ]api[_ ]key\b|\binvalid x-api-key\b|\bnot logged in\b|\bnot signed in\b|\bsigned out\b|\bunauthorized\b|\breauthenticate\b`)},
	{Quota, regexp.MustCompile(`(?i)\b(?:http|status|code|error)[ /:=]*429\b|\b429 (?:too many requests|rate[ _-]?limit|quota)|\bquotas? (?:exhausted|exceeded|reached|too low)\b|\busage[ _-]?limit\b|\bsession limit\b|\bweekly limit\b|\bcredit balance (?:is )?too low\b|\binsufficient credits?\b|\bout of credits?\b|\bno credits? left\b|\brate[ _-]?limit (?:exceeded|reached|hit)\b|\btoo many requests\b`)},
	{Unavailable, regexp.MustCompile(`(?i)\binternal server error\b|\bbad gateway\b|\bgateway timeout\b`)},
}

func match(patterns []struct {
	reason  Reason
	pattern *regexp.Regexp
}, detail string) (Reason, bool) {
	if strings.TrimSpace(detail) == "" {
		return "", false
	}
	for _, entry := range patterns {
		if entry.pattern.MatchString(detail) {
			return entry.reason, true
		}
	}
	return "", false
}

// Classify returns the reason a protocol error field names, or ok false when it
// names none. The detail must come from a trusted boundary; see TagDetail.
func Classify(detail string) (Reason, bool) { return match(protocolPatterns, detail) }

// ClassifyField returns the reason a structured protocol type, code or status
// names. Such a field joins its words with underscores or dashes, as in
// "insufficient_quota", so it is also read with those joins as spaces, and a
// bare number is an HTTP status. The value must come from a structured error
// field, never from model output.
func ClassifyField(field string) (Reason, bool) {
	field = strings.TrimSpace(field)
	if status, err := strconv.Atoi(field); err == nil {
		if reason, ok := StatusReason(status); ok {
			return reason, true
		}
	}
	if reason, ok := Classify(field); ok {
		return reason, true
	}
	return Classify(strings.NewReplacer("_", " ", "-", " ").Replace(field))
}

// ClassifyJSONField returns the reason a raw JSON scalar field names: a string
// is read as a provider token or word, and a number as an HTTP status. Callers
// pass only a field of a structured provider error.
func ClassifyJSONField(raw []byte) (Reason, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return ClassifyField(text)
	}
	var status int
	if json.Unmarshal(raw, &status) == nil {
		return StatusReason(status)
	}
	return "", false
}

// StatusReason returns the reason a provider HTTP status names. A status a
// provider reports is structured evidence, so it needs no matching text. A 402
// asks for payment and names no provider failure.
func StatusReason(status int) (Reason, bool) {
	switch status {
	case 401:
		return Auth, true
	case 429:
		return Quota, true
	case 503:
		return Capacity, true
	case 500, 502, 504:
		return Unavailable, true
	}
	return "", false
}

// Action is what recovery does for a reason. Each reason has exactly one action
// so a caller never decides again from display text.
type Action string

const (
	// ActionReauthenticate stops the turn: another model or account cannot sign
	// the account in.
	ActionReauthenticate Action = "reauthenticate"
	// ActionMoveAccount moves the turn to another allowed account, then to the
	// next model or profile when no account is left.
	ActionMoveAccount Action = "move-account"
	// ActionRetryLater keeps the same model, waits with backoff, and then moves
	// to the next model.
	ActionRetryLater Action = "retry-later"
	// ActionTryAnotherModel moves to the next model of the profile.
	ActionTryAnotherModel Action = "try-another-model"
)

// Action returns the one recovery this reason allows.
func (r Reason) Action() Action {
	switch r {
	case Auth:
		return ActionReauthenticate
	case Quota:
		return ActionMoveAccount
	case Capacity:
		return ActionRetryLater
	case Unavailable:
		return ActionTryAnotherModel
	}
	return ""
}
