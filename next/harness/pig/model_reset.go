package pig

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PiG's assistant event has only the final error text. Its HTTP adapters put
// the status before the body, and prefix errors from a Retry-After above its
// in-turn retry cap with the requested delay.
var resetError = regexp.MustCompile(`(?s)^(?:Server requested ([0-9]+)s retry delay \(max: [0-9]+s\)\. )?(?:[A-Za-z][A-Za-z _-]* \()?(429|502|503)\)?(?::? (.*))?$`)

type resetBody struct {
	Code         json.RawMessage `json:"code"`
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
		Error *resetBody `json:"error"`
	}
	var detail resetBody
	if json.Unmarshal([]byte(body), &payload) == nil {
		detail = payload.resetBody
		if payload.Error != nil {
			detail = *payload.Error
		}
	}
	switch strings.Trim(string(detail.Code), `"`) {
	case "1302", "1305", "model_cooldown":
	default:
		// OpenAI's capped-delay error can preserve the message but omit the
		// code and body. Accept only the messages of those same 429s.
		known429 := body == "Rate limit reached for requests" || body == "The service may be temporarily overloaded" ||
			(strings.HasPrefix(body, "All credentials for model ") && strings.Contains(body, " are cooling down"))
		if status == "429" && (hint == "" || !known429) {
			return 0
		}
	}
	seconds, _ := strconv.ParseInt(hint, 10, 64)
	seconds = max(seconds, detail.ResetSeconds)
	if seconds < 0 || seconds > int64((1<<63-1)/time.Second) {
		return 0
	}
	return seconds
}
