package harness

import (
	"bytes"
	"io"
	"strings"
	"sync"
)

// observedBuffer retains stdout while forwarding complete JSONL records to a
// session-id observer as they arrive. String is safe after or during writes.
type observedBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	pending string
	onLine  func(string)
	onBytes func()
	live    io.Writer
}

func newObservedBuffer(onLine func(string), live io.Writer) *observedBuffer {
	return &observedBuffer{onLine: onLine, live: live}
}

func (b *observedBuffer) Write(p []byte) (int, error) {
	if len(p) > 0 && b.onBytes != nil {
		b.onBytes()
	}
	if b.live != nil {
		_, _ = b.live.Write(p)
	}
	b.mu.Lock()
	n, err := b.buf.Write(p)
	combined := b.pending + string(p)
	parts := strings.Split(combined, "\n")
	b.pending = parts[len(parts)-1]
	lines := append([]string(nil), parts[:len(parts)-1]...)
	b.mu.Unlock()
	if b.onLine != nil {
		for _, line := range lines {
			b.onLine(line)
		}
	}
	return n, err
}

func (b *observedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// FlushPending forwards a final non-newline-terminated record to the supplied
// side channel. It deliberately does not invoke the live session observer or
// alter retained stdout/raw capture.
func (b *observedBuffer) FlushPending(onPending func(string)) {
	b.mu.Lock()
	line := b.pending
	b.pending = ""
	b.mu.Unlock()
	if line != "" && onPending != nil {
		onPending(line)
	}
}

// activityWriter forwards stderr without buffering or changing its contents.
type activityWriter struct {
	out    io.Writer
	notify func()
}

func (w activityWriter) Write(p []byte) (int, error) {
	if len(p) > 0 && w.notify != nil {
		w.notify()
	}
	return w.out.Write(p)
}
