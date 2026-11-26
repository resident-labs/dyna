package agy

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
)

// ErrSignInRequired is agy answering `authenticate` with no sign-in on this host (T3 ASUP:167-177).
var ErrSignInRequired = errors.New("agy is not signed in on this host")

// ErrSubscriptionRequired is T3's text for a sign-in Google refuses for lack of a plan (AntigravityAuth.ts:157-159).
var ErrSubscriptionRequired = errors.New("Google requires an eligible Antigravity subscription for this account.")

// agy returns an upstream failure as a chat message and end_turn. T3 reads the session database to find it and classifies
// the error step in turnFailure (acp/AntigravityTurnOutcome.ts:214-271, still so on T3 main d78106d). Only engine/store may
// use database/sql here, so these are T3's patterns read from the turn's text. The text counts only when a line of it
// starts with an error shape T3 reads (the API error, the status error or the HTTP error body), so an answer that merely
// mentions a status code or a token such as SUBSCRIPTION_REQUIRED is not taken for an error. Go's regexp has no
// lookaround, so the shapes are loosened a little. Like T3, there is no ban-specific handling: Google's message is shown.
const maxErrorText = 16 << 10

var (
	apiError      = regexp.MustCompile(`(?sm)^[ \t]*Error (\d{3}), Message: (.+?), Status: ([A-Z_]+|\d{3}[^,\n]*)`)
	statusError   = regexp.MustCompile(`(?m)^[ \t]*([A-Z_]+) \(code (\d{3})\): ([^\x00-\x1f]+)`)
	errorBody     = regexp.MustCompile(`(?m)^[ \t]*\{\s*"error"\s*:`)
	validation    = regexp.MustCompile(`"reason"\s*:\s*"VALIDATION_REQUIRED"`)
	validationURL = regexp.MustCompile(`"validation_url"\s*:\s*"([^"]+)"`)

	quotaExhausted = regexp.MustCompile(`\breason:QUOTA_EXHAUSTED\b`)
	goDuration     = `((?:\d+(?:\.\d+)?(?:ns|us|µs|μs|ms|h|m|s))+)`
	resetTimestamp = regexp.MustCompile(`\bquotaResetTimeStamp:([^\s\]]+)`)
	resetAfter     = []*regexp.Regexp{
		regexp.MustCompile(`\bquotaResetDelay:` + goDuration),
		regexp.MustCompile(`\bretryDelay:` + goDuration),
		regexp.MustCompile(`\bResets in ` + goDuration),
	}
)

// failure is how a turn ended badly: the TurnEnded fields. Start returns no error for it, as the runner would replace the
// result with harness_failed. A retryable failure is CauseModelFailed, which the engine retries; one that is not is
// CauseHarnessFailed, which it does not, so the session waits for a new input.
type failure struct {
	cause      contract.FailureCause
	message    string
	retryAfter int64 // seconds, for CauseModelFailed
}

func retryable(message string, reset time.Duration) *failure {
	// TODO: use the contract's longer limit-reset bound once it is on main; until then a long reset is capped here.
	seconds := min(int64(reset.Seconds()+0.5), contract.MaxModelRetryAfterSeconds)
	return &failure{cause: contract.CauseModelFailed, message: harness.Mask(clip(message)), retryAfter: seconds}
}

// classify reads a turn's text or a JSON-RPC error message and returns nil for anything that is not a Google failure.
func classify(text string, now time.Time) *failure {
	if len(text) > maxErrorText {
		return nil
	}
	if errorBody.MatchString(text) && validation.MatchString(text) {
		message := "Verify your Google account, then retry this turn."
		if m := validationURL.FindStringSubmatch(text); m != nil {
			message += " " + m[1]
		}
		return &failure{cause: contract.CauseHarnessFailed, message: message}
	}
	status, code, message := "", "", ""
	if m := apiError.FindStringSubmatch(text); m != nil {
		code, message, status = m[1], strings.TrimSpace(m[2]), m[3]
	} else if m := statusError.FindStringSubmatch(text); m != nil {
		status, code, message = m[1], m[2], strings.TrimSpace(m[3])
	} else {
		return nil
	}
	reset := resetDelay(text, now)
	if quotaExhausted.MatchString(text) || code == "429" || status == "RESOURCE_EXHAUSTED" {
		return retryable(message, reset)
	}
	// T3 also retries a 404 after the model answered; the text cannot say whether it had, so a 404 is not retried.
	if code >= "500" {
		return retryable(message, reset)
	}
	return &failure{cause: contract.CauseHarnessFailed, message: harness.Mask(clip(message))}
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

// stopFailure is how a turn's stopReason, and its text on end_turn, ended it.
func stopFailure(reason, text string, now time.Time) *failure {
	switch reason {
	case "end_turn":
		return classify(text, now)
	case "refusal":
		// agy maps only the SDK's QUOTA_EXHAUSTED stop reason to refusal (T3 turnFailure).
		if f := classify(text, now); f != nil && f.cause == contract.CauseModelFailed {
			return f
		}
		return retryable("You have reached your Antigravity quota for this period.", 0)
	case "max_tokens":
		return &failure{cause: contract.CauseOutputLimit, message: "the reply reached the model's output limit"}
	case "cancelled":
		return nil
	}
	return &failure{cause: contract.CauseHarnessFailed, message: fmt.Sprintf("agy stopped the turn: %s", reason)}
}
