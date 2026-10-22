package engineclient

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

// UnacknowledgedError means no valid decision reached the caller after
// sending started. The request may have committed. Its original ID names
// ordered recovery or the engine receipt; the caller has no approval.
type UnacknowledgedError struct {
	RequestID string
	Cause     error
}

func (e *UnacknowledgedError) Error() string {
	return fmt.Sprintf("engine did not acknowledge request %s: %v", e.RequestID, e.Cause)
}

func (e *UnacknowledgedError) Unwrap() error { return e.Cause }

// waitedCall belongs to one entry. The queue mutex serializes result
// publication and detachment, so only one can win.
type waitedCall struct {
	cancelled chan struct{}
	started   bool
	detached  bool
	completed bool
	err       error
	typeOf    reflect.Type
	target    reflect.Value
	candidate reflect.Value
}

// CallResultWithinID waits at most one minute for an engine decision, apart
// from settling the canceled active sender and decoder. A shorter ctx wins.
// It uses the existing queue and never falls back to a local write.
// An unsent canceled call is removed. A sent one returns its uncertain ID
// after settlement and stays in ordered retry/spool recovery. Neither
// cancellation nor failed decoding changes result or the terminal scope.
//
// The returned ID belongs to the existing queue entry. It is empty if the
// call was never enqueued. A caller that rejects a decoded reply can retain
// this ID without trusting the reply.
func (o *Outbox) CallResultWithinID(ctx context.Context, result any, method string, args ...any) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	w := &waitedCall{cancelled: make(chan struct{})}
	if result != nil {
		w.target = reflect.ValueOf(result)
		if w.target.Kind() != reflect.Pointer || w.target.IsNil() {
			return "", fmt.Errorf("engine result must be a non-nil pointer")
		}
		w.typeOf = w.target.Type().Elem()
	}
	reply := make(chan error, 1)
	e := o.enqueue(method, args, reply, nil, w)
	return e.id, o.waitWithin(ctx, e)
}

func (o *Outbox) waitWithin(ctx context.Context, e *outboxEntry) error {
	select {
	case err := <-e.reply:
		return err
	case <-ctx.Done():
	}
	return o.cancelWithin(e, ctx.Err())
}

func (o *Outbox) cancelWithin(e *outboxEntry, cause error) error {
	w, reply := e.waited, e.reply
	o.mu.Lock()
	if w.completed {
		// A published reply wins even when cancellation is now ready too.
		o.mu.Unlock()
		return <-reply
	}
	if o.removeQueued(e) {
		w.target = reflect.Value{}
		o.mu.Unlock()
		return cause
	}
	w.detached = true
	w.err = &UnacknowledgedError{RequestID: e.id, Cause: cause}
	w.target = reflect.Value{}
	close(w.cancelled)
	if o.sending == e && o.sendCancel != nil {
		o.sendCancel()
	} else {
		// No active attempt remains to join. Backoff and spool I/O run
		// independently of this waiter and retain its original entry.
		o.finishWaited(e, nil)
	}
	o.mu.Unlock()
	return <-reply
}

// finishWaited requires mu and no active attempt for e. Publication and
// detachment use this same lock; retries retain no caller-owned target.
func (o *Outbox) finishWaited(e *outboxEntry, err error) {
	w := e.waited
	if w.completed {
		return
	}
	if w.detached {
		err = w.err
	} else if err == nil && w.target.IsValid() {
		w.target.Elem().Set(w.candidate.Elem())
	}
	w.target, w.candidate = reflect.Value{}, reflect.Value{}
	w.completed = true
	e.reply <- err
}

// callWaited owns only private response data. A typed decode failure is
// final; replaying an engine receipt cannot change its JSON type.
func (o *Outbox) callWaited(ctx context.Context, e *outboxEntry) (err error, decodeFailed bool) {
	w := e.waited
	o.mu.Lock()
	decode := !w.detached && w.typeOf != nil
	o.mu.Unlock()
	if !decode {
		return o.client.Call(ctx, e.method, e.args, nil), false
	}
	var raw json.RawMessage
	if err := o.client.Call(ctx, e.method, e.args, &raw); err != nil {
		return err, false
	}
	candidate := reflect.New(w.typeOf)
	if err := json.Unmarshal(raw, candidate.Interface()); err != nil {
		return &UnacknowledgedError{RequestID: e.id, Cause: err}, true
	}
	o.mu.Lock()
	if !w.detached {
		w.candidate = candidate
	}
	o.mu.Unlock()
	return nil, false
}
