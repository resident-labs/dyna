package engineclient

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"dyna-agent/internal/callspec"
)

// ErrSpooled means the engine did not acknowledge the write. Its request ID
// and arguments were durably spooled for engine recovery instead.
var ErrSpooled = errors.New("outbox writes were spooled without engine acknowledgement")

// Outbox sends one detached process's writes to the engine in order,
// without making the process wait (item 2). Each write gets a request ID
// from the outbox's prefix and a sequence number, and keeps it when it is
// sent again, so the engine applies it once (requirement 9). One goroutine
// sends the head of the queue, so a later write never lands before an
// earlier one.
//
// The prefix must be unique to the process, for example its session, turn,
// pid and start time: the engine remembers applied IDs, and a second
// process that reused them would have its writes taken for repeats.
type Outbox struct {
	client *Client
	prefix string
	// Logf reports writes the engine refused and writes Close drops.
	Logf func(format string, args ...any)
	// Spool takes queued writes in order after terminal expiry or a timed-out
	// Flush or Close. It must persist them before returning success.
	Spool func([]Queued) error

	mu      sync.Mutex
	queue   []*outboxEntry
	sending *outboxEntry // the head while it is being sent
	closed  bool
	seq     int
	idle    chan struct{} // shared by all waiters; closed when the queue is empty
	wake    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	stopped chan struct{}

	terminal   *terminalScope
	spoolOnly  bool
	spoolStart chan struct{}
	spoolErr   error
	sendCancel context.CancelFunc
	retryAfter func(time.Duration) <-chan time.Time
}

type terminalScope struct {
	ctx  context.Context
	stop func() bool
}

// Queued is a write the outbox could not send: its request ID, method and
// arguments, as it would have sent them.
type Queued struct {
	ID     string
	Method string
	Args   []any
}

type outboxEntry struct {
	id       string
	method   string
	args     []any
	result   any
	reply    chan error // set for Call
	spooling bool
	waited   *waitedCall
}

// NewOutbox starts an outbox that sends through c.
func NewOutbox(c *Client, prefix string) *Outbox {
	ctx, cancel := context.WithCancel(context.Background())
	idle := make(chan struct{})
	close(idle)
	o := &Outbox{client: c, prefix: prefix, Logf: log.Printf, wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel, stopped: make(chan struct{}), idle: idle, spoolStart: make(chan struct{}), retryAfter: time.After}
	go o.run()
	return o
}

// Send queues a write for the engine and returns at once.
func (o *Outbox) Send(method string, args ...any) {
	o.enqueue(method, args, nil, nil, nil)
}

// Call queues a write behind the earlier ones and waits for its reply. It
// is for a write the caller must know succeeded before it goes on. When ctx
// ends while the write is still queued, it is taken off the queue, so it
// never lands after its caller was told it failed. A write already being
// sent may still land, so Call then waits for its answer: the resend loop
// and Close bound that wait, not ctx.
func (o *Outbox) Call(ctx context.Context, method string, args ...any) error {
	return o.CallResult(ctx, nil, method, args...)
}

// CallResult is Call with an engine result decoded into result before it
// returns. A queued call removed on cancellation never changes result;
// a call being sent waits for its reply, so decoding cannot continue after
// its caller returns.
func (o *Outbox) CallResult(ctx context.Context, result any, method string, args ...any) error {
	reply := make(chan error, 1)
	e := o.enqueue(method, args, reply, result, nil)
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
	}
	o.mu.Lock()
	removed := o.removeQueued(e)
	o.mu.Unlock()
	if removed {
		return ctx.Err()
	}
	return <-reply
}

// removeQueued requires mu. Once a bounded call has started, its ID must
// stay in the queue even between attempts or while the worker spools it.
func (o *Outbox) removeQueued(e *outboxEntry) bool {
	for i, q := range o.queue {
		if q == e && q != o.sending && !q.spooling && (q.waited == nil || !q.waited.started) {
			copy(o.queue[i:], o.queue[i+1:])
			o.queue[len(o.queue)-1] = nil
			o.queue = o.queue[:len(o.queue)-1]
			if len(o.queue) == 0 {
				close(o.idle)
			}
			return true
		}
	}
	return false
}

