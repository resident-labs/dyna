package podman

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestTransferIdleUsesByteEventsNotWallTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		progress := make(chan struct{}, 1)
		ctx, stop := IdleContext(context.Background(), 3*time.Minute, progress)
		defer stop()
		// Eighteen minutes exceeds the former whole-create deadline while remaining below each idle budget.
		for range 9 {
			time.Sleep(2 * time.Minute)
			progress <- struct{}{}
			synctest.Wait()
			if ctx.Err() != nil {
				t.Fatalf("progressing transfer timed out: %v", context.Cause(ctx))
			}
		}
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		if !errors.Is(context.Cause(ctx), ErrTransferIdle) {
			t.Fatalf("idle: %v", context.Cause(ctx))
		}
	})
}

func TestProgressReaderCountsPayloadNotFraming(t *testing.T) {
	counts := 0
	r := &progressReader{Reader: &oneByteReader{left: 35}, skip: 29, progress: func(n int) { counts += n }}
	for counts < 6 {
		buf := make([]byte, 1)
		_, _ = r.Read(buf)
	}
	if counts != 6 {
		t.Fatalf("payload bytes %d", counts)
	}
}

type oneByteReader struct{ left int }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, nil
	}
	r.left--
	p[0] = 'x'
	return 1, nil
}
