package podman

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"github.com/klauspost/compress/zstd"
	"io"
	"sync"
	"time"
)

type BinaryEncoding string

const (
	BinaryGzip BinaryEncoding = "gzip"
	BinaryZstd BinaryEncoding = "zstd"
)

// CompressBinary creates a reusable transfer representation. zstd is the existing in-process encoder at its best ratio
// (the library's zstd level >= 10 mapping), one worker and an 8 MiB window; it requires no external encoder executable.
// Hashes always describe the uncompressed executable. gzip is retained for existing hosts without zstd.
func CompressBinary(raw []byte, encoding BinaryEncoding) ([]byte, error) {
	if encoding == BinaryZstd {
		z, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(8<<20), zstd.WithLowerEncoderMem(true))
		if err != nil {
			return nil, err
		}
		packed := z.EncodeAll(raw, nil)
		if err := z.Close(); err != nil {
			return nil, err
		}
		// EncodeAll reserves for incompressible input. Do not retain that worst-case capacity in the per-binary cache.
		return bytes.Clone(packed), nil
	}
	if encoding != BinaryGzip {
		return nil, errors.New("unsupported binary encoding")
	}
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	if _, err := z.Write(raw); err != nil {
		return nil, err
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// BinaryCache retains one immutable compressed payload per actual executable and encoding, lazily. The fallback does not
// consume CPU or memory on hosts that have zstd. Separate Once values let all hosts share the same compression work.
type BinaryCache struct {
	read       func() ([]byte, error)
	gzip, zstd binaryPayload
}
type binaryPayload struct {
	once sync.Once
	data []byte
	err  error
}

func NewBinaryCache(read func() ([]byte, error)) *BinaryCache { return &BinaryCache{read: read} }
func (c *BinaryCache) Bytes(encoding BinaryEncoding) ([]byte, error) {
	payload := &c.gzip
	if encoding == BinaryZstd {
		payload = &c.zstd
	}
	payload.once.Do(func() {
		raw, err := c.read()
		if err != nil {
			payload.err = err
			return
		}
		payload.data, payload.err = CompressBinary(raw, encoding)
	})
	return payload.data, payload.err
}

type byteProgressKey struct{}

// WithByteProgress observes payload bytes successfully written into SSH's stdin pipe, not source reads or framing.
// Acceptance into this local transport is not proof of child reads or remote receipt.
// This is transport progress, not a remote acknowledgement; verification still bounds the final idle wait.
func WithByteProgress(ctx context.Context, progress func(int)) context.Context {
	return context.WithValue(ctx, byteProgressKey{}, progress)
}

// ProgressRunner is the cancellable SSH streaming boundary used by binary transfers. Implementations must join their process
// and I/O workers on cancellation. Runners without it still work but cannot extend a transfer's idle budget.
type ProgressRunner interface {
	RunProgress(context.Context, []byte, func(int), ...string) ([]byte, error)
}

type progressReader struct {
	io.Reader
	skip     int
	progress func(int)
}

// os/exec copies non-file stdin with io.Copy. WriterTo puts accounting on its
// destination pipe, after Write returns, rather than on speculative source reads.
func (r *progressReader) WriteTo(dst io.Writer) (int64, error) {
	w := &progressWriter{Writer: dst, skip: r.skip, progress: r.progress}
	// Hide source WriterTo so a bytes.Reader cannot issue one payload-sized write:
	// bounded writes must report progress while a large transfer is still running.
	n, err := io.Copy(w, struct{ io.Reader }{r.Reader})
	r.skip = w.skip
	return n, err
}

type progressWriter struct {
	io.Writer
	skip     int
	progress func(int)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	skip := min(n, w.skip)
	w.skip -= skip
	if n > skip {
		w.progress(n - skip)
	}
	return n, err
}

var ErrTransferIdle = errors.New("binary transfer made no byte progress")

// IdleContext owns its timer and joins its only worker. Transfer callbacks merely send a coalesced byte event.
func IdleContext(parent context.Context, budget time.Duration, progress <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(budget)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-progress:
				timer.Reset(budget)
			case <-timer.C:
				cancel(ErrTransferIdle)
				return
			}
		}
	}()
	var once sync.Once
	return ctx, func() { once.Do(func() { cancel(context.Canceled); <-done }) }
}
