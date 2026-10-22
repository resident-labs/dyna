package engineclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
)

func TestOutboxBoundedCallReturnsAConfirmedResult(t *testing.T) {
	f := &fakeEngine{results: map[string]json.RawMessage{"Admit": json.RawMessage(`{"allowed":true}`)}}
	o := NewOutbox(serveFake(t, f), "runner")
	defer closeWithin(t, o)
	o.Send("Before")
	var result struct{ Allowed bool }
	if _, err := o.CallResultWithinID(t.Context(), &result, "Admit"); err != nil || !result.Allowed {
		t.Fatalf("admission = %+v, %v; want a confirmed result", result, err)
	}
	if got := f.seen(); !slices.Equal(got, []string{"Before runner/1", "Admit runner/2"}) {
		t.Fatalf("calls = %v; want ordered writes", got)
	}
}

func TestOutboxBoundedCallDoesNotPublishAPartialTypedResult(t *testing.T) {
	f := &fakeEngine{results: map[string]json.RawMessage{"Admit": json.RawMessage(`{"Allowed":true,"Slots":"bad"}`)}}
	o := NewOutbox(serveFake(t, f), "runner")
	defer closeWithin(t, o)
	result := struct {
		Allowed bool
		Slots   int
	}{Slots: 9}
	_, err := o.CallResultWithinID(t.Context(), &result, "Admit")
	var uncertain *UnacknowledgedError
	var mismatch *json.UnmarshalTypeError
	if !errors.As(err, &uncertain) || uncertain.RequestID != "runner/1" || !errors.As(err, &mismatch) || result.Allowed || result.Slots != 9 {
		t.Fatalf("typed decode = %+v, %v; want the original result and recoverable ID", result, err)
	}
	o.Send("After")
	closeWithin(t, o)
	if got := f.seen(); !slices.Equal(got, []string{"Admit runner/1", "After runner/2"}) {
		t.Fatalf("a permanent typed error was retried: %v", got)
	}
}

func TestOutboxBoundedCallKeepsAPublishedReplyWhenCancellationIsReady(t *testing.T) {
	f := &fakeEngine{results: map[string]json.RawMessage{"Admit": json.RawMessage(`17`)}}
	o := NewOutbox(serveFake(t, f), "runner")
	defer closeWithin(t, o)
	result := 29
	w := &waitedCall{cancelled: make(chan struct{}), target: reflect.ValueOf(&result), typeOf: reflect.TypeOf(result)}
	e := o.enqueue("Admit", nil, make(chan error, 1), nil, w)
	// Delay cancellation processing until the real worker publishes its
	// reply. Exercise that branch directly, without a random select winner.
	err := awaitTerminal(t, e.reply)
	e.reply <- err
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := o.cancelWithin(e, ctx.Err()); err != nil || result != 17 {
		t.Fatalf("published reply lost to cancellation: result %d, error %v", result, err)
	}
}

type waitedReplyBody struct {
	io.Reader
	ctx                         context.Context
	closing, cancelled, release chan struct{}
}

func (b *waitedReplyBody) Close() error {
	close(b.closing)
	<-b.ctx.Done()
	close(b.cancelled)
	<-b.release
	return nil
}

