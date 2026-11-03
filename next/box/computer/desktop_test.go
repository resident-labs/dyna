package computer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// rfb is the least of an RFB client: security None, raw pixels, and the owner's pointer and keys.
type rfb struct {
	r             *bufio.Reader
	w             io.Writer
	width, height int
}

func dialRFB(t *testing.T, r io.Reader, w io.Writer) *rfb {
	t.Helper()
	c := &rfb{r: bufio.NewReaderSize(r, 1<<20), w: w}
	version := make([]byte, 12)
	c.read(t, version)
	if string(version) != "RFB 003.008\n" {
		t.Fatalf("version %q", version)
	}
	c.write(t, version)
	count := make([]byte, 1)
	c.read(t, count)
	types := make([]byte, count[0])
	c.read(t, types)
	if !bytes.Contains(types, []byte{1}) {
		t.Fatalf("security types %v offer no None", types)
	}
	c.write(t, []byte{1})
	result := make([]byte, 4)
	c.read(t, result)
	if binary.BigEndian.Uint32(result) != 0 {
		t.Fatal("security refused")
	}
	c.write(t, []byte{1}) // shared, as the owner's view is
	init := make([]byte, 24)
	c.read(t, init)
	c.width, c.height = int(binary.BigEndian.Uint16(init[0:])), int(binary.BigEndian.Uint16(init[2:]))
	name := make([]byte, binary.BigEndian.Uint32(init[20:]))
	c.read(t, name)
	if init[4] != 32 {
		t.Fatalf("bits per pixel %d", init[4])
	}
	c.write(t, []byte{2, 0, 0, 1, 0, 0, 0, 0}) // SetEncodings: Raw only
	return c
}

func (c *rfb) read(t *testing.T, b []byte) {
	t.Helper()
	if _, err := io.ReadFull(c.r, b); err != nil {
		t.Fatal(err)
	}
}

func (c *rfb) write(t *testing.T, b []byte) {
	t.Helper()
	if _, err := c.w.Write(b); err != nil {
		t.Fatal(err)
	}
}

// update asks for a region and returns its pixels' bytes once the server sends it.
func (c *rfb) update(t *testing.T, incremental bool, x, y, w, h int) int {
	t.Helper()
	request := []byte{3, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if incremental {
		request[1] = 1
	}
	for i, v := range []int{x, y, w, h} {
		binary.BigEndian.PutUint16(request[2+2*i:], uint16(v))
	}
	c.write(t, request)
	head := make([]byte, 4)
	c.read(t, head)
	if head[0] != 0 {
		t.Fatalf("server message %d", head[0])
	}
	total := 0
	for range binary.BigEndian.Uint16(head[2:]) {
		rect := make([]byte, 12)
		c.read(t, rect)
		w, h := int(binary.BigEndian.Uint16(rect[4:])), int(binary.BigEndian.Uint16(rect[6:]))
		if encoding := int32(binary.BigEndian.Uint32(rect[8:])); encoding != 0 {
			t.Fatalf("encoding %d", encoding)
		}
		pixels := make([]byte, w*h*4)
		c.read(t, pixels)
		total += len(pixels)
	}
	return total
}

func (c *rfb) pointer(t *testing.T, buttons byte, x, y int) {
	t.Helper()
	event := []byte{5, buttons, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(event[2:], uint16(x))
	binary.BigEndian.PutUint16(event[4:], uint16(y))
	c.write(t, event)
}

func (c *rfb) key(t *testing.T, keysym uint32) {
	t.Helper()
	for _, down := range []byte{1, 0} {
		event := []byte{4, down, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(event[4:], keysym)
		c.write(t, event)
	}
}

// pss sums the proportional memory of the desktop's programs, which share Chromium's pages between its processes.
func pss(t *testing.T) int {
	t.Helper()
	total := 0
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		if name := strings.TrimSpace(string(comm)); name != "Xvnc" && name != "jwm" && !strings.HasPrefix(name, "chrom") {
			continue
		}
		rollup, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "smaps_rollup"))
		for _, line := range strings.Split(string(rollup), "\n") {
			if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "Pss:" {
				kb, _ := strconv.Atoi(fields[1])
				total += kb
			}
		}
	}
	return total
}

