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
	ticks := make(chan time.Time)
	notices := make(chan bool, 8)
	starts := make(chan chan error, 2)
	stopped := make(chan struct{})
	missing := true
	h := tunnelHooks{
		now:   func() time.Time { return time.Unix(1, 0) },
		after: func(time.Duration) <-chan time.Time { return ticks },
		find: func() (string, error) {
			if missing {
				return "", errors.New("missing")
			}
			return "fake", nil
		},
		prepare: func(context.Context) (string, error) { return "invented", nil },
		start: func(ctx context.Context, _, _ string) <-chan error {
			done := make(chan error, 1)
			starts <- done
			go func() {
				<-ctx.Done()
				select {
				case done <- nil:
				default:
				}
			}()
			return done
		},
	}
	go func() { defer close(stopped); superviseTunnel(ctx, fakeTunnelState{notices}, true, wake, h) }()
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
	ticks <- time.Time{}
	if <-notices {
		t.Fatal("connector unexpectedly missing")
	}
	<-starts
	cancel()
	<-stopped
}