func TestOutboxBoundedCallDetachmentWinsAfterTheReplyBytesArrive(t *testing.T) {
	body := &waitedReplyBody{Reader: strings.NewReader(`17`), closing: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	free := sync.OnceFunc(func() { close(body.release) })
	client := noWait("")
	var first sync.Once
	client.http.Transport = claimTransport(func(r *http.Request) (*http.Response, error) {
		var response io.ReadCloser = io.NopCloser(strings.NewReader(`17`))
		first.Do(func() { body.ctx = r.Context(); response = body })
		return &http.Response{StatusCode: http.StatusOK, Body: response, Header: http.Header{}}, nil
	})
	o := NewOutbox(client, "runner")
	defer func() { free(); closeWithin(t, o) }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := 29
	done := make(chan error, 1)
	go func() {
		_, err := o.CallResultWithinID(ctx, &result, "Admit")
		done <- err
	}()
	// The full valid JSON has arrived. The sender still owns the response
	// and cannot publish the decision before closing it.
	awaitTerminal(t, body.closing)
	cancel()
	awaitTerminal(t, body.cancelled)
	free()
	var uncertain *UnacknowledgedError
	if err := awaitTerminal(t, done); !errors.As(err, &uncertain) || result != 29 {
		t.Fatalf("detached waiter published a late decision: result %d, error %v", result, err)
	}
	closeWithin(t, o)
	if result != 29 {
		t.Fatalf("recovery published into the detached result: %d", result)
	}
}

func TestOutboxBoundedCallRefusesAnOldEngineWithoutAFallback(t *testing.T) {
	f := &fakeEngine{missing: map[string]bool{"Admit": true}}
	o := NewOutbox(serveFake(t, f), "runner")
	o.Logf = t.Logf
	defer closeWithin(t, o)
	result := 29
	_, err := o.CallResultWithinID(t.Context(), &result, "Admit")
	var remote *RemoteError
	if !errors.As(err, &remote) || !remote.NoMethod() || result != 29 {
		t.Fatalf("old engine = %d, %v; want a final no-method refusal", result, err)
	}
	closeWithin(t, o)
	if got := f.seen(); !slices.Equal(got, []string{"Admit runner/1"}) {
		t.Fatalf("old-engine admission was retried: %v", got)
	}
}

type waitedWriteConn struct {
	net.Conn
	writing chan struct{}
	once    sync.Once
}

func (c *waitedWriteConn) Write(b []byte) (int, error) {
	c.once.Do(func() { close(c.writing) })
	return c.Conn.Write(b)
}

func TestOutboxBoundedCallCancelsSocketWriteAndRead(t *testing.T) {
	for _, stage := range []string{"write", "read"} {
		t.Run(stage, func(t *testing.T) {
			local, server := net.Pipe()
			defer server.Close()
			conn := &waitedWriteConn{Conn: local, writing: make(chan struct{})}
			client := noWait("")
			transport := &http.Transport{DisableKeepAlives: true, DialContext: func(context.Context, string, string) (net.Conn, error) { return conn, nil }}
			client.http.Transport = transport
			defer transport.CloseIdleConnections()
			o := NewOutbox(client, "runner")
			o.Spool = func([]Queued) error { return nil }
			defer func() { ctx, cancel := context.WithCancel(t.Context()); cancel(); o.Close(ctx) }()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := o.CallResultWithinID(ctx, nil, "Admit", "account")
				done <- err
			}()
			if stage == "read" {
				read := make(chan error, 1)
				go func() {
					req, err := http.ReadRequest(bufio.NewReader(server))
					if err == nil {
						_, err = io.Copy(io.Discard, req.Body)
						_ = req.Body.Close()
					}
					read <- err
				}()
				if err := awaitTerminal(t, read); err != nil {
					t.Fatal(err)
				}
			} else {
				awaitTerminal(t, conn.writing)
			}
			cancel()
			var uncertain *UnacknowledgedError
			if err := awaitTerminal(t, done); !errors.Is(err, context.Canceled) || !errors.As(err, &uncertain) || uncertain.RequestID != "runner/1" {
				t.Fatalf("cancel during socket %s = %v", stage, err)
			}
		})
	}
}

func TestOutboxBoundedCallCancelsTheEngineRestartWait(t *testing.T) {
	arrived := make(chan struct{})
	var once sync.Once
	client := noWait("")
	client.restartWait = time.Minute
	client.http.Transport = claimTransport(func(*http.Request) (*http.Response, error) {
		once.Do(func() { close(arrived) })
		return nil, ErrNotRunning
	})
	o := NewOutbox(client, "runner")
	o.Spool = func([]Queued) error { return nil }
	defer func() { ctx, cancel := context.WithCancel(t.Context()); cancel(); o.Close(ctx) }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := o.CallResultWithinID(ctx, nil, "Admit")
		done <- err
	}()
	awaitTerminal(t, arrived)
	cancel()
	var uncertain *UnacknowledgedError
	if err := awaitTerminal(t, done); !errors.Is(err, context.Canceled) || !errors.As(err, &uncertain) {
		t.Fatalf("cancel during engine restart wait = %v", err)
	}
}

