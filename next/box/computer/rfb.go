package computer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"slices"
	"strings"
	"time"
)

// Rect is a region of the desktop in its pixels.
type Rect struct{ X, Y, Width, Height int }

func (r Rect) empty() bool { return r.Width <= 0 || r.Height <= 0 }

// union is the smallest rect holding both.
func (r Rect) union(o Rect) Rect {
	if r.empty() {
		return o
	}
	if o.empty() {
		return r
	}
	x, y := min(r.X, o.X), min(r.Y, o.Y)
	return Rect{x, y, max(r.X+r.Width, o.X+o.Width) - x, max(r.Y+r.Height, o.Y+o.Height) - y}
}

func (r Rect) String() string { return fmt.Sprintf("%dx%d at %d,%d", r.Width, r.Height, r.X, r.Y) }

// RFB encodings and the messages the computer's own clients read.
const (
	encodingRaw      = 0
	encodingCopyRect = 1
	encodingCursor   = -239
)

// rfbClient is the computer's own VNC client on the display's socket, shared with the owner's view. It reads the screen
// for screenshots and the changes an action makes. It asks for the cursor shape apart, so the pointer is not in its
// pixels and a pointer move is no change.
type rfbClient struct {
	conn          net.Conn
	r             *bufio.Reader
	width, height int
	bytesPerPixel int
}

// dialRFB connects to the display with security None. bytesPerPixel is 4 for true pixels, or 1 for the coarse pixels
// that only show where the screen changed.
func dialRFB(ctx context.Context, socket string, bytesPerPixel int) (*rfbClient, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("the desktop is not running: %w", err)
	}
	c := &rfbClient{conn: conn, r: bufio.NewReaderSize(conn, 1<<20), bytesPerPixel: bytesPerPixel}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := c.handshake(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("desktop handshake: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	return c, nil
}

func (c *rfbClient) handshake() error {
	version := make([]byte, 12)
	if _, err := io.ReadFull(c.r, version); err != nil {
		return err
	}
	if _, err := c.conn.Write([]byte("RFB 003.008\n")); err != nil {
		return err
	}
	count, err := c.r.ReadByte()
	if err != nil {
		return err
	}
	types := make([]byte, count)
	if _, err := io.ReadFull(c.r, types); err != nil {
		return err
	}
	if !bytes.Contains(types, []byte{1}) {
		return errors.New("the desktop asks for a password")
	}
	result := make([]byte, 4)
	if _, err := c.conn.Write([]byte{1}); err != nil {
		return err
	}
	if _, err := io.ReadFull(c.r, result); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(result) != 0 {
		return errors.New("the desktop refused the connection")
	}
	if _, err := c.conn.Write([]byte{1}); err != nil { // shared, so the owner's view stays
		return err
	}
	init := make([]byte, 24)
	if _, err := io.ReadFull(c.r, init); err != nil {
		return err
	}
	c.width, c.height = int(binary.BigEndian.Uint16(init[0:])), int(binary.BigEndian.Uint16(init[2:]))
	if _, err := io.CopyN(io.Discard, c.r, int64(binary.BigEndian.Uint32(init[20:]))); err != nil {
		return err
	}
	// SetPixelFormat: 32 bpp little-endian xRGB, or 8 bpp bgr233.
	format := []byte{0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0}
	if c.bytesPerPixel == 1 {
		format = []byte{0, 0, 0, 0, 8, 8, 0, 1, 0, 7, 0, 7, 0, 3, 0, 3, 6, 0, 0, 0}
	}
	encodings := []int32{encodingCopyRect, encodingRaw, encodingCursor}
	set := []byte{2, 0, 0, byte(len(encodings))}
	for _, e := range encodings {
		set = binary.BigEndian.AppendUint32(set, uint32(e))
	}
	_, err = c.conn.Write(append(format, set...))
	return err
}

func (c *rfbClient) Close() { _ = c.conn.Close() }

// request asks for an update of r, or of what changed in it since the last one.
// textKeysyms turns text into the keysyms that type it: Enter and Tab for line breaks and tabs, Latin-1 characters as
// themselves, and any other character as its Unicode keysym.
func textKeysyms(text string) []uint32 {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var out []uint32
	for _, c := range text {
		switch {
		case c == '\n':
			out = append(out, 0xff0d)
		case c == '\t':
			out = append(out, 0xff09)
		default:
			out = append(out, runeKeysym(c))
		}
	}
	return out
}

// Keyboard sends the desktop's keyboard input as VNC key events, for every computer action that types or holds a key.
// xdotool types a keysym the keyboard map lacks by mapping it onto a spare key, sending it and restoring the map at once,
// and an app that reads the key after the restore drops it, as in about 2 typings of "é" in 10. Xvnc maps a keysym it
// lacks onto a spare key itself and keeps it there, and adds Shift where a character needs it, so every key arrives.
type Keyboard struct {
	c    *rfbClient
	held []uint32
}

func DialKeyboard(ctx context.Context, socket string) (*Keyboard, error) {
	c, err := dialRFB(ctx, socket, 1)
	if err != nil {
		return nil, err
	}
	return &Keyboard{c: c}, nil
}

// Close lets go of every key still down, and ends the connection.
func (k *Keyboard) Close() {
	_ = k.Up(k.held...)
	k.c.Close()
}

func (k *Keyboard) send(down bool, keysyms []uint32) error {
	flag := byte(0)
	if down {
		flag = 1
	}
	var events []byte
	for _, keysym := range keysyms {
		events = binary.BigEndian.AppendUint32(append(events, 4, flag, 0, 0), keysym)
	}
	_, err := k.c.conn.Write(events)
	return err
}

// Down presses keys in order, and Up lets them go in order.
func (k *Keyboard) Down(keysyms ...uint32) error {
	k.held = append(k.held, keysyms...)
	return k.send(true, keysyms)
}

func (k *Keyboard) Up(keysyms ...uint32) error {
	k.held = slices.DeleteFunc(k.held, func(held uint32) bool { return slices.Contains(keysyms, held) })
	return k.send(false, keysyms)
}

// Sync returns once Xvnc has taken the events before it, as it answers a request sent after them, so input from
// another source, such as xdotool's pointer, comes after them, and the settle that follows sees what they did.
func (k *Keyboard) Sync(ctx context.Context) error {
	if err := k.c.request(false, Rect{0, 0, 1, 1}); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = k.c.conn.SetReadDeadline(deadline)
	}
	_, err := k.c.update(nil)
	return err
}

