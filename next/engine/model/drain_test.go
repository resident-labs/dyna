package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDrainWaitsForCallsAndLeavesNewCallsUnsent(t *testing.T) {
	d := NewDrain()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h := d.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { close(entered); <-release }))
	go func() { h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil)); close(finished) }()
	<-entered
	zero := d.Begin()
	select {
	case <-zero:
		t.Fatal("drained before call finished")
	default:
	}
	ctx, cancel := context.WithCancel(context.Background())
	blocked := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil).WithContext(ctx))
		close(blocked)
	}()
	cancel()
	<-blocked
	select {
	case <-zero:
		t.Fatal("drained before call finished")
	default:
	}
	close(release)
	<-finished
	<-zero
}