func TestOutboxBoundedCallDoesNotJoinRetryBackoff(t *testing.T) {
	f := &fakeEngine{busy: map[string]bool{"runner/1": true}}
	o := NewOutbox(serveFake(t, f), "runner")
	entered, release := make(chan struct{}), make(chan struct{})
	free := sync.OnceFunc(func() { close(release) })
	// Hold the real sender at its retry delay, after the attempt settled.
	// Cancellation must release only the waiter, without joining this delay.
	o.retryAfter = func(time.Duration) <-chan time.Time {
		close(entered)
		<-release
		return nil
	}
	defer func() { free(); closeWithin(t, o) }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := o.CallResultWithinID(ctx, nil, "Admit")
		done <- err
	}()
	awaitTerminal(t, entered)
	o.Send("After")
	cancel()
	var uncertain *UnacknowledgedError
	if err := awaitTerminal(t, done); !errors.As(err, &uncertain) || uncertain.RequestID != "runner/1" {
		t.Fatalf("wait during retry backoff = %v", err)
	}
	free()
	closeWithin(t, o)
	if got := f.seen(); !slices.Equal(got, []string{"Admit runner/1", "Admit runner/1", "After runner/2"}) {
		t.Fatalf("backoff recovery changed the ID or order: %v", got)
	}
}

func TestOutboxBoundedCallCancelsUnsentInput(t *testing.T) {
	release := make(chan struct{})
	free := sync.OnceFunc(func() { close(release) })
	f := &fakeEngine{held: release, arrived: make(chan struct{})}
	o := NewOutbox(serveFake(t, f), "runner")
	defer func() { free(); closeWithin(t, o) }()
	o.Send("Before")
	awaitTerminal(t, f.arrived)
	ctx, cancel := context.WithCancel(t.Context())
	result := 29
	w := &waitedCall{cancelled: make(chan struct{}), target: reflect.ValueOf(&result), typeOf: reflect.TypeOf(result)}
	e := o.enqueue("Admit", nil, make(chan error, 1), nil, w)
	cancel()
	err := o.waitWithin(ctx, e)
	var uncertain *UnacknowledgedError
	if !errors.Is(err, context.Canceled) || errors.As(err, &uncertain) || result != 29 {
		t.Fatalf("unsent admission = %d, %v; want unchanged result and cancellation", result, err)
	}
	free()
	closeWithin(t, o)
	if got := f.seen(); !slices.Equal(got, []string{"Before runner/1"}) {
		t.Fatalf("canceled input reached the engine: %v", got)
	}
}