// TypeText types text on the desktop, a press and a release for each character.
func TypeText(ctx context.Context, socket, text string) error {
	k, err := DialKeyboard(ctx, socket)
	if err != nil {
		return err
	}
	defer k.Close()
	for _, keysym := range textKeysyms(text) {
		if err := k.Down(keysym); err != nil {
			return err
		}
		if err := k.Up(keysym); err != nil {
			return err
		}
	}
	return k.Sync(ctx)
}

func (c *rfbClient) request(incremental bool, r Rect) error {
	message := []byte{3, 0}
	if incremental {
		message[1] = 1
	}
	for _, v := range []int{r.X, r.Y, r.Width, r.Height} {
		message = binary.BigEndian.AppendUint16(message, uint16(v))
	}
	_, err := c.conn.Write(message)
	return err
}

// update reads the next framebuffer update and returns the region it changed. With pixels, raw rectangles are drawn
// into it at their place; CopyRect moves are not, since a client that wants pixels asks for whole regions.
func (c *rfbClient) update(pixels *image.RGBA) (Rect, error) {
	for {
		kind, err := c.r.ReadByte()
		if err != nil {
			return Rect{}, err
		}
		switch kind {
		case 0:
		case 2: // bell
			continue
		case 3: // cut text
			head := make([]byte, 7)
			if _, err := io.ReadFull(c.r, head); err != nil {
				return Rect{}, err
			}
			if _, err := io.CopyN(io.Discard, c.r, int64(binary.BigEndian.Uint32(head[3:]))); err != nil {
				return Rect{}, err
			}
			continue
		default:
			return Rect{}, fmt.Errorf("unexpected desktop message %d", kind)
		}
		head := make([]byte, 3)
		if _, err := io.ReadFull(c.r, head); err != nil {
			return Rect{}, err
		}
		var changed Rect
		for range binary.BigEndian.Uint16(head[1:]) {
			rect := make([]byte, 12)
			if _, err := io.ReadFull(c.r, rect); err != nil {
				return Rect{}, err
			}
			r := Rect{int(binary.BigEndian.Uint16(rect[0:])), int(binary.BigEndian.Uint16(rect[2:])), int(binary.BigEndian.Uint16(rect[4:])), int(binary.BigEndian.Uint16(rect[6:]))}
			switch int32(binary.BigEndian.Uint32(rect[8:])) {
			case encodingRaw:
				data := make([]byte, r.Width*r.Height*c.bytesPerPixel)
				if _, err := io.ReadFull(c.r, data); err != nil {
					return Rect{}, err
				}
				if pixels != nil && c.bytesPerPixel == 4 {
					draw(pixels, r, data)
				}
				changed = changed.union(r)
			case encodingCopyRect:
				if _, err := io.CopyN(io.Discard, c.r, 4); err != nil {
					return Rect{}, err
				}
				changed = changed.union(r)
			case encodingCursor:
				if _, err := io.CopyN(io.Discard, c.r, int64(r.Width*r.Height*c.bytesPerPixel+(r.Width+7)/8*r.Height)); err != nil {
					return Rect{}, err
				}
			default:
				return Rect{}, fmt.Errorf("unexpected desktop encoding %d", int32(binary.BigEndian.Uint32(rect[8:])))
			}
		}
		return changed, nil
	}
}

