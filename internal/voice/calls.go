package voice

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/voice/realtime"
)

// Calls runs the owner's voice call for the engine: at most one, for one
// room, at a time.
type Calls struct {
	// Ctx bounds every call: the engine's lifetime.
	Ctx context.Context
	// Open links a room, by ID or name.
	Open func(ref string) (*RoomLink, error)
	// start starts a call; nil uses startCall.
	start func(context.Context, *RoomLink) (*session, error)
	// ready says why calls are unavailable, or ""; nil uses voiceReady.
	ready func() string
	// afterFunc runs f after d, like time.AfterFunc; nil uses it. Tests
	// fire the timer themselves.
	afterFunc func(d time.Duration, f func()) (stop func() bool)
	// waiting, when set, runs as a stop starts to wait for its call to
	// end. Tests use it to see that a stop blocks.
	waiting func()

	mu   sync.Mutex
	live *liveCall // reserved from the start until its call has ended
	last string    // why the last call ended, when it failed
}

// liveCall is one call. Its fields change only under Calls.mu, except
// exited, which closes once.
type liveCall struct {
	link     *RoomLink
	started  time.Time
	stop     context.CancelFunc
	sess     *session      // nil until the call is connected
	stopping bool          // Stop has ended it
	exited   chan struct{} // closed when the start failed or the call ended
	err      error         // why it ended, when it failed
	clients  int           // pages joined now
	idle     func() bool   // stops the pending abandon timer, if any
	idleGen  int           // the abandon timer that may still end the call
}

// AbandonAfter is how long a live call may run with no page connected. A
// page reload reconnects well within it; a closed tab or a lost phone ends
// the call, as Hang up does.
const AbandonAfter = 60 * time.Second

// CallStatus is the engine's voice call, as the app sees it.
type CallStatus struct {
	// Available is true once a Codex account is active: the owner's opt-in
	// to voice. The app offers calls only then; Unavailable says what is
	// missing.
	Available   bool   `json:"available"`
	Unavailable string `json:"unavailable,omitempty"`
	// Live is true once the call is connected. RoomID and Room name its room
	// while it starts, runs or stops.
	Live   bool   `json:"live"`
	RoomID string `json:"roomId,omitempty"`
	Room   string `json:"room,omitempty"`
	// StartedAt (unix ms) is when the call started, before any of its
	// posts. The app shows the call's voice-call posts from then on.
	StartedAt int64 `json:"startedAt,omitempty"`
	// Error says why the last call ended, when it failed.
	Error string `json:"error,omitempty"`
}

// BusyError is a start refused because a call holds another room.
type BusyError struct{ Room string }

func (e *BusyError) Error() string {
	return fmt.Sprintf("a voice call is already live in room %s; end it first", e.Room)
}

// Start starts the call for room ref and returns once it is connected.
// A start for the room whose call is live returns its status.
func (c *Calls) Start(ref string) (CallStatus, error) {
	link, err := c.Open(ref)
	if err != nil {
		return CallStatus{}, err
	}
	c.mu.Lock()
	c.reapLocked()
	if lc := c.live; lc != nil {
		defer c.mu.Unlock()
		switch {
		case lc.link.ID() != link.ID():
			return CallStatus{}, &BusyError{Room: lc.link.Name()}
		case lc.stopping:
			return CallStatus{}, errors.New("the call is still ending; try again")
		case lc.sess == nil:
			return CallStatus{}, errors.New("the call is still starting")
		}
		if speaker, ok := lc.sess.call.(Speaker); ok {
			lc.link.joinRequestedCall(speaker)
		}
		return c.statusLocked(), nil
	}
	ctx, stop := context.WithCancel(c.Ctx)
	lc := &liveCall{link: link, started: time.Now(), stop: stop, exited: make(chan struct{})}
	c.live, c.last = lc, ""
	c.mu.Unlock()

	start := c.start
	if start == nil {
		start = startCall
	}
	sess, err := start(ctx, link)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		// A failed start has already cleaned up.
		stop()
		lc.err = err
		close(lc.exited)
		return CallStatus{}, err
	}
	go func() {
		<-sess.done
		c.mu.Lock()
		lc.err = sess.err
		c.mu.Unlock()
		close(lc.exited)
	}()
	if lc.stopping {
		// Stop ran while it started. Stop waits for this call to end.
		return CallStatus{}, errors.New("the call was ended while it started")
	}
	lc.sess = sess
	// The page connects right after the start; one that never does
	// abandons the call.
	c.idleLocked(lc)
	return c.statusLocked(), nil
}