func TestOutboxBoundedCallReturnsBeforeBlockedSpoolIO(t *testing.T) {
	arrived := make(chan struct{})
	var first sync.Once
	client := noWait("")
	client.http.Transport = claimTransport(func(r *http.Request) (*http.Response, error) {
		first.Do(func() { close(arrived) })
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	o := NewOutbox(client, "runner")
	spooling, release := make(chan struct{}), make(chan struct{})
	free := sync.OnceFunc(func() { close(release) })
	var saved []Queued
	o.Spool = func(q []Queued) error {
		saved = append(saved, q...)
		close(spooling)
		<-release
		return nil
	}
	defer func() { free(); closeWithin(t, o) }()
	o.Send("Before")
	awaitTerminal(t, arrived)
	result := 29
	w := &waitedCall{cancelled: make(chan struct{}), target: reflect.ValueOf(&result), typeOf: reflect.TypeOf(result)}
	e := o.enqueue("Admit", nil, make(chan error, 1), nil, w)
	terminal, expire := context.WithCancel(t.Context())
	o.ArmTerminal(terminal)
	expire()
	awaitTerminal(t, spooling)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() { done <- o.waitWithin(ctx, e) }()
	var uncertain *UnacknowledgedError
	if err := awaitTerminal(t, done); !errors.As(err, &uncertain) || uncertain.RequestID != "runner/2" || result != 29 {
		t.Fatalf("wait behind spool I/O = %d, %v", result, err)
	}
	free()
	closeWithin(t, o)
	if len(saved) != 2 || saved[1].ID != uncertain.RequestID {
		t.Fatalf("spool did not keep the detached request: %+v", saved)
	}
}

func TestOutboxBoundedCallJoinsDecoderAndRecoversTheOriginalID(t *testing.T) {
	body := &terminalBody{reading: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	freeBody := sync.OnceFunc(func() { close(body.release) })
	retried, releaseRetry := make(chan struct{}), make(chan struct{})
	freeRetry := sync.OnceFunc(func() { close(releaseRetry) })
	var mu sync.Mutex
	var calls []string
	client := noWait("")
	client.http.Transport = claimTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		calls = append(calls, strings.TrimPrefix(r.URL.Path, "/api/")+" "+r.Header.Get(callspec.RequestHeader))
		n := len(calls)
		mu.Unlock()
		if n == 1 {
			// The engine has committed this admission. Its response stalls.
			body.ctx = r.Context()
			return &http.Response{StatusCode: http.StatusOK, Body: body, Header: http.Header{}}, nil
		}
		if n == 2 {
			select {
			case <-body.closed:
			default:
				t.Error("recovery started before the first decoder closed")
			}
			close(retried)
			<-releaseRetry
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`17`)), Header: http.Header{}}, nil
	})
	o := NewOutbox(client, "runner")
	defer func() { freeBody(); freeRetry(); closeWithin(t, o) }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := 29
	done := make(chan error, 1)
	go func() {
		_, err := o.CallResultWithinID(ctx, &result, "Admit", "account")
		done <- err
	}()
	awaitTerminal(t, body.reading)
	o.Send("After")
	cancel()
	awaitTerminal(t, body.cancelled)
	select {
	case err := <-done:
		t.Fatalf("wait returned before decoder settlement: %v", err)
	default:
	}
	freeBody()
	err := awaitTerminal(t, done)
	var uncertain *UnacknowledgedError
	if !errors.Is(err, context.Canceled) || !errors.As(err, &uncertain) || uncertain.RequestID != "runner/1" || result != 29 {
		t.Fatalf("sent admission = %d, %v; want an uncertain runner/1 and unchanged result", result, err)
	}
	awaitTerminal(t, retried)
	mu.Lock()
	before := slices.Clone(calls)
	mu.Unlock()
	if !slices.Equal(before, []string{"Admit runner/1", "Admit runner/1"}) {
		t.Fatalf("unresolved admission was passed or changed ID: %v", before)
	}
	if !o.DisarmTerminal() {
		t.Fatal("ordinary cancellation put the outbox into terminal mode")
	}
	freeRetry()
	closeWithin(t, o)
	if result != 29 {
		t.Fatalf("late recovery changed the returned result to %d", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(calls, []string{"Admit runner/1", "Admit runner/1", "After runner/2"}) {
		t.Fatalf("recovery order = %v", calls)
	}
}

func TestOutboxBoundedCallSpoolsTheOriginalUncertainRequest(t *testing.T) {
	started, settled := make(chan struct{}), make(chan struct{})
	var first sync.Once
	client := noWait("")
	client.http.Transport = claimTransport(func(r *http.Request) (*http.Response, error) {
		first.Do(func() { close(started) })
		<-r.Context().Done()
		select {
		case <-settled:
		default:
			close(settled)
		}
		return nil, r.Context().Err()
	})
	o := NewOutbox(client, "runner")
	var saved []Queued
	o.Spool = func(q []Queued) error {
		select {
		case <-settled:
		default:
			t.Error("spool ran before the sender settled")
		}
		saved = append(saved, q...)
		return nil
	}
	defer func() {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		o.Close(ctx)
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := o.CallResultWithinID(ctx, nil, "Admit", "account")
		done <- err
	}()
	awaitTerminal(t, started)
	o.Send("TurnEnd")
	cancel()
	err := awaitTerminal(t, done)
	var uncertain *UnacknowledgedError
	if !errors.As(err, &uncertain) || uncertain.RequestID != "runner/1" {
		t.Fatalf("uncertain request = %v", err)
	}
	closed, stop := context.WithCancel(t.Context())
	stop()
	o.Close(closed)
	if len(saved) != 2 || saved[0].ID != uncertain.RequestID || saved[0].Method != "Admit" || fmt.Sprint(saved[0].Args) != "[account]" || saved[1].ID != "runner/2" || saved[1].Method != "TurnEnd" {
		t.Fatalf("spooled requests = %+v; want the original admission before TurnEnd", saved)
	}
}