func (o *Outbox) enqueue(method string, args []any, reply chan error, result any, waited *waitedCall) *outboxEntry {
	o.mu.Lock()
	o.seq++
	e := &outboxEntry{id: o.prefix + "/" + strconv.Itoa(o.seq), method: method, args: args, reply: reply, result: result, waited: waited}
	if o.closed {
		err := errors.New("outbox closed before the write was sent")
		if waited != nil {
			o.finishWaited(e, err)
		}
		o.mu.Unlock()
		o.Logf("outbox: %s %s queued after the outbox closed: lost", e.method, e.id)
		if waited == nil && reply != nil {
			reply <- err
		}
		return e
	}
	if len(o.queue) == 0 {
		o.idle = make(chan struct{})
	}
	o.queue = append(o.queue, e)
	o.mu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return e
}

// ArmTerminal starts the caller's terminal deadline, normally ten minutes
// before its first waited final call. Repeated calls keep the first scope.
// Expiry ends engine sending permanently, but later final writes can spool
// through this same outbox until Close.
func (o *Outbox) ArmTerminal(ctx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.spoolOnly || o.closed {
		return
	}
	if o.terminal == nil {
		scope := &terminalScope{ctx: ctx}
		o.terminal = scope
		scope.stop = context.AfterFunc(ctx, func() {
			o.mu.Lock()
			defer o.mu.Unlock()
			if o.terminal == scope {
				o.expire()
			}
		})
		if ctx.Err() != nil {
			o.expire()
		}
	}
}

// DisarmTerminal permits continued active work only if the terminal scope
// has not expired. A later final call can then arm a fresh scope.
func (o *Outbox) DisarmTerminal() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.spoolOnly || o.closed {
		return false
	}
	if scope := o.terminal; scope != nil {
		if scope.ctx.Err() != nil || !scope.stop() {
			o.expire()
			return false
		}
		o.terminal = nil
	}
	return true
}

