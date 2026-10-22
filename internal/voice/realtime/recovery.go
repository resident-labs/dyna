package realtime

// Ported from qwen-audio-agent server/src/voice/realtime-recovery-context.mjs,
// the content-safety branch of realtime-session-runtime.mjs and the
// classifyError of providers/gpt-live.mjs (Apache-2.0).

import (
	"fmt"
	"regexp"
)

// MaxSafetyRecoveries is how many content-safety refusals in a row a call
// recovers from. Only a completed reply resets the count: a new connection
// alone does not prove that a turn can complete.
const MaxSafetyRecoveries = 2

// safetyNotice is what the page shows while the call recovers, and
// safetyLine what Voice says once the new connection is up.
const (
	safetyNotice = "Voice could not handle that and is reconnecting."
	safetyLine   = "The voice service refused the last part of the conversation, so you started fresh. Tell the owner in a few words that you could not handle that, and that they can say it another way."
	safetyStop   = "The voice service keeps refusing this conversation, so Voice stopped. Start a new call."
)

// openAIRefusal matches an OpenAI Realtime content-safety refusal, as qwen's
// GPT-Live classifyError does.
var openAIRefusal = regexp.MustCompile(`(?i)content[_ -]?(?:filter|moderation|safety|policy)|policy violation`)

// refusedContent recovers from a content-safety refusal. The provider will
// not go on with this conversation, so the call drops the connection and
// redials: the new connection starts fresh, without the refused turn.
// Audio still waiting from before the refusal is dropped too, so it cannot
// be refused again; the owner's speech after it goes to the new connection.
// Once the new connection is up, Voice says one short line about it. After
// MaxSafetyRecoveries in a row the call ends.
func (c *Call) refusedContent(text string) {
	if c.safetyRecoveries++; c.safetyRecoveries > MaxSafetyRecoveries || c.Redial == nil {
		c.toPage(PageEvent{Type: "error", Message: safetyStop})
		c.err = fmt.Errorf("the voice service refused the conversation: %s", text)
		return
	}
	c.toPage(PageEvent{Type: "error", Message: safetyNotice})
	c.recovering = true
	if c.conn != nil {
		c.conn.Close() // the reader sees the drop, and the call redials
	}
}