// Status reports the current call.
func (c *Calls) Status() CallStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statusLocked()
}

func (c *Calls) statusLocked() CallStatus {
	c.reapLocked()
	if c.live == nil {
		ready := c.ready
		if ready == nil {
			ready = voiceReady
		}
		why := ready()
		return CallStatus{Available: why == "", Unavailable: why, Error: c.last}
	}
	lc := c.live
	return CallStatus{Available: true, Live: lc.sess != nil && !lc.stopping, RoomID: lc.link.ID(), Room: lc.link.Name(), StartedAt: lc.started.UnixMilli()}
}

// voiceReady checks, without starting anything, what a call needs first:
// an active Codex account.
func voiceReady() string {
	if _, err := runtime.NewAccounts().Active("codex"); err != nil {
		return "Sign in to Codex in Settings > Harnesses to call Voice."
	}
	return ""
}

// reapLocked releases a call that has fully exited by itself, and keeps why
// it failed. Stop releases the call it ends.
func (c *Calls) reapLocked() {
	lc := c.live
	if lc == nil || lc.stopping {
		return
	}
	select {
	case <-lc.exited:
		c.live = nil
		lc.stop()
		if lc.err != nil {
			c.last = lc.err.Error()
		}
	default:
	}
}

// Stop ends the call, if any, and returns once it has ended. Until then the
// call stays reserved, and every start is refused.
func (c *Calls) Stop() {
	c.mu.Lock()
	c.stopLocked(c.live)
}

// stopLocked ends lc as Stop does. It is called with c.mu held and returns
// with it released. A second stop of the same call, such as Hang up while
// the abandon timer ends it, also waits until it has ended.
func (c *Calls) stopLocked(lc *liveCall) {
	if lc == nil {
		c.mu.Unlock()
		return
	}
	first := !lc.stopping
	if first {
		lc.stopping = true
		if lc.idle != nil {
			lc.idle()
		}
	}
	waiting := c.waiting
	c.mu.Unlock()
	if first {
		lc.stop()
	}
	if waiting != nil {
		waiting()
	}
	<-lc.exited
	c.mu.Lock()
	if c.live == lc {
		c.live = nil
	}
	c.mu.Unlock()
}

// Join attaches page to the live call and returns send, which hands the
// call one message from the page, and leave, which the caller calls once
// when the page's socket ends. A newer page takes over from an older one.
// With no page joined, the call ends after AbandonAfter, as Hang up does; a
// page that joins in time keeps it.
func (c *Calls) Join(page *realtime.Page) (send func(realtime.PageMessage), leave func(), err error) {
	c.mu.Lock()
	c.reapLocked()
	lc := c.live
	if lc == nil || lc.sess == nil || lc.stopping {
		c.mu.Unlock()
		return nil, nil, errors.New("no voice call is live")
	}
	lc.clients++
	lc.idleGen++ // a timer that is already firing must not end the call
	if lc.idle != nil {
		lc.idle()
		lc.idle = nil
	}
	c.mu.Unlock()
	detach := lc.sess.call.Attach(page)
	var once sync.Once
	return lc.sess.call.FromPage, func() {
		once.Do(func() {
			detach()
			c.mu.Lock()
			defer c.mu.Unlock()
			lc.clients--
			if lc.clients == 0 && c.live == lc && !lc.stopping {
				c.idleLocked(lc)
			}
		})
	}, nil
}

// idleLocked starts the abandon timer for lc, which has no page connected.
func (c *Calls) idleLocked(lc *liveCall) {
	lc.idleGen++
	gen := lc.idleGen
	after := c.afterFunc
	if after == nil {
		after = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	lc.idle = after(AbandonAfter, func() {
		c.mu.Lock()
		if c.live != lc || lc.idleGen != gen || lc.clients > 0 {
			c.mu.Unlock()
			return
		}
		lc.idle = nil
		c.stopLocked(lc)
	})
}

// session is one running call.
type session struct {
	call pageCall
	done chan struct{} // closed when the call has ended
	err  error         // why it ended, when it failed; set before done closes
}

// pageCall is what a page needs from a call: *realtime.Call.
type pageCall interface {
	Attach(*realtime.Page) (leave func())
	FromPage(realtime.PageMessage)
}
