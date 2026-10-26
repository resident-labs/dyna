//go:build linux

package computers

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// This small client uses only registry, sync, virtual pointer, and foreign
// toplevel messages. All connections target the private compositor socket.
type wlGlobal struct{ name, version uint32 }
type wayland struct {
	conn    *net.UnixConn
	next    uint32
	globals map[string]wlGlobal
	apps    map[uint32]App
}

func wlWords(values ...uint32) []byte {
	out := make([]byte, 4*len(values))
	for i, n := range values {
		binary.NativeEndian.PutUint32(out[4*i:], n)
	}
	return out
}
func wlString(s string) []byte {
	n := len(s) + 1
	out := make([]byte, 4+(n+3)&^3)
	binary.NativeEndian.PutUint32(out, uint32(n))
	copy(out[4:], s)
	return out
}
func wlReadString(b []byte) (string, []byte) {
	if len(b) < 4 {
		return "", nil
	}
	n := int(binary.NativeEndian.Uint32(b))
	if n < 1 || n > len(b)-4 {
		return "", nil
	}
	return string(b[4 : 4+n-1]), b[4+((n+3)&^3):]
}
func (w *wayland) id() uint32 { w.next++; return w.next }
func (w *wayland) send(id uint32, opcode uint16, args []byte) error {
	message := append(wlWords(id, uint32(len(args)+8)<<16|uint32(opcode)), args...)
	for len(message) > 0 {
		n, err := w.conn.Write(message)
		if err != nil {
			return err
		}
		message = message[n:]
	}
	return nil
}
func (w *wayland) read() (uint32, uint16, []byte, error) {
	head := make([]byte, 8)
	if _, err := io.ReadFull(w.conn, head); err != nil {
		return 0, 0, nil, err
	}
	id := binary.NativeEndian.Uint32(head)
	word := binary.NativeEndian.Uint32(head[4:])
	size := int(word >> 16)
	if size < 8 || size > 1<<20 {
		return 0, 0, nil, fmt.Errorf("invalid Wayland message size")
	}
	body := make([]byte, size-8)
	_, err := io.ReadFull(w.conn, body)
	return id, uint16(word), body, err
}
func (w *wayland) roundtrip() error {
	callback := w.id()
	if err := w.send(1, 0, wlWords(callback)); err != nil {
		return err
	}
	for {
		id, op, b, err := w.read()
		if err != nil {
			return err
		}
		if id == callback && op == 0 {
			return nil
		}
		if id == 1 && op == 0 {
			return fmt.Errorf("Wayland protocol error: %q", b)
		}
		if id == 2 && op == 0 && len(b) >= 4 {
			name := binary.NativeEndian.Uint32(b)
			iface, rest := wlReadString(b[4:])
			if len(rest) >= 4 {
				w.globals[iface] = wlGlobal{name, binary.NativeEndian.Uint32(rest)}
			}
		}
		if id == 4 && op == 0 && len(b) >= 4 {
			w.apps[binary.NativeEndian.Uint32(b)] = App{}
		}
		if app, ok := w.apps[id]; ok {
			switch op {
			case 0:
				app.Title, _ = wlReadString(b)
				w.apps[id] = app
			case 1:
				app.AppID, _ = wlReadString(b)
				w.apps[id] = app
			case 6:
				delete(w.apps, id)
			}
		}
	}
}
func connectWayland(ctx context.Context, display string) (*wayland, error) {
	if display == "" {
		return nil, fmt.Errorf("no windows yet")
	}
	path := display
	if !filepath.IsAbs(path) {
		path = filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), path)
	}
	raw, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	conn := raw.(*net.UnixConn)
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	w := &wayland{conn: conn, next: 2, globals: map[string]wlGlobal{}, apps: map[uint32]App{}}
	if err = w.send(1, 1, wlWords(2)); err == nil {
		err = w.roundtrip()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return w, nil
}
func (w *wayland) bind(iface string, id, version uint32) error {
	g, ok := w.globals[iface]
	if !ok {
		return fmt.Errorf("compositor does not support %s", iface)
	}
	if version > g.version {
		version = g.version
	}
	args := append(wlWords(g.name), wlString(iface)...)
	args = append(args, wlWords(version, id)...)
	if id > w.next {
		w.next = id
	}
	return w.send(2, 0, args)
}
func desktopApps(ctx context.Context, display string) ([]App, error) {
	w, err := connectWayland(ctx, display)
	if err != nil {
		return nil, err
	}
	defer w.conn.Close()
	if err = w.bind("zwlr_foreign_toplevel_manager_v1", 4, 3); err != nil {
		return nil, err
	}
	if err = w.roundtrip(); err != nil {
		return nil, err
	}
	if err = w.roundtrip(); err != nil {
		return nil, err
	}
	out := []App{}
	for _, app := range w.apps {
		out = append(out, app)
	}
	return out, nil
}

type virtualPointer struct {
	w             *wayland
	object        uint32
	width, height int
	// held lists the buttons pressed and not yet released, so close releases
	// a pointer_down that its batch never matched with a pointer_up.
	held map[string]bool
	// agent reports whether the agent still has control. Nil means it does.
	agent func() bool
}

func (p *virtualPointer) agentHasControl() bool { return p.agent == nil || p.agent() }

// releaseHeld releases every button the batch pressed and did not release.
func (p *virtualPointer) releaseHeld() {
	for button := range p.held {
		_ = p.button(button, false)
	}
}

func newVirtualPointer(ctx context.Context, display string, width, height int) (*virtualPointer, error) {
	w, err := connectWayland(ctx, display)
	if err != nil {
		return nil, err
	}
	manager := w.id()
	if err = w.bind("zwlr_virtual_pointer_manager_v1", manager, 1); err != nil {
		w.conn.Close()
		return nil, err
	}
	object := w.id()
	if err = w.send(manager, 0, wlWords(0, object)); err != nil {
		w.conn.Close()
		return nil, err
	}
	if err = w.roundtrip(); err != nil {
		w.conn.Close()
		return nil, err
	}
	return &virtualPointer{w: w, object: object, width: width, height: height}, nil
}
func (p *virtualPointer) close() {
	p.releaseHeld()
	p.w.conn.Close()
}
func (p *virtualPointer) send(op uint16, args ...uint32) error {
	_ = p.w.conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := p.w.send(p.object, op, wlWords(args...)); err != nil {
		return err
	}
	if op != 4 {
		if err := p.w.send(p.object, 4, nil); err != nil {
			return err
		}
	}
	return p.w.roundtrip()
}
func pointerTime() uint32 { return uint32(time.Now().UnixMilli()) }
func (p *virtualPointer) move(x, y int) error {
	return p.send(1, pointerTime(), uint32(x), uint32(y), uint32(p.width), uint32(p.height))
}
func (p *virtualPointer) button(button string, pressed bool) error {
	code := uint32(272)
	if button == "right" {
		code = 273
	} else if button == "middle" {
		code = 274
	}
	state := uint32(0)
	if pressed {
		state = 1
	}
	if p.held == nil {
		p.held = map[string]bool{}
	}
	if pressed {
		p.held[button] = true
	} else {
		delete(p.held, button)
	}
	return p.send(2, pointerTime(), code, state)
}
func (p *virtualPointer) scroll(x, y int) error {
	for axis, value := range []int{y, x} {
		if value == 0 {
			continue
		}
		if err := p.send(3, pointerTime(), uint32(axis), uint32(int32(value*256/10))); err != nil {
			return err
		}
	}
	return nil
}
