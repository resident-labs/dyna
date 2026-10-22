//go:build linux

package computers

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type outputDamageEvent struct {
	id   uint32
	op   uint16
	body []byte
	err  error
}

// outputDamage keeps one manager alive so successive captures share damage
// history. Call wait serially. Output damage does not prove app readiness.
type outputDamage struct {
	w                             *wayland
	manager, output, shm          uint32
	buffer                        uint32
	format, width, height, stride uint32
	fd                            int
	events                        chan outputDamageEvent
	done                          chan struct{}
	closed                        sync.Once
}

func newOutputDamage(ctx context.Context, display string) (*outputDamage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w, err := connectWayland(ctx, display)
	if err != nil {
		return nil, err
	}
	d := &outputDamage{w: w, fd: -1, events: make(chan outputDamageEvent, 16), done: make(chan struct{})}
	ok := false
	defer func() {
		if !ok {
			d.close()
		}
	}()
	if g, exists := w.globals["zwlr_screencopy_manager_v1"]; !exists || g.version < 2 {
		return nil, fmt.Errorf("output damage requires screencopy version 2")
	}
	// Do not use roundtrip after binding: its dispatcher assigns special
	// meanings to object IDs. This client routes every event by its own IDs.
	d.manager, d.output, d.shm = w.id(), w.id(), w.id()
	for _, binding := range []struct {
		name        string
		id, version uint32
	}{
		{"zwlr_screencopy_manager_v1", d.manager, 2},
		{"wl_output", d.output, 1},
		{"wl_shm", d.shm, 1},
	} {
		if err := w.bind(binding.name, binding.id, binding.version); err != nil {
			return nil, err
		}
	}
	if err := w.conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	go d.readEvents()
	baseline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Ordinary copy does not initialize/clear wlroots damage history.
	if err := d.capture(baseline); err != nil {
		return nil, fmt.Errorf("output damage baseline: %w", err)
	}
	ok = true
	return d, nil
}

func (d *outputDamage) readEvents() {
	defer close(d.events)
	for {
		id, op, body, err := d.w.read()
		select {
		case d.events <- outputDamageEvent{id, op, body, err}:
		case <-d.done:
			return
		}
		if err != nil {
			return
		}
	}
}

func (d *outputDamage) next(ctx context.Context) (outputDamageEvent, error) {
	select {
	case <-ctx.Done():
		return outputDamageEvent{}, ctx.Err()
	case <-d.done:
		return outputDamageEvent{}, net.ErrClosed
	case event, ok := <-d.events:
		if !ok {
			return outputDamageEvent{}, net.ErrClosed
		}
		if event.err != nil {
			return event, event.err
		}
		if event.id == 1 && event.op == 0 {
			return event, fmt.Errorf("Wayland protocol error: %q", event.body)
		}
		return event, nil
	}
}

func (d *outputDamage) prepareBuffer(body []byte) error {
	if len(body) != 16 {
		return fmt.Errorf("invalid screencopy buffer description")
	}
	format := binary.NativeEndian.Uint32(body)
	width := binary.NativeEndian.Uint32(body[4:])
	height := binary.NativeEndian.Uint32(body[8:])
	stride := binary.NativeEndian.Uint32(body[12:])
	size := uint64(stride) * uint64(height)
	if width == 0 || height == 0 || stride == 0 || width > 1<<31-1 || height > 1<<31-1 || size > 1<<31-1 {
		return fmt.Errorf("invalid screencopy buffer dimensions")
	}
	if d.buffer != 0 && d.format == format && d.width == width && d.height == height && d.stride == stride {
		return nil
	}
	if d.buffer != 0 {
		if err := d.w.send(d.buffer, 0, nil); err != nil {
			return err
		}
		d.buffer = 0
	}
	if d.fd >= 0 {
		unix.Close(d.fd)
		d.fd = -1
	}
	fd, err := unix.MemfdCreate("dyna-output-damage", unix.MFD_CLOEXEC)
	if err != nil {
		return err
	}
	d.fd = fd
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		return err
	}
	pool := d.w.id()
	message := append(wlWords(d.shm, uint32(16)<<16), wlWords(pool, uint32(size))...)
	n, oobn, err := d.w.conn.WriteMsgUnix(message, unix.UnixRights(fd), nil)
	if err != nil {
		return err
	}
	if oobn != len(unix.UnixRights(fd)) {
		return io.ErrShortWrite
	}
	for n < len(message) {
		written, err := d.w.conn.Write(message[n:])
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		n += written
	}
	defer d.w.send(pool, 1, nil)
	buffer := d.w.id()
	if err := d.w.send(pool, 0, wlWords(buffer, 0, width, height, stride, format)); err != nil {
		return err
	}
	d.buffer = buffer
	d.format, d.width, d.height, d.stride = format, width, height, stride
	return nil
}

