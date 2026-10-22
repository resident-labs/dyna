package realtime

// Ported from qwen-audio-agent server/src/voice/reconnect-backoff.mjs and
// realtime-provider-session.mjs (Apache-2.0).

import (
	"math/rand/v2"
	"time"
)

// A call redials a dropped provider connection: MaxPendingAudio chunks of
// the owner's audio wait meanwhile, the oldest dropped first, and a
// connection that stayed up StableConnection starts the backoff afresh.
// After MaxRedials failed dials in a row the call ends; qwen retries until
// an error is fatal, and Dyna ends the call instead of ringing forever.
const (
	MaxPendingAudio  = 30
	StableConnection = 10 * time.Second
	MaxRedials       = 6
)

// backoff is the wait before each redial: base doubled per attempt up to
// max, with jitter either way.
type backoff struct {
	base, max time.Duration
	jitter    float64
	random    func() float64
	attempt   int
}

func newBackoff() *backoff {
	return &backoff{base: 500 * time.Millisecond, max: 10 * time.Second, jitter: 0.2, random: rand.Float64}
}

func (b *backoff) next() time.Duration {
	d := min(b.max, b.base<<min(b.attempt, 8))
	b.attempt++
	return max(0, d+time.Duration(float64(d)*b.jitter*(b.random()*2-1)))
}

func (b *backoff) reset() { b.attempt = 0 }
