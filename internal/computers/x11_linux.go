//go:build linux

package computers

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// X11 window metadata complements foreign-toplevel metadata on compositors
// which expose only native Wayland windows through that protocol. Callers must
// first check that Xwayland is already alive: querying must never start it.
type x11Client struct {
	net.Conn
	root uint32
}

func connectX11(ctx context.Context, display string) (*x11Client, error) {
	number := strings.TrimPrefix(display, ":")
	number, _, _ = strings.Cut(number, ".")
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", "/tmp/.X11-unix/X"+number)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	setup := []byte{'l', 0, 11, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if _, err = conn.Write(setup); err != nil {
		conn.Close()
		return nil, err
	}
	header := make([]byte, 8)
	if _, err = io.ReadFull(conn, header); err != nil {
		conn.Close()
		return nil, err
	}
	n := int(binary.LittleEndian.Uint16(header[6:])) * 4
	data := make([]byte, n)
	if _, err = io.ReadFull(conn, data); err != nil {
		conn.Close()
		return nil, err
	}
	if header[0] != 1 || len(data) < 32 {
		conn.Close()
		return nil, fmt.Errorf("X11 setup failed")
	}
	vendor := int(binary.LittleEndian.Uint16(data[16:]))
	offset := 32 + ((vendor + 3) &^ 3) + int(data[21])*8
	if offset+4 > len(data) {
		conn.Close()
		return nil, fmt.Errorf("invalid X11 setup")
	}
	return &x11Client{Conn: conn, root: binary.LittleEndian.Uint32(data[offset:])}, nil
}
func (x *x11Client) request(op, detail byte, body []byte) ([]byte, error) {
	message := make([]byte, 4+((len(body)+3)&^3))
	message[0] = op
	message[1] = detail
	binary.LittleEndian.PutUint16(message[2:], uint16(len(message)/4))
	copy(message[4:], body)
	if _, err := x.Write(message); err != nil {
		return nil, err
	}
	for {
		reply := make([]byte, 32)
		if _, err := io.ReadFull(x, reply); err != nil {
			return nil, err
		}
		if reply[0] == 0 {
			return nil, fmt.Errorf("X11 error %d", reply[1])
		}
		if reply[0] != 1 {
			continue
		}
		n := binary.LittleEndian.Uint32(reply[4:]) * 4
		if n > 1<<20 {
			return nil, fmt.Errorf("X11 reply too large")
		}
		extra := make([]byte, n)
		if _, err := io.ReadFull(x, extra); err != nil {
			return nil, err
		}
		return append(reply, extra...), nil
	}
}
func (x *x11Client) atom(name string) (uint32, error) {
	body := make([]byte, 4+len(name))
	binary.LittleEndian.PutUint16(body, uint16(len(name)))
	copy(body[4:], name)
	reply, err := x.request(16, 1, body)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(reply[8:]), nil
}
func (x *x11Client) property(window uint32, name string) ([]byte, error) {
	atom, err := x.atom(name)
	if err != nil || atom == 0 {
		return nil, err
	}
	body := make([]byte, 20)
	binary.LittleEndian.PutUint32(body, window)
	binary.LittleEndian.PutUint32(body[4:], atom)
	binary.LittleEndian.PutUint32(body[16:], 4096)
	reply, err := x.request(20, 0, body)
	if err != nil {
		return nil, err
	}
	count := int(binary.LittleEndian.Uint32(reply[16:]))
	size := int(reply[1]) / 8 * count
	if size > len(reply)-32 {
		return nil, fmt.Errorf("invalid X11 property")
	}
	return reply[32 : 32+size], nil
}
func x11Apps(ctx context.Context, display string) ([]App, error) {
	x, err := connectX11(ctx, display)
	if err != nil {
		return nil, err
	}
	defer x.Close()
	windows, err := x.property(x.root, "_NET_CLIENT_LIST")
	if err != nil {
		return nil, err
	}
	out := []App{}
	for len(windows) >= 4 {
		window := binary.LittleEndian.Uint32(windows)
		windows = windows[4:]
		class, _ := x.property(window, "WM_CLASS")
		classes := strings.Split(strings.TrimRight(string(class), "\x00"), "\x00")
		app := App{}
		if len(classes) > 0 {
			app.AppID = classes[len(classes)-1]
		}
		name, _ := x.property(window, "_NET_WM_NAME")
		if len(name) == 0 {
			name, _ = x.property(window, "WM_NAME")
		}
		app.Title = strings.TrimRight(string(name), "\x00")
		if app.AppID != "" || app.Title != "" {
			out = append(out, app)
		}
	}
	return out, nil
}
