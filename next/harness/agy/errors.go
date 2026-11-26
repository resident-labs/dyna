package agy

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
)

// QuotaError is a turn that failed on Google's usage limit. ResetAfter is zero when the text gave no reset time.
type QuotaError struct {
	ResetAfter time.Duration
	Message    string
}

func (e *QuotaError) Error() string { return e.Message }

// AccountError is Google refusing the account: Kind is "forbidden", "tos_violation", "subscription_required" or
// "signin_required".
type AccountError struct {
	Kind    string
	Message string
}

func (e *AccountError) Error() string { return "antigravity account: " + e.Kind + ": " + e.Message }

// agy returns an upstream failure as a chat message and end_turn. T3 reads the session database to find it
// (AntigravityTurnOutcome.ts); only engine/store may use database/sql here, so these are T3's patterns read from the
// message text. Go's regexp has no lookaround, so the two error shapes are loosened a little. A message must also be short,
// so an answer that merely talks about a status code is not taken for an error.
const maxErrorText = 16 << 10

var (
	apiError       = regexp.MustCompile(`(?s)Error (\d{3}), Message: (.+?), Status: ([A-Z_]+|\d{3}[^,\n]*)`)
	statusError    = regexp.MustCompile(`\b([A-Z_]+) \(code (\d{3})\): ([^\x00-\x1f]+)`)
	quotaExhausted = regexp.MustCompile(`\breason:QUOTA_EXHAUSTED\b`)
	goDuration     = `((?:\d+(?:\.\d+)?(?:ns|us|µs|μs|ms|h|m|s))+)`
	resetTimestamp = regexp.MustCompile(`\bquotaResetTimeStamp:([^\s\]]+)`)
	resetAfter     = []*regexp.Regexp{
		regexp.MustCompile(`\bquotaResetDelay:` + goDuration),
		regexp.MustCompile(`\bretryDelay:` + goDuration),
		regexp.MustCompile(`\bResets in ` + goDuration),
	}
	account = regexp.MustCompile(`\b(TOS_VIOLATION|SUBSCRIPTION_REQUIRED)\b`)
)

// classify reads a turn's text or a JSON-RPC error message. It returns nil for anything that is not a Google failure.
func classify(text string, now time.Time) error {
	if len(text) > maxErrorText {
		return nil
	}
	status, code, message := "", "", ""
	if m := apiError.FindStringSubmatch(text); m != nil {
		code, message, status = m[1], m[2], m[3]
	} else if m := statusError.FindStringSubmatch(text); m != nil {
		status, code, message = m[1], m[2], m[3]
	}
	if m := account.FindStringSubmatch(text); m != nil {
		kind := strings.ToLower(m[1])
		return &AccountError{Kind: kind, Message: harness.Mask(clip(text))}
	}
	if code == "403" || status == "PERMISSION_DENIED" {
		return &AccountError{Kind: "forbidden", Message: harness.Mask(clip(text))}
	}
	if quotaExhausted.MatchString(text) || code == "429" || status == "RESOURCE_EXHAUSTED" {
		if message == "" {
			message = "You have reached your Antigravity quota for this period."
		}
		return &QuotaError{ResetAfter: resetDelay(text, now), Message: harness.Mask(clip(strings.TrimSpace(message)))}
	}
	return nil
}

func clip(s string) string {
	if len(s) > 500 {
		return strings.ToValidUTF8(s[:500], "")
	}
	return s
}

func resetDelay(text string, now time.Time) time.Duration {
	if m := resetTimestamp.FindStringSubmatch(text); m != nil {
		if at, err := time.Parse(time.RFC3339, m[1]); err == nil {
			return max(0, at.Sub(now))
		}
	}
	for _, re := range resetAfter {
		if m := re.FindStringSubmatch(text); m != nil {
			if d, err := time.ParseDuration(m[1]); err == nil {
				return d
			}
		}
	}
	return 0
}

// failure is how a turn ended badly: the TurnEnded fields, and the error Start returns with it, if any.
type failure struct {
	cause      contract.FailureCause
	message    string
	retryAfter int64
	err        error
}

func quotaFailure(q *QuotaError) *failure {
	seconds := min(int64(q.ResetAfter.Seconds()+0.5), contract.MaxModelRetryAfterSeconds)
	return &failure{cause: contract.CauseModelFailed, message: q.Message, retryAfter: seconds}
}

func stopFailure(reason, text string, now time.Time) *failure {
	switch reason {
	case "end_turn":
		switch err := classify(text, now).(type) {
		case *QuotaError:
			return quotaFailure(err)
		case *AccountError:
			return &failure{cause: contract.CauseModelFailed, message: err.Error(), err: err}
		}
		return nil
	case "refusal":
		// agy maps only the SDK's QUOTA_EXHAUSTED stop reason to refusal (T3: turnFailure).
		q, ok := classify(text, now).(*QuotaError)
		if !ok {
			q = &QuotaError{Message: "You have reached your Antigravity quota for this period."}
		}
		return quotaFailure(q)
	case "max_tokens":
		return &failure{cause: contract.CauseOutputLimit, message: "the reply reached the model's output limit"}
	case "cancelled":
		return nil
	}
	return &failure{cause: contract.CauseHarnessFailed, message: fmt.Sprintf("agy stopped the turn: %s", reason)}
}
