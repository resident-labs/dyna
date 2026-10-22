package engineclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func awaitTerminal[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case value := <-ch:
		return value
	case <-timer.C:
		t.Fatal("outbox event did not arrive")
		var zero T
		return zero
	}
}

func TestOutboxTerminalExpirySpoolsLateFinalWrites(t *testing.T) {
	for _, absent := range []bool{true, false} {
		t.Run(fmt.Sprintf("absent=%v", absent), func(t *testing.T) {
			arrived := make(chan struct{})
			var first sync.Once
			client := noWait("")
			client.http.Transport = claimTransport(func(r *http.Request) (*http.Response, error) {
				first.Do(func() { close(arrived) })
				if absent {
					return nil, ErrNotRunning
				}
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(`{"error":"read-only"}`)), Header: http.Header{}}, nil
			})
			o := NewOutbox(client, "session/turn")
			o.Logf = t.Logf
			entered, release := make(chan struct{}), make(chan struct{})
			var batches [][]Queued
			o.Spool = func(q []Queued) error {
				batches = append(batches, slices.Clone(q))
				if len(batches) == 1 {
					close(entered)
					<-release
				}
				return nil
			}
			terminal, expire := context.WithCancel(t.Context())
			defer expire()
			o.ArmTerminal(terminal)
			// A second finalization step must keep the first deadline.
			o.ArmTerminal(t.Context())
			result := "unchanged"
			done := make(chan error, 1)
			go func() { done <- o.CallResult(t.Context(), &result, "Claim") }()
			awaitTerminal(t, arrived)
			o.Send("TurnEnd")
			expire()
			awaitTerminal(t, entered)
			select {
			case err := <-done:
				t.Fatalf("caller returned before durable spool: %v", err)
			default:
			}
			close(release)
			if err := awaitTerminal(t, done); !errors.Is(err, ErrSpooled) {
				t.Fatalf("claim error = %v, want ErrSpooled", err)
			}
			if result != "unchanged" {
				t.Fatalf("unanswered claim changed result to %q", result)
			}
			if err := o.Call(t.Context(), "Usage"); !errors.Is(err, ErrSpooled) {
				t.Fatalf("late final write = %v, want ErrSpooled", err)
			}
			if err := o.Flush(t.Context()); !errors.Is(err, ErrSpooled) {
				t.Fatalf("Flush = %v, want ErrSpooled", err)
			}
			o.Close(t.Context())
			var got []string
			for _, q := range batches {
				for _, entry := range q {
					got = append(got, entry.Method+" "+entry.ID)
				}
			}
			want := []string{"Claim session/turn/1", "TurnEnd session/turn/2", "Usage session/turn/3"}
			if !slices.Equal(got, want) {
				t.Fatalf("spool = %v, want %v", got, want)
			}
		})
	}
}

type terminalBody struct {
	ctx                         context.Context
	reading, cancelled, release chan struct{}
	closed                      chan struct{}
}

func (b *terminalBody) Read([]byte) (int, error) {
	close(b.reading)
	<-b.ctx.Done()
	close(b.cancelled)
	<-b.release
	return 0, b.ctx.Err()
}

func (b *terminalBody) Close() error { close(b.closed); return nil }

func TestOutboxTerminalExpiryJoinsDecoderBeforeSpooling(t *testing.T) {
	body := &terminalBody{reading: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	client := noWait("")
	client.http.Transport = claimTransport(func(r *http.Request) (*http.Response, error) {
		body.ctx = r.Context()
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: http.Header{}}, nil
	})
	o := NewOutbox(client, "runner")
	o.Spool = func([]Queued) error {
		select {
		case <-body.closed:
		default:
			t.Error("spool started before the sender closed its result body")
		}
		return nil
	}
	terminal, expire := context.WithCancel(t.Context())
	defer expire()
	o.ArmTerminal(terminal)
	result := 23
	done := make(chan error, 1)
	go func() { done <- o.CallResult(t.Context(), &result, "Claim") }()
	awaitTerminal(t, body.reading)
	expire()
	awaitTerminal(t, body.cancelled)
	select {
	case err := <-done:
		t.Fatalf("caller returned while decoder still runs: %v", err)
	default:
	}
	close(body.release)
	if err := awaitTerminal(t, done); !errors.Is(err, ErrSpooled) {
		t.Fatalf("claim = %v, want ErrSpooled", err)
	}
	o.Close(t.Context())
	if result != 23 {
		t.Fatalf("failed decoder changed result to %d", result)
	}
}

func TestOutboxFlushKeepsQueueOpenForCompletion(t *testing.T) {
	f := &fakeEngine{held: make(chan struct{}), arrived: make(chan struct{})}
	release := f.held
	o := NewOutbox(serveFake(t, f), "runner")
	o.Send("TurnEnd")
	awaitTerminal(t, f.arrived)
	done := make(chan error, 2)
	for range 2 {
		go func() { done <- o.Flush(t.Context()) }()
	}
	close(release)
	for range 2 {
		if err := awaitTerminal(t, done); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.Call(t.Context(), "Complete"); err != nil {
		t.Fatal(err)
	}
	closeWithin(t, o)
	if got := f.seen(); !slices.Equal(got, []string{"TurnEnd runner/1", "Complete runner/2"}) {
		t.Fatalf("engine saw %v", got)
	}
}

func TestOutboxContinuationCanDisarmOnlyBeforeExpiry(t *testing.T) {
	o := NewOutbox(serveFake(t, &fakeEngine{}), "runner")
	terminal, expire := context.WithCancel(t.Context())
	defer expire()
	o.ArmTerminal(terminal)
	if !o.DisarmTerminal() {
		t.Fatal("could not disarm an active scope")
	}
	expire()
	if err := o.Call(t.Context(), "Continued"); err != nil {
		t.Fatal(err)
	}
	expired, cancel := context.WithCancel(t.Context())
	cancel()
	o.Spool = func([]Queued) error { return nil }
	o.ArmTerminal(expired)
	if o.DisarmTerminal() {
		t.Fatal("disarmed an expired scope")
	}
	if err := o.Call(t.Context(), "TurnEnd"); !errors.Is(err, ErrSpooled) {
		t.Fatalf("expired scope resumed engine writes: %v", err)
	}
	o.Close(t.Context())
}

func TestOutboxFlushReportsSpoolFailure(t *testing.T) {
	o := NewOutbox(noWait(""), "runner")
	o.Logf = t.Logf
	diskErr := errors.New("disk full")
	o.Spool = func([]Queued) error { return diskErr }
	terminal, expire := context.WithCancel(t.Context())
	expire()
	o.ArmTerminal(terminal)
	if err := o.Call(t.Context(), "TurnEnd"); !errors.Is(err, diskErr) || errors.Is(err, ErrSpooled) {
		t.Fatalf("write = %v, want spool persistence error", err)
	}
	if err := o.Flush(t.Context()); !errors.Is(err, diskErr) {
		t.Fatalf("Flush lost spool failure: %v", err)
	}
	o.Close(t.Context())
}
