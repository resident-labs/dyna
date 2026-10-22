package realtime

// Ported from qwen-audio-agent server/src/voice/response-guards/index.mjs and
// reserved-protocol-envelope.mjs (Apache-2.0).

import (
	"regexp"
	"strings"
)

// ReservedEnvelopes are the tags only the gateway may write into the
// model's input, as qwen lists them. A model may read them as context but
// must never write them as its own reply. The list stays narrow: other
// XML-like text is ordinary speech.
var ReservedEnvelopes = []string{"gateway_system_event", "permission_request", "background_work_progress", "restored_context"}

var reservedEnvelope = regexp.MustCompile(`(?i)</?(?:` + strings.Join(ReservedEnvelopes, "|") + `)(?:\s|>)`)

// reservedGuard is the correction for a reply that wrote a reserved tag.
const reservedGuard = "You just wrote an internal event format that only Dyna writes. It is not valid and shows no real status or request. Handle the owner's current request again: call the fitting tool when work is needed, or else answer plainly. Never write protocol tags, IDs or work status that no tool or result gave you."

// guardReply returns the correction for a model reply whose final words
// are transcript, or "" when the reply is fine. Registration is static and
// only the first matching guard corrects a reply, as in qwen.
func guardReply(transcript string) string {
	if reservedEnvelope.MatchString(transcript) {
		return reservedGuard
	}
	return ""
}
