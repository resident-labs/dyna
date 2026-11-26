package pig

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// PiG's assistant event has only the final error text. Its HTTP adapters put
// the status before the body (the route copies Retry-After into reset_seconds).
// With provider retries enabled, PiG may replace the body with the requested
// delay and the error's message when the delay exceeds its in-turn cap.
var resetError = regexp.MustCompile(`(?s)^(?:Server requested ([0-9]+)s retry delay \(max: [0-9]+s\)\. )?(?:[A-Za-z][A-Za-z _-]* \()?(429|502|503)\)?(?::? (.*))?$`)

type resetBody struct {
	Code         json.RawMessage `json:"code"`
	Message      string          `json:"message"`
	ResetSeconds int64           `json:"reset_seconds"`
}

func modelRetryAfterSeconds(message string) int64 {
	parts := resetError.FindStringSubmatch(message)
	if parts == nil {
		return 0
	}
	hint, status, body := parts[1], parts[2], parts[3]
	// Some adapters keep the HTTP reason phrase before the JSON body.
	if hint != "" && !strings.HasPrefix(body, "{") {
		if _, raw, ok := strings.Cut(body, ". "); ok && strings.HasPrefix(raw, "{") {
			body = raw
		}
	}
	var payload struct {
		resetBody
		Error json.RawMessage `json:"error"`
	}
	var detail resetBody
	if json.Unmarshal([]byte(body), &payload) == nil {
		detail = payload.resetBody
		// OpenAI may already have unwrapped the error envelope. In that
		// case an upstream string-valued error must not hide a sibling reset.
		var nested *resetBody
		if json.Unmarshal(payload.Error, &nested) == nil && nested != nil {
			detail = *nested
		}
	}
	limit := int64(contract.MaxModelRetryAfterSeconds)
	var fallback int64
	if status == "429" {
		switch strings.Trim(string(detail.Code), `"`) {
		case "1302":
			fallback = 60
		case "1305":
			fallback = 20
		case "transient_rate_limit":
		default:
			text := detail.Message
			if text == "" {
				text = body
			}
			if text != "Rate limit reached for requests" && text != "The service may be temporarily overloaded" {
				limit = contract.MaxModelQuotaResetSeconds
			}
		}
	}
	// PiG's OpenAI adapters may drop the body when the delay exceeds their
	// retry cap. In that case the prefix still carries the route's hint.
	seconds, _ := strconv.ParseInt(hint, 10, 64)
	seconds = max(seconds, detail.ResetSeconds)
	if seconds <= 0 || seconds > limit {
		if status == "429" && seconds == 0 {
			return fallback
		}
		return 0
	}
	return seconds
}