// expire changes transport mode under mu. The worker owns both sending
// and spooling, so it joins any active sender and decoder before spooling.
func (o *Outbox) expire() {
	if o.spoolOnly {
		return
	}
	o.spoolOnly = true
	close(o.spoolStart)
	if o.sendCancel != nil {
		o.sendCancel()
	}
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// spoolResult requires mu. Callers release results only after settlement.
func (o *Outbox) spoolResult() error {
	if o.spoolErr != nil {
		return o.spoolErr
	}
	if o.spoolOnly {
		return ErrSpooled
	}
	return nil
}

// WaitIdle waits for the current ordered writes without closing the queue.
// Unlike Flush, cancellation only stops this observer. It never changes
// sending, terminal scope or spool state.
func (o *Outbox) WaitIdle(ctx context.Context) error {
	o.mu.Lock()
	idle := o.idle
	o.mu.Unlock()
	select {
	case <-idle:
	case <-ctx.Done():
		return ctx.Err()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.spoolResult()
}

// Flush waits for the shared queue to empty without closing it. Expiry of
// ctx or the armed terminal scope cancels engine sending, then waits for
// durable spool settlement. ErrSpooled distinguishes that result from an
// engine acknowledgement. A spool failure is returned instead.
func (o *Outbox) Flush(ctx context.Context) error {
	o.mu.Lock()
	idle := o.idle
	o.mu.Unlock()
	select {
	case <-idle:
	case <-ctx.Done():
		o.mu.Lock()
		o.expire()
		o.mu.Unlock()
		<-idle
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.spoolResult()
}

// Close seals the queue, settles it through the same Flush wait, and joins
// the worker. It never starts a new terminal budget.
func (o *Outbox) Close(ctx context.Context) {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	_ = o.Flush(ctx)
	o.cancel()
	<-o.stopped
	o.mu.Lock()
	if o.terminal != nil {
		o.terminal.stop()
	}
	o.mu.Unlock()
}

func (o *Outbox) run() {
	defer close(o.stopped)
	for delay := 100 * time.Millisecond; ; {
		o.mu.Lock()
		if o.spoolOnly && len(o.queue) > 0 {
			o.mu.Unlock()
			o.spool()
			continue
		}
		var head *outboxEntry
		if len(o.queue) > 0 {
			head = o.queue[0]
			if head.waited != nil {
				head.waited.started = true
			}
		}
		o.sending = head
		closed := o.closed
		o.mu.Unlock()
		if head == nil {
			if closed {
				return
			}
			select {
			case <-o.wake:
				continue
			case <-o.ctx.Done():
				return
			}
		}
		retry, err := o.send(head)
		o.mu.Lock()
		if head.waited != nil && head.waited.detached {
			o.finishWaited(head, nil)
		}
		if o.spoolOnly {
			o.sending = nil
			o.mu.Unlock()
			continue
		}
		var detached <-chan struct{}
		if head.waited != nil && !head.waited.detached {
			detached = head.waited.cancelled
		}
		o.mu.Unlock()
		if retry {
			// The engine did not answer, is down, or failed for now (5xx):
			// send the same write again, with the same request ID.
			select {
			case <-o.retryAfter(delay):
				delay = min(2*delay, time.Second)
				continue
			case <-o.ctx.Done():
				return
			case <-o.spoolStart:
				continue
			case <-detached:
				continue
			}
		}
		delay = 100 * time.Millisecond
		if err != nil {
			o.Logf("outbox: %s %s failed: %v", head.method, head.id, err)
		}
		o.mu.Lock()
		if head.waited != nil {
			o.finishWaited(head, err)
		}
		o.sending = nil
		o.queue = o.queue[1:]
		if len(o.queue) == 0 {
			close(o.idle)
		}
		o.mu.Unlock()
		if head.waited == nil && head.reply != nil {
			head.reply <- err
		}
	}
}

// spool runs only on the sender goroutine. The captured prefix cannot be
// removed by cancelled callers while its durable append is in progress.
func (o *Outbox) spool() {
	o.mu.Lock()
	if len(o.queue) == 0 {
		o.mu.Unlock()
		return
	}
	batch := append([]*outboxEntry(nil), o.queue...)
	left := make([]Queued, 0, len(batch))
	for _, e := range batch {
		e.spooling = true
		left = append(left, Queued{ID: e.id, Method: e.method, Args: e.args})
	}
	o.mu.Unlock()
	err := errors.New("no spool")
	if o.Spool != nil {
		err = o.Spool(left)
	}
	o.mu.Lock()
	if err != nil && o.spoolErr == nil {
		o.spoolErr = fmt.Errorf("outbox could not persist queued writes: %w", err)
	}
	result := o.spoolResult()
	for _, e := range batch {
		if e.waited != nil {
			o.finishWaited(e, result)
		}
	}
	o.queue = o.queue[len(batch):]
	o.sending = nil
	if len(o.queue) == 0 {
		close(o.idle)
	}
	o.mu.Unlock()
	for _, e := range batch {
		if err != nil {
			o.Logf("outbox: %s %s not sent before the process ended: lost (%v)", e.method, e.id, err)
		}
		if e.waited == nil && e.reply != nil {
			e.reply <- result
		}
	}
}

// send sends head's write to the engine. retry is true when the engine may
// still apply the write: it did not answer, or it failed for now (5xx).
// An engine refusal (4xx), including an unknown method, is final.
func (o *Outbox) send(head *outboxEntry) (retry bool, err error) {
	ctx, cancel := context.WithCancel(o.ctx)
	defer cancel()
	o.mu.Lock()
	o.sendCancel = cancel
	if o.spoolOnly {
		cancel()
	}
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		o.sendCancel = nil
		o.mu.Unlock()
	}()
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if head.waited != nil {
		var decodeFailed bool
		err, decodeFailed = o.callWaited(callspec.WithRequest(ctx, head.id), head)
		if decodeFailed {
			return false, err
		}
	} else {
		err = o.client.Call(callspec.WithRequest(ctx, head.id), head.method, head.args, head.result)
	}
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	var remote *RemoteError
	if !errors.As(err, &remote) {
		return err != nil, err
	}
	return !remote.Final(), err
}
