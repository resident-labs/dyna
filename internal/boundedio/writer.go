// Package boundedio provides a bounded in-memory writer for subprocess output.
//
// A Writer always consumes its full input and drains its reader to EOF, so a
// child process never blocks on a full pipe once the limit is reached.
package boundedio

import (
	"bytes"
	"io"
)

// Writer keeps at most limit bytes of everything written to it. New keeps the
// first limit bytes and drops the rest. NewTail keeps the last limit bytes and
// drops what scrolls off the front. Truncated reports whether anything was
// dropped. Bytes and Len expose the kept bytes.
type Writer struct {
	buf       bytes.Buffer
	limit     int
	tail      bool
	truncated bool
}

// New returns a limit-mode Writer that keeps the first limit bytes.
func New(limit int) *Writer { return &Writer{limit: limit} }

// NewTail returns a tail-mode Writer that keeps the last limit bytes.
func NewTail(limit int) *Writer { return &Writer{limit: limit, tail: true} }

// Write keeps what fits and reports the whole input as written. Reporting the
// full length keeps io.Copy draining the stream to EOF after the limit fills.
func (w *Writer) Write(p []byte) (int, error) {
	n := len(p)
	if w.tail {
		raw := append(w.buf.Bytes(), p...)
		if len(raw) > w.limit {
			raw = raw[len(raw)-w.limit:]
			w.truncated = true
		}
		w.buf.Reset()
		_, _ = w.buf.Write(raw)
		return n, nil
	}
	left := w.limit - w.buf.Len()
	if len(p) > left {
		p = p[:max(0, left)]
		w.truncated = true
	}
	_, _ = w.buf.Write(p)
	return n, nil
}

// ReadFrom drains input to EOF through Write. It also hides io.ReaderFrom from
// io.Copy, which would otherwise bypass the limit and the tail.
func (w *Writer) ReadFrom(input io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{w}, input)
}

// Bytes returns the kept bytes. The slice is valid until the next write.
func (w *Writer) Bytes() []byte { return w.buf.Bytes() }

// Len returns the number of kept bytes.
func (w *Writer) Len() int { return w.buf.Len() }

// Truncated reports whether any input was dropped.
func (w *Writer) Truncated() bool { return w.truncated }
