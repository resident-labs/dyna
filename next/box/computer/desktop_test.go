package computer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image/png"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pss sums the proportional memory of the desktop's programs, which share Chromium's pages between its processes.
func pss() int {
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

// The real desktop in a box image: DYNA_DESKTOP_TEST=1 runs it inside a box built from dyna-base, through the box
// agent's endpoint as the computer tool reaches it. Every action runs against a test page and a real app, and the page
// reports what it received. It logs start time, settle times and the desktop's memory.
func TestRealDesktop(t *testing.T) {
	if os.Getenv("DYNA_DESKTOP_TEST") == "" {
		t.Skip("DYNA_DESKTOP_TEST=1 runs the real desktop inside a box image")
	}
	ctx := context.Background()
	socket := filepath.Join(t.TempDir(), "agent.sock")
	errs := make(chan error, 1)
	closeDesktop, err := Serve(ctx, socket, errs, func(bool) {})
	if err != nil {
		t.Fatal(err)
	}
	defer closeDesktop()
	agent := NewAgent(socket)
	do := func(action Action, r Request) Result {
		t.Helper()
		began := time.Now()
		result, err := Do(ctx, agent, action, r)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		t.Logf("%s in %v: %s", action, time.Since(began).Round(time.Millisecond), result.Text)
		return result
	}
	page := func(expression string) string {
		t.Helper()
		started, err := agent.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		d, err := Connect(ctx, started.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		var value any
		if err := evaluate(ctx, d, expression, &value); err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(value)
		return string(out)
	}

	began := time.Now()
	html := `<title>Desktop test</title><body style="margin:0;height:4000px">
<input id=q style="position:absolute;left:300px;top:200px;width:300px;height:40px">
<button id=b style="position:absolute;left:700px;top:200px;width:120px;height:40px">Button</button>
<canvas id=c width=400 height=200 style="position:absolute;left:300px;top:300px;background:#eee"></canvas>
<p id=p style="position:absolute;top:600px">hello desktop</p>
<script>window.seen = [];
for (const kind of ["mousedown", "mouseup", "dblclick", "contextmenu"]) addEventListener(kind, e => seen.push(kind + " " + e.button + " " + e.target.id + (e.shiftKey ? " shift" : "")));
</script>`
	open := do(Open, Request{URL: "data:text/html," + url.PathEscape(html), Status: "Checking the desktop actions"})
	t.Logf("the first action, which started the desktop, took %v", time.Since(began).Round(time.Millisecond))
	if !strings.Contains(open.Text, "Desktop test") {
		t.Fatalf("open: %q", open.Text)
	}
	if text := do(Text, Request{Selector: "#p"}); text.Text != "hello desktop" {
		t.Fatalf("text %q", text.Text)
	}

	// A selector click is a real click at the element's center on the desktop.
	clicked := do(Click, Request{Selector: "#q"})
	if got := page("document.activeElement.id"); got != `"q"` || !strings.Contains(clicked.Text, "The screen changed") {
		t.Fatalf("click on the field: focus %s, %q", got, clicked.Text)
	}
	do(Type, Request{Text: "hiH"})
	do(Key, Request{Keys: []string{"ctrl+a"}})
	do(Type, Request{Text: "dyna é"})
	if got := page("document.querySelector('#q').value"); got != `"dyna é"` {
		t.Fatalf("typed value %s", got)
	}
	do(Type, Request{Selector: "#q", Text: "!"})
	if got := page("document.querySelector('#q').value"); got != `"dyna é!"` {
		t.Fatalf("inserted value %s", got)
	}
	do(Click, Request{Selector: "#b", Double: true, Keys: []string{"SHIFT"}})
	do(Click, Request{Selector: "#b", Button: "right"})
	// A right click opens Chromium's menu, which may take the release, so only its press and menu event are certain.
	got := page("seen.filter(s => s.includes(' b')).join(', ')")
	if !strings.HasPrefix(got, `"mousedown 0 b shift, mouseup 0 b shift, mousedown 0 b shift, mouseup 0 b shift, dblclick 0 b shift, mousedown 2 b`) || !strings.Contains(got, "contextmenu 2 b") {
		t.Fatalf("button events %s", got)
	}
	do(Key, Request{Keys: []string{"Escape"}})

	// A drag on the canvas, by desktop points: its press and release land where the page says.
	var at []float64
	if err := json.Unmarshal([]byte(page(`(() => { const r = document.querySelector('#c').getBoundingClientRect(); return [screenX + (outerWidth - innerWidth) / 2 + r.left, screenY + outerHeight - innerHeight + r.top]; })()`)), &at); err != nil {
		t.Fatal(err)
	}
	x, y := int(at[0]), int(at[1])
	do(Drag, Request{Path: []Point{{x + 20, y + 20}, {x + 200, y + 100}, {x + 380, y + 180}}, StepMS: number(30)})
	if got := page("seen.slice(-2).join(', ')"); got != `"mousedown 0 c, mouseup 0 c"` {
		t.Fatalf("drag events %s", got)
	}
	scrolled := do(Scroll, Request{X: number(640), Y: number(500), ScrollY: 5})
	if got := page("scrollY"); got == "0" || !strings.Contains(scrolled.Text, "The screen changed") {
		t.Fatalf("scroll: scrollY %s, %q", got, scrolled.Text)
	}
	// The watching view drops the cursor encodings on its open connection, and Xvnc then draws the agent's pointer into
	// the picture: a move of the pointer alone changes pixels where it went.
	c, err := dialRFB(ctx, VNCSocket, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.request(false, Rect{0, 0, c.width, c.height}); err != nil {
		t.Fatal(err)
	}
	for got := (Rect{}); got.empty(); got, err = c.update(nil) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.conn.Write([]byte{2, 0, 0, 1, 0, 0, 0, 0}); err != nil { // SetEncodings: Raw only
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	do(Move, Request{X: number(900), Y: number(700)})
	if err := c.request(true, Rect{0, 0, c.width, c.height}); err != nil {
		t.Fatal(err)
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	drawn, err := c.update(nil)
	if err != nil || drawn.X > 900 || drawn.Y > 700 || drawn.X+drawn.Width <= 900 || drawn.Y+drawn.Height <= 700 {
		t.Fatalf("the pointer is not in the picture: %s, %v", drawn, err)
	}
	c.Close()
	do(Move, Request{X: number(5), Y: number(5)})
	still := do(Wait, Request{MS: number(100)})
	if still.Text != "Waited 100 ms." {
		t.Fatalf("wait %q", still.Text)
	}

	shot := do(Screenshot, Request{})
	full, err := png.DecodeConfig(bytes.NewReader(shot.PNG))
	if err != nil || full.Width != 1280 || full.Height != 800 {
		t.Fatalf("screenshot %+v: %v", full, err)
	}
	region := do(Screenshot, Request{X: number(100), Y: number(50), Width: 400, Height: 300})
	part, err := png.DecodeConfig(bytes.NewReader(region.PNG))
	if err != nil || part.Width != 400 || part.Height != 300 || !strings.Contains(region.Text, "add 100,50") {
		t.Fatalf("region %+v %q: %v", part, region.Text, err)
	}
	if whole := do(Screenshot, Request{Page: true}); len(whole.PNG) == 0 {
		t.Fatal("no page screenshot")
	}

	// launch waits for the app's window; the box has urxvt, which jwm brings.
	launched := do(Launch, Request{Command: "urxvt -title dyna-term", Status: "Opening a terminal"})
	if !strings.Contains(launched.Text, `"dyna-term"`) {
		t.Fatalf("launch %q", launched.Text)
	}
	do(Type, Request{Text: "echo typed-in-term > /tmp/dyna-term.out\n"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if out, _ := os.ReadFile("/tmp/dyna-term.out"); string(out) == "typed-in-term\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("typing into the terminal did not run its command")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := Do(ctx, agent, Launch, Request{Command: "true", TimeoutMS: 1000}); err == nil || !strings.Contains(err.Error(), "no new window") {
		t.Fatalf("a command with no window: %v", err)
	}
	time.Sleep(2 * time.Second)
	t.Logf("desktop memory with a page and a terminal open: %d MiB PSS", pss()/1024)

	do(Stop, Request{})
	if n := pss(); n != 0 {
		t.Fatalf("%d KiB of the desktop left after stop", n)
	}
	do(Wait, Request{MS: number(0)})
	if got := page("1 + 1"); got != "2" {
		t.Fatalf("after a restart: %s", got)
	}
}

// fps reads a stream's VNC updates for a while and counts them. With continuous, it asks Xvnc to push updates, as
// noVNC does, answering its fences; without, it asks for each, as the stage 1 view did. It asks for Tight with JPEG
// quality 6, as noVNC's moving picture does, and skips the rectangles without decoding them.
func fps(t *testing.T, r *bufio.Reader, w io.Writer, continuous bool, length time.Duration) (frames int, bytes int64) {
	t.Helper()
	read := func(b []byte) {
		if _, err := io.ReadFull(r, b); err != nil {
			t.Fatal(err)
		}
	}
	write := func(b []byte) {
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	version := make([]byte, 12)
	read(version)
	write(version)
	count := make([]byte, 1)
	read(count)
	read(make([]byte, count[0]))
	write([]byte{1})
	read(make([]byte, 4))
	write([]byte{1})
	init := make([]byte, 24)
	read(init)
	width, height := int(binary.BigEndian.Uint16(init)), int(binary.BigEndian.Uint16(init[2:]))
	read(make([]byte, binary.BigEndian.Uint32(init[20:])))
	write([]byte{0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0})
	encodings := []int32{7, -26, -254} // Tight, quality 6, compression 2
	if continuous {
		encodings = append(encodings, -313, -312) // ContinuousUpdates, Fence
	}
	set := []byte{2, 0, 0, byte(len(encodings))}
	for _, e := range encodings {
		set = binary.BigEndian.AppendUint32(set, uint32(e))
	}
	write(set)
	whole := func(incremental byte) []byte {
		m := []byte{3, incremental, 0, 0, 0, 0}
		return binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(m, uint16(width)), uint16(height))
	}
	write(whole(0))
	compact := func() int {
		n := 0
		for shift := 0; shift < 21; shift += 7 {
			b, _ := r.ReadByte()
			n |= int(b&0x7f) << shift
			if b&0x80 == 0 {
				break
			}
		}
		return n
	}
	skip := func(n int) {
		if _, err := io.CopyN(io.Discard, r, int64(n)); err != nil {
			t.Fatal(err)
		}
		bytes += int64(n)
	}
	began, enabled := time.Now(), false
	for time.Since(began) < length {
		kind, err := r.ReadByte()
		if err != nil {
			t.Fatal(err)
		}
		switch kind {
		case 0:
			head := make([]byte, 3)
			read(head)
			for range binary.BigEndian.Uint16(head[1:]) {
				rect := make([]byte, 12)
				read(rect)
				w, h := int(binary.BigEndian.Uint16(rect[4:])), int(binary.BigEndian.Uint16(rect[6:]))
				if encoding := int32(binary.BigEndian.Uint32(rect[8:])); encoding != 7 {
					t.Fatalf("encoding %d", encoding)
				}
				control, _ := r.ReadByte()
				switch control >> 4 {
				case 8: // fill
					skip(3)
				case 9: // JPEG
					skip(compact())
				default:
					size, palette := w*h*3, 0
					if control&0x40 != 0 {
						filter, _ := r.ReadByte()
						if filter == 1 {
							n, _ := r.ReadByte()
							palette = int(n) + 1
							skip(palette * 3)
							size = w * h
							if palette == 2 {
								size = (w + 7) / 8 * h
							}
						}
					}
					if size < 12 {
						skip(size)
					} else {
						skip(compact())
					}
				}
			}
			frames++
			if !continuous {
				write(whole(1))
			}
		case 150: // EndOfContinuousUpdates: the server takes them, so turn them on
			if !enabled {
				enabled = true
				write(append([]byte{150, 1, 0, 0, 0, 0}, whole(0)[6:]...))
			}
		case 248: // a fence: answer it with the flags the client supports
			head := make([]byte, 8)
			read(head)
			payload := make([]byte, head[7])
			read(payload)
			flags := binary.BigEndian.Uint32(head[3:]) &^ (1 << 31) & 7
			write(append(append([]byte{248, 0, 0, 0}, binary.BigEndian.AppendUint32(nil, flags)...), append([]byte{byte(len(payload))}, payload...)...))
		case 2:
		default:
			t.Fatalf("server message %d", kind)
		}
	}
	return frames, bytes
}

// DYNA_DESKTOP_STREAM is a command whose stdin and stdout are a box's VNC stream, such as the ssh and podman exec the
// engine runs, and the desktop should be moving. The test logs a picture request's round trip and the frame rate with
// the client asking for each frame, then with continuous updates.
func TestDesktopStreamFrameRate(t *testing.T) {
	command := os.Getenv("DYNA_DESKTOP_STREAM")
	if command == "" {
		t.Skip("DYNA_DESKTOP_STREAM names a command that carries a running desktop's VNC stream")
	}
	for _, continuous := range []bool{false, true} {
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
		length := 10 * time.Second
		frames, bytes := fps(t, bufio.NewReaderSize(out, 1<<20), in, continuous, length)
		_ = in.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		mode := "asking for each frame"
		if continuous {
			mode = "continuous updates"
		}
		t.Logf("%s: %.1f fps, %.0f KiB/s", mode, float64(frames)/length.Seconds(), float64(bytes)/1024/length.Seconds())
	}
}
