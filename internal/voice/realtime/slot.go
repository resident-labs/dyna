package realtime

// Ported from qwen-audio-agent server/src/voice/realtime-response-slot.mjs
// and the watchdogs in realtime-provider.mjs (Apache-2.0).

import "time"

// The provider's single response slot is guarded: a response asked for
// must start within the provider's ResponseStartTimeout
// (DefaultResponseStart when unset), and a response that runs must send
// something at least every ResponseInactivity. Otherwise the call cancels
// it, and if the slot is not free CancelGrace later, it drops the
// connection, which the call then redials. A local timeout is never taken
// as proof that the remote slot is free.
const (
	DefaultResponseStart = 30 * time.Second
	ResponseInactivity   = 2 * time.Minute
	CancelGrace          = time.Second
)

// slot is the state of the provider's response slot as the call sees it.
type slot struct {
	phase string // "", "pending" (asked for or busy), "running" or "cancelling"
	stop  func() // stops the running timer
	gen   int    // counts timers: one that fires after it was replaced does nothing
}

// arm starts the slot's timer: fn runs after d unless the timer is
// stopped or replaced first.
func (c *Call) arm(d time.Duration, fn func()) {
	c.stopSlot()
	gen := c.slot.gen
	c.slot.stop = c.later(d, func() {
		if gen == c.slot.gen {
			fn()
		}
	})
}

// occupy notes that the slot is taken by a response that has not started:
// one the call asked for, or one the provider says is active.
func (c *Call) occupy() {
	if c.slot.phase != "" {
		return
	}
	c.slot.phase = "pending"
	wait := c.provider.ResponseStartTimeout
	if wait == 0 {
		wait = DefaultResponseStart
	}
	c.arm(wait, c.recoverSlot)
}

// running notes activity of the response in the slot: it started, or sent
// an event. The inactivity wait starts again.
func (c *Call) running() {
	if c.slot.phase == "cancelling" {
		return
	}
	c.slot.phase = "running"
	c.arm(ResponseInactivity, c.recoverSlot)
}

// recoverSlot cancels the response in the slot. If the slot is not free
// CancelGrace later, the connection is dropped and the call redials.
func (c *Call) recoverSlot() {
	if c.slot.phase == "cancelling" || c.slot.phase == "" {
		return
	}
	c.slot.phase = "cancelling"
	// Armed before the cancel is sent, as qwen does.
	c.arm(CancelGrace, func() {
		if c.conn != nil {
			c.conn.Close() // the reader sees the drop, and the call redials
		}
	})
	if c.active != "" {
		c.cancelled[c.active] = true
	}
	c.send(c.provider.Protocol.ResponseCancel())
}

// releaseSlot notes that the slot is free.
func (c *Call) releaseSlot() {
	c.stopSlot()
	c.slot.phase = ""
}

func (c *Call) stopSlot() {
	c.slot.gen++
	if c.slot.stop != nil {
		c.slot.stop()
		c.slot.stop = nil
	}
}
