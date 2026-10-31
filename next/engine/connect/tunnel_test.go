package connect

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeTunnelState struct{ notices chan bool }

func (f fakeTunnelState) ConnectLinked(context.Context) (bool, error) { return true, nil }
func (f fakeTunnelState) SetConnectConnector(_ context.Context, missing bool, _ time.Time) error {
	f.notices <- missing
	return nil
}

func TestTunnelRestartsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	wake := make(chan struct{}, 1)
	notices := make(chan bool, 8)
	starts := make(chan chan error, 2)
	stopped := make(chan struct{})
	missing := true
	h := tunnelHooks{
		now: func() time.Time { return time.Unix(1, 0) },
		find: func() (string, error) {
			if missing {
				return "", errors.New("missing")
			}
			return "fake", nil
		},
		prepare: func(context.Context) (string, error) { return "invented", nil },
		retry:   func() {},
		start: func(context.Context, string, string) (context.CancelFunc, <-chan error, error) {
			done := make(chan error, 1)
			starts <- done
			return func() {
				select {
				case done <- nil:
				default:
				}
			}, done, nil
		},
	}
	go func() { defer close(stopped); superviseTunnel(ctx, fakeTunnelState{notices}, true, wake, h) }()
	wake <- struct{}{}
	if !<-notices {
		t.Fatal("missing connector was not reported")
	}
	missing = false
	wake <- struct{}{}
	if <-notices {
		t.Fatal("connector alert was not cleared")
	}
	first := <-starts
	first <- errors.New("connector stopped")
	if <-notices {
		t.Fatal("connector unexpectedly missing")
	}
	<-starts
	cancel()
	<-stopped
}
