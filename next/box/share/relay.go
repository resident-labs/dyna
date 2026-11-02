package share

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"time"
)

const (
	relayRetry = 250 * time.Millisecond
	// holdServer is how long one try waits for the server. A listing expires every few seconds and a refused socket fails a
	// call at once, so without this a box would get I/O errors for as long as the forward is away, as in an engine restart.
	// rclone tries a call 10 times, so a box's call waits about 5 minutes, as a model call does (spec 5.7).
	holdServer = 30 * time.Second
)

// relay listens on a socket of its own and connects each caller to the server, waiting for the server for as long as hold.
func relay(ctx context.Context, listen, server string, hold time.Duration) error {
	if err := os.Remove(listen); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", listen)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		listener.Close()
	}()
	go func() {
		for {
			caller, err := listener.Accept()
			if err != nil {
				return
			}
			go carry(ctx, caller.(*net.UnixConn), server, hold)
		}
	}()
	return nil
}

func carry(ctx context.Context, caller *net.UnixConn, server string, hold time.Duration) {
	defer caller.Close()
	upstream, err := reach(ctx, server, hold)
	if err != nil {
		return
	}
	defer upstream.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(upstream, caller)
		_ = upstream.CloseWrite()
	}()
	_, _ = io.Copy(caller, upstream)
	_ = caller.CloseWrite()
	<-done
}

func reach(ctx context.Context, server string, hold time.Duration) (*net.UnixConn, error) {
	deadline := time.Now().Add(hold)
	for {
		conn, err := net.Dial("unix", server)
		if err == nil {
			return conn.(*net.UnixConn), nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(relayRetry):
		}
	}
}