// draw copies little-endian xRGB pixels of r into img, whose origin is the desktop's.
func draw(img *image.RGBA, r Rect, data []byte) {
	for y := range r.Height {
		for x := range r.Width {
			p := image.Pt(r.X+x, r.Y+y)
			if !p.In(img.Rect) {
				continue
			}
			i, o := (y*r.Width+x)*4, img.PixOffset(p.X, p.Y)
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = data[i+2], data[i+1], data[i], 255
		}
	}
}

// Capture returns a PNG of region of the desktop, or of all of it when region is empty. The pointer is not in it.
func Capture(ctx context.Context, socket string, region Rect) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := dialRFB(ctx, socket, 4)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if region.empty() {
		region = Rect{0, 0, c.width, c.height}
	}
	if region.X+region.Width > c.width || region.Y+region.Height > c.height {
		return nil, fmt.Errorf("the region is outside the %dx%d desktop", c.width, c.height)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
	}
	img := image.NewRGBA(image.Rect(region.X, region.Y, region.X+region.Width, region.Y+region.Height))
	if err := c.request(false, region); err != nil {
		return nil, err
	}
	// A cursor shape can come alone before the pixels.
	for got := (Rect{}); got.union(region) != got; {
		changed, err := c.update(img)
		if err != nil {
			return nil, err
		}
		got = got.union(changed)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Changes watches the screen from before an action until it settles after it, as v1 waited for output damage after a
// click or a shortcut, so the app can redraw before the next input. It uses coarse pixels: only where matters.
type Changes struct {
	c       *rfbClient
	updates chan Rect
	ended   chan error
	done    chan struct{}
	pending bool
}

// WatchChanges connects and takes the screen as it is, before the action.
func WatchChanges(ctx context.Context, socket string) (*Changes, error) {
	c, err := dialRFB(ctx, socket, 1)
	if err != nil {
		return nil, err
	}
	w := &Changes{c: c, updates: make(chan Rect), ended: make(chan error, 1), done: make(chan struct{})}
	if err := c.request(false, Rect{0, 0, c.width, c.height}); err != nil {
		c.Close()
		return nil, err
	}
	for baseline := (Rect{}); baseline.empty(); {
		if baseline, err = c.update(nil); err != nil {
			c.Close()
			return nil, err
		}
	}
	go func() {
		for {
			r, err := c.update(nil)
			if err != nil {
				w.ended <- err
				return
			}
			select {
			case w.updates <- r:
			case <-w.done:
				return
			}
		}
	}()
	return w, nil
}

func (w *Changes) Close() { close(w.done); w.c.Close() }

// next waits up to timeout for a change. A request the server has not answered yet stays asked.
func (w *Changes) next(ctx context.Context, timeout time.Duration) (Rect, bool, error) {
	if !w.pending {
		if err := w.c.request(true, Rect{0, 0, w.c.width, w.c.height}); err != nil {
			return Rect{}, false, err
		}
		w.pending = true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case r := <-w.updates:
			w.pending = false
			if r.empty() { // a cursor shape alone
				if err := w.c.request(true, Rect{0, 0, w.c.width, w.c.height}); err != nil {
					return Rect{}, false, err
				}
				w.pending = true
				continue
			}
			return r, true, nil
		case err := <-w.ended:
			return Rect{}, false, err
		case <-timer.C:
			return Rect{}, false, nil
		case <-ctx.Done():
			return Rect{}, false, ctx.Err()
		}
	}
}

// Settled is what an action did to the screen.
type Settled struct {
	Changed Rect          // the region that changed, empty for none
	After   time.Duration // when the first change came, or how long nothing did
	Still   bool          // the screen stayed still for the quiet time before limit
}

// Settle waits up to first for the screen to change, then until it stays still for quiet, within limit in all.
func (w *Changes) Settle(ctx context.Context, first, quiet, limit time.Duration) (Settled, error) {
	began := time.Now()
	var s Settled
	r, changed, err := w.next(ctx, first)
	if err != nil || !changed {
		s.Still, s.After = err == nil, first
		return s, err
	}
	s.After, s.Changed = time.Since(began), r
	for {
		left := limit - time.Since(began)
		if left <= 0 {
			return s, nil
		}
		r, changed, err = w.next(ctx, min(quiet, left))
		if err != nil {
			return s, err
		}
		if !changed {
			s.Still = time.Since(began) < limit
			return s, nil
		}
		s.Changed = s.Changed.union(r)
	}
}

// Describe says what changed, for the agent.
func (s Settled) Describe() string {
	if s.Changed.empty() {
		return fmt.Sprintf("Nothing on the screen changed within %d ms.", s.After.Milliseconds())
	}
	text := fmt.Sprintf("The screen changed after %d ms in %s", s.After.Milliseconds(), s.Changed)
	if !s.Still {
		return text + ", and it was still changing."
	}
	return text + ", then settled."
}
