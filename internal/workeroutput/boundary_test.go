package workeroutput

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

type boundaryWriter struct {
	bytes.Buffer
	short bool
}

func (w *boundaryWriter) Seek(offset int64, whence int) (int64, error) { return int64(w.Len()), nil }
func (w *boundaryWriter) ReadAt(p []byte, off int64) (int, error) {
	return bytes.NewReader(w.Bytes()).ReadAt(p, off)
}
func (w *boundaryWriter) Write(p []byte) (int, error) {
	if w.short {
		return w.Buffer.Write(p[:len(p)/2])
	}
	return w.Buffer.Write(p)
}

func TestPartialWriteMakesBoundaryUnknown(t *testing.T) {
	w := &boundaryWriter{}
	n := NewNormalizer(nil, w)
	n.Now = func() time.Time { return time.Unix(0, 0) }
	boundary, hasBoundary := any(n).(interface{ Boundary() (int64, bool) })
	var initial int64
	var initialKnown bool
	if hasBoundary {
		initial, initialKnown = boundary.Boundary()
	}
	if err := n.WriteLine("first"); err != nil {
		t.Fatal(err)
	}
	end := int64(w.Len())
	var complete int64
	var completeKnown bool
	if hasBoundary {
		complete, completeKnown = boundary.Boundary()
	}
	w.short = true
	if err := n.WriteLine("second"); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v; want io.ErrShortWrite", err)
	}
	if !hasBoundary {
		t.Fatal("normalizer has no Boundary method")
	}
	if initial != 0 || !initialKnown {
		t.Fatalf("initial boundary = %d, %v", initial, initialKnown)
	}
	if complete != end || !completeKnown {
		t.Fatalf("complete boundary = %d, %v; want %d, true", complete, completeKnown, end)
	}
	if _, known := boundary.Boundary(); known {
		t.Fatal("partial write left a known boundary")
	}
}