func (d *outputDamage) capture(ctx context.Context) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-d.done:
		return net.ErrClosed
	default:
	}
	deadline := time.Now().Add(2 * time.Second)
	if t, ok := ctx.Deadline(); ok && t.Before(deadline) {
		deadline = t
	}
	if err := d.w.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	// Wake blocked writes on cancellation without interrupting the persistent
	// reader halfway through a Wayland message.
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { d.w.conn.SetWriteDeadline(time.Now()); close(interrupted) })
	frame := d.w.id()
	created := false
	defer func() {
		if !stop() {
			<-interrupted
		}
		if created {
			// Destruction cancels an outstanding copy and releases render locks.
			d.w.conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			if err := d.w.send(frame, 1, nil); err != nil {
				d.close()
				result = err
			}
		}
		d.w.conn.SetWriteDeadline(time.Time{})
	}()
	if err := d.w.send(d.manager, 0, wlWords(frame, 0, d.output)); err != nil {
		// A short protocol write leaves the connection unusable.
		d.close()
		return err
	}
	created = true
	copied := false
	for {
		event, err := d.next(ctx)
		if err != nil {
			return err
		}
		if event.id != frame {
			continue
		}
		switch event.op {
		case 0: // buffer (manager v2 guarantees wl_shm)
			if copied {
				return fmt.Errorf("duplicate screencopy buffer event")
			}
			if err := d.prepareBuffer(event.body); err != nil {
				return err
			}
			if err := d.w.send(frame, 2, wlWords(d.buffer)); err != nil {
				return err
			}
			copied = true
		case 2: // ready
			if !copied {
				return fmt.Errorf("screencopy ready before copy")
			}
			return nil
		case 3:
			return fmt.Errorf("screencopy frame failed")
		}
	}
}

// changed reports whether the output changed within timeout.
func (d *outputDamage) changed(ctx context.Context, timeout time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := d.capture(bounded)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err == nil {
		return true, nil
	}
	if errors.Is(err, context.DeadlineExceeded) || (bounded.Err() == context.DeadlineExceeded && os.IsTimeout(err)) {
		return false, nil
	}
	return false, err
}

// settle waits for the output to change and then to stay still for quiet,
// within limit. A client that resumes after a stall can first present a
// frame it drew before the stall; the quiet period lets the frames that
// follow the shortcut land before later input.
func (d *outputDamage) settle(ctx context.Context, limit, quiet time.Duration) error {
	deadline := time.Now().Add(limit)
	changed, err := d.changed(ctx, limit)
	for err == nil && changed {
		left := time.Until(deadline)
		if left <= 0 {
			return nil
		}
		changed, err = d.changed(ctx, min(quiet, left))
	}
	return err
}

func (d *outputDamage) close() {
	if d == nil {
		return
	}
	d.closed.Do(func() {
		close(d.done)
		d.w.conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		if d.buffer != 0 {
			d.w.send(d.buffer, 0, nil)
		}
		if d.manager != 0 {
			d.w.send(d.manager, 2, nil)
		}
		d.w.conn.Close()
		if d.fd >= 0 {
			unix.Close(d.fd)
			d.fd = -1
		}
	})
}