// The real desktop in a box image: DYNA_DESKTOP_TEST=1 runs it inside a box built from dyna-base. It starts Xvnc, jwm and
// a windowed Chromium, reads the screen over VNC, moves the pointer and types into the page over VNC, and drives the page
// with the agent's DevTools actions. It logs the start time and the desktop's memory.
func TestRealDesktop(t *testing.T) {
	if os.Getenv("DYNA_DESKTOP_TEST") == "" {
		t.Skip("DYNA_DESKTOP_TEST=1 runs the real desktop inside a box image")
	}
	ctx := context.Background()
	p := NewProcess()
	defer p.Close()
	began := time.Now()
	if _, err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	displayed := time.Since(began)
	d, err := Connect(ctx, URL)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	t.Logf("desktop shows in %v, Chromium answers DevTools in %v", displayed.Round(time.Millisecond), time.Since(began).Round(time.Millisecond))

	conn, err := net.Dial("unix", VNCSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := dialRFB(t, conn, conn)
	if c.width != 1280 || c.height != 800 {
		t.Fatalf("desktop %dx%d", c.width, c.height)
	}
	if n := c.update(t, false, 0, 0, c.width, c.height); n != 1280*800*4 {
		t.Fatalf("full frame %d bytes", n)
	}

	page := "data:text/html,<title>t</title><input id=q autofocus><p id=p>hello desktop</p>"
	var out bytes.Buffer
	if err := Execute(ctx, d, Open, []string{page}, "", false, &out); err != nil {
		t.Fatal(err)
	}
	if err := Execute(ctx, d, Text, []string{"#p"}, "", false, &out); err != nil || strings.TrimSpace(out.String()) != "hello desktop" {
		t.Fatalf("text %q: %v", out.String(), err)
	}
	shot := filepath.Join(t.TempDir(), "shot.png")
	if err := Execute(ctx, d, Screenshot, []string{shot}, "", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(shot); err != nil || info.Size() < 1000 {
		t.Fatalf("screenshot: %v", err)
	}

	// The owner's pointer and keys, over VNC: a move the display sees, then a click on the field and typed text.
	c.pointer(t, 0, 321, 123)
	time.Sleep(200 * time.Millisecond)
	locate := exec.Command("xdotool", "getmouselocation", "--shell")
	locate.Env = append(os.Environ(), "DISPLAY="+display)
	where, err := locate.Output()
	if err != nil || !strings.Contains(string(where), "X=321\nY=123\n") {
		t.Fatalf("pointer %q: %v", where, err)
	}
	box, err := d.Call(ctx, "Runtime.evaluate", json.RawMessage(`{"expression":"(() => { const r = document.querySelector('#q').getBoundingClientRect(); return [r.x + r.width / 2, r.y + r.height / 2, window.screenX + (window.outerWidth - window.innerWidth) / 2, window.screenY + window.outerHeight - window.innerHeight]; })()","returnByValue":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var at struct {
		Result struct {
			Value []float64 `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(box, &at); err != nil || len(at.Result.Value) != 4 {
		t.Fatalf("field position %s: %v", box, err)
	}
	x, y := int(at.Result.Value[0]+at.Result.Value[2]), int(at.Result.Value[1]+at.Result.Value[3])
	c.pointer(t, 1, x, y)
	c.pointer(t, 0, x, y)
	time.Sleep(200 * time.Millisecond)
	for _, keysym := range []uint32{'h', 'i', 'H'} {
		c.key(t, keysym)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		value, err := d.Call(ctx, "Runtime.evaluate", json.RawMessage(`{"expression":"document.querySelector('#q').value","returnByValue":true}`))
		if err == nil && strings.Contains(string(value), `"hiH"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("typed value %s: %v (clicked %d,%d)", value, err, x, y)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n := c.update(t, true, 0, 0, c.width, c.height); n == 0 || n == 1280*800*4 {
		t.Fatalf("an incremental update after typing sent %d bytes, want only the changed regions", n)
	}
	time.Sleep(3 * time.Second)
	t.Logf("desktop memory, idle with about:blank's window and a small page: %d MiB PSS", pss(t)/1024)
}

// DYNA_DESKTOP_STREAM is a command whose stdin and stdout are a box's VNC stream, such as the ssh and podman exec the
// engine runs. The test logs how long a picture request takes to come back through it.
func TestDesktopStreamRoundTrip(t *testing.T) {
	command := os.Getenv("DYNA_DESKTOP_STREAM")
	if command == "" {
		t.Skip("DYNA_DESKTOP_STREAM names a command that carries a running desktop's VNC stream")
	}
	began := time.Now()
	cmd := exec.Command("sh", "-c", command)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = cmd.Wait() }()
	c := dialRFB(t, out, in)
	t.Logf("handshake through the stream in %v", time.Since(began).Round(time.Millisecond))
	start := time.Now()
	c.update(t, false, 0, 0, c.width, c.height)
	t.Logf("a full 1280x800 raw frame (4 MB) in %v", time.Since(start).Round(time.Millisecond))
	var trips []time.Duration
	for range 20 {
		start := time.Now()
		c.update(t, false, 0, 0, 16, 16)
		trips = append(trips, time.Since(start))
	}
	var sum time.Duration
	for _, trip := range trips {
		sum += trip
	}
	t.Logf("a picture request's round trip: %s", fmt.Sprintf("mean %v, first %v, last %v", (sum/time.Duration(len(trips))).Round(100*time.Microsecond), trips[0].Round(100*time.Microsecond), trips[len(trips)-1].Round(100*time.Microsecond)))
}
