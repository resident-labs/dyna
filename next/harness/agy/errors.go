package agy

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/harness/agy/sessiondb"
)

// ErrSignInRequired is agy answering `authenticate` with no sign-in on this host (T3 ASUP:167-177).
var ErrSignInRequired = errors.New("agy is not signed in on this host")

// ErrSubscriptionRequired is T3's text for a sign-in Google refuses for lack of a plan (AntigravityAuth.ts:157-159).
var ErrSubscriptionRequired = errors.New("Google requires an eligible Antigravity subscription for this account.")

// agy returns an upstream failure as a chat message and end_turn. T3 finds it in the session's own log: the last step the
// prompt added, when it is an error step (acp/AntigravityTurnOutcome.ts, turnFailure and watchAntigravityPromptOutcome), and
// so does package sessiondb. Without that evidence only a typed `refusal` fails the turn; the reply text is never read. The
// patterns below are T3's, with the lookarounds Go lacks loosened. Like T3, there is no ban-specific handling: Google's message
// is shown.
var (
	apiError    = regexp.MustCompile(`(?s)Error (\d{3}), Message: (.+?), Status: ([A-Z_]+|\d{3}[^,\n]*)`)
	statusError = regexp.MustCompile(`([A-Z_]+) \(code (\d{3})\): ([^\p{Cc}]+)`)
	errorJSON   = regexp.MustCompile(`\{\s*"error"\s*:`)

	quotaExhausted   = regexp.MustCompile(`\breason:QUOTA_EXHAUSTED\b`)
	invalidSignature = regexp.MustCompile("\\bmessages\\.\\d+\\.content\\.\\d+(?:\\.thinking\\.signature: Field required|: Invalid `signature` in `thinking` block)")
	goDuration       = `((?:\d+(?:\.\d+)?(?:ns|us|µs|μs|ms|h|m|s))+)`
	resetTimestamp   = regexp.MustCompile(`\bquotaResetTimeStamp:([^\s\]]+)`)
	resetAfter       = []*regexp.Regexp{
		regexp.MustCompile(`\bquotaResetDelay:` + goDuration + `(?:[\s\]]|$)`),
		regexp.MustCompile(`\bretryDelay:` + goDuration + `(?:[\s\]]|$)`),
		regexp.MustCompile(`\bResets in ` + goDuration + `(?:[^\w.]|\.\D|\.$|$)`),
	}
)

// failure is how a turn ended badly: the TurnEnded fields. Start returns no error for it, as the runner would replace the
// result with harness_failed. A retryable failure is CauseModelFailed, which the engine retries; one that is not is
// CauseHarnessFailed, which it does not, so the session waits for a new input.
type failure struct {
	cause      contract.FailureCause
	message    string
	retryAfter int64 // seconds, for CauseModelFailed
	broken     bool  // agy's saved conversation can no longer be sent
}

// retryable is a failure the engine's model retry may act on. A usage limit is retried at its reset when Google states one, up to
// the contract's quota bound; with no reset, or one beyond it, the 5-minute ladder would knock on a spent account up to eight
// times, so the turn fails with Google's message instead and the session waits for new input. A transient failure (5xx,
// 404 after the model answered) uses the ladder, with the delay Google gives if any.
func retryable(message string, reset time.Duration, limit bool) *failure {
	message = harness.Mask(clip(message))
	seconds := int64(reset.Seconds() + 0.5)
	if limit {
		if seconds <= 0 || seconds > contract.MaxModelQuotaResetSeconds {
			return &failure{cause: contract.CauseHarnessFailed, message: message}
		}
		return &failure{cause: contract.CauseModelFailed, message: message, retryAfter: seconds}
	}
	return &failure{cause: contract.CauseModelFailed, message: message, retryAfter: min(seconds, contract.MaxModelRetryAfterSeconds)}
}

