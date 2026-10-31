package main

import (
	"errors"
	"net"
	"testing"
	"testing/synctest"
)

type proxyTestListener struct{ called chan struct{} }

func (l proxyTestListener) Accept() (net.Conn, error) { close(l.called); return nil, net.ErrClosed }
func (proxyTestListener) Close() error                { return nil }
func (proxyTestListener) Addr() net.Addr              { return nil }

func TestProxyAcceptWaitsForReadinessAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ready, done, called := make(chan struct{}), make(chan struct{}), make(chan struct{})
		l := &readyListener{Listener: proxyTestListener{called}, ready: ready, done: done}
		result := make(chan error, 1)
		go func() { _, err := l.Accept(); result <- err }()
		synctest.Wait()
		select {
		case <-called:
			t.Fatal("accepted before models were ready")
		default:
		}
		close(ready)
		synctest.Wait()
		if err := <-result; !errors.Is(err, net.ErrClosed) {
			t.Fatalf("accept: %v", err)
		}
		l = &readyListener{Listener: proxyTestListener{make(chan struct{})}, ready: make(chan struct{}), done: done}
		go func() { _, err := l.Accept(); result <- err }()
		synctest.Wait()
		close(done)
		synctest.Wait()
		if err := <-result; !errors.Is(err, net.ErrClosed) {
			t.Fatalf("shutdown: %v", err)
		}
	})
}
