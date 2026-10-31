package model

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSupervisorBackoffAndStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	starts := make(chan chan error)
	delays := make(chan time.Duration)
	tick := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		supervise(ctx, func() time.Time { return time.Time{} }, func(d time.Duration) <-chan time.Time { delays <- d; return tick }, func() <-chan error { c := make(chan error, 1); starts <- c; return c })
	}()
	first := <-starts
	first <- errors.New("crash")
	if d := <-delays; d != time.Second {
		t.Fatalf("first delay %v", d)
	}
	tick <- time.Time{}
	second := <-starts
	second <- errors.New("crash")
	if d := <-delays; d != 2*time.Second {
		t.Fatalf("second delay %v", d)
	}
	cancel()
	<-done
}