// validationRequired is T3's verificationErrorInfo: a 403 PERMISSION_DENIED JSON body whose details carry the
// VALIDATION_REQUIRED ErrorInfo of cloudcode-pa. It returns the verification URL, which may be empty.
func validationRequired(text string) (string, bool) {
	for _, loc := range errorJSON.FindAllStringIndex(text, -1) {
		obj := balancedJSON(text, loc[0])
		if obj == "" {
			continue
		}
		var body struct {
			Error struct {
				Code    int               `json:"code"`
				Status  string            `json:"status"`
				Details []json.RawMessage `json:"details"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(obj), &body) != nil || body.Error.Code != 403 || body.Error.Status != "PERMISSION_DENIED" {
			continue
		}
		for _, raw := range body.Error.Details {
			var d struct {
				Type     string `json:"@type"`
				Domain   string `json:"domain"`
				Reason   string `json:"reason"`
				Metadata *struct {
					URL any `json:"validation_url"`
				} `json:"metadata"`
			}
			if json.Unmarshal(raw, &d) != nil || d.Type != "type.googleapis.com/google.rpc.ErrorInfo" ||
				d.Domain != "cloudcode-pa.googleapis.com" || d.Reason != "VALIDATION_REQUIRED" {
				continue
			}
			if d.Metadata != nil {
				if u, ok := d.Metadata.URL.(string); ok {
					return u, true
				}
			}
			return "", true
		}
	}
	return "", false
}

// balancedJSON is T3's balancedJsonObject: the object that starts at start, or "".
func balancedJSON(text string, start int) string {
	depth, inString, escaped := 0, false, false
	for i := start; i < len(text); i++ {
		ch := text[i]
		switch {
		case inString && escaped:
			escaped = false
		case inString && ch == '\\':
			escaped = true
		case inString && ch == '"':
			inString = false
		case inString:
		case ch == '"':
			inString = true
		case ch == '{':
			depth++
		case ch == '}':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}

// classify is T3's turnFailure (AntigravityTurnOutcome.ts:199-276) for an error text: from the session log's error step
// (reachedModel: a planner response came in the prompt), or the message of a JSON-RPC error. It is nil when text is not an error.
func classify(text string, now time.Time, reachedModel bool) *failure {
	if quotaExhausted.MatchString(text) {
		return retryable("You have reached your Antigravity quota for this period.", resetDelay(text, now), true)
	}
	if invalidSignature.MatchString(text) {
		return &failure{cause: contract.CauseHarnessFailed, broken: true,
			message: "Antigravity's saved conversation can no longer be sent to the model. The next message starts a fresh Antigravity session."}
	}
	if url, ok := validationRequired(text); ok {
		message := "Verify your Google account, then retry this turn."
		if url != "" {
			message += " " + harness.Mask(clip(url))
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
	if code == "429" || status == "RESOURCE_EXHAUSTED" {
		return retryable(message, resetDelay(text, now), true)
	}
	// A 404 before the model answered is an unknown or unentitled model; after, a transient edge error (Outcome:263-276).
	if code >= "500" || (code == "404" && reachedModel) {
		return retryable(message, resetDelay(text, now), false)
	}
	return &failure{cause: contract.CauseHarnessFailed, message: harness.Mask(clip(message))}
}

// outcome is what the session log says about a prompt that ended (T3 turnFailure): a typed refusal is a usage limit, an
// error step is classified, anything else is no failure.
func outcome(reason string, step *sessiondb.Step, now time.Time) *failure {
	if reason == "refusal" {
		text := ""
		if step != nil {
			text = step.Error
		}
		return retryable("You have reached your Antigravity quota for this period.", resetDelay(text, now), true)
	}
	if step == nil || !step.IsError {
		return nil
	}
	if f := classify(step.Error, now, step.ReachedModel); f != nil {
		return f
	}
	return &failure{cause: contract.CauseHarnessFailed, message: "Antigravity stopped this turn because of an error."}
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

// stopFailure is how a stop reason other than a typed refusal or an error step ended a turn.
func stopFailure(reason string) *failure {
	switch reason {
	case "end_turn", "refusal", "cancelled":
		return nil
	case "max_tokens":
		return &failure{cause: contract.CauseOutputLimit, message: "the reply reached the model's output limit"}
	}
	return &failure{cause: contract.CauseHarnessFailed, message: fmt.Sprintf("agy stopped the turn: %s", reason)}
}
