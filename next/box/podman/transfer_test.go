package podman

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
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

func TestProgressWriterCountsPayloadNotFraming(t *testing.T) {
	// Exercise writes before, exactly at, and straddling the 29-byte framing boundary.
	for _, chunk := range []int{1, 4, 29, 30, 35, 64} {
		t.Run(strconv.Itoa(chunk), func(t *testing.T) {
			counts := 0
			w := &progressWriter{Writer: io.Discard, skip: 29, progress: func(n int) { counts += n }}
			for remaining := 35; remaining > 0; {
				n, err := w.Write(make([]byte, min(chunk, remaining)))
				if err != nil || n == 0 {
					t.Fatalf("wrote %d bytes: %v", n, err)
				}
				remaining -= n
			}
			if counts != 6 {
				t.Fatalf("payload bytes %d, wanted 6", counts)
			}
		})
	}
}

type progressWriteFunc func([]byte) (int, error)

func (f progressWriteFunc) Write(p []byte) (int, error) { return f(p) }

func TestProgressWriterCountsOnlySuccessfulWrites(t *testing.T) {
	for _, accepted := range []int{0, 7, 29, 31, 35} {
		t.Run(strconv.Itoa(accepted), func(t *testing.T) {
			counts, callbacks := 0, 0
			returned := false
			w := &progressWriter{
				Writer: progressWriteFunc(func([]byte) (int, error) {
					if counts != 0 {
						t.Error("credited bytes before the transport write")
					}
					returned = true
					return accepted, io.ErrClosedPipe
				}),
				skip: 29,
				progress: func(n int) {
					if !returned || n <= 0 {
						t.Errorf("invalid progress: returned=%v, bytes=%d", returned, n)
					}
					counts += n
					callbacks++
				},
			}
			n, err := w.Write(make([]byte, 35))
			if n != accepted || err != io.ErrClosedPipe || counts != max(0, accepted-29) || w.skip != max(0, 29-accepted) {
				t.Fatalf("accepted=%d: write=(%d, %v), progress=%d, skip=%d", accepted, n, err, counts, w.skip)
			}
			if accepted <= 29 && callbacks != 0 {
				t.Fatalf("header-only write reported %d callbacks", callbacks)
			}
		})
	}
}

func TestProgressReaderReportsDuringCopy(t *testing.T) {
	// Both source and destination offer io.Copy fast paths. They must not bypass
	// accounting or turn the entire transfer into a single delayed progress event.
	source := bytes.NewReader(make([]byte, 1<<20))
	counts, events, during := 0, 0, 0
	r := &progressReader{Reader: source, skip: 29, progress: func(n int) {
		counts += n
		events++
		if source.Len() > 0 {
			during++
		}
	}}
	n, err := io.Copy(io.Discard, r)
	if err != nil || n != 1<<20 || counts != (1<<20)-29 || events < 2 || during == 0 {
		t.Fatalf("copy=(%d, %v), progress=%d, events=%d, events before EOF=%d", n, err, counts, events, during)
	}
}
