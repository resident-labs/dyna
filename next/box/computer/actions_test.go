package computer

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func number(n int) *int { return &n }

func TestValidateChecksAWholeActionBeforeInput(t *testing.T) {
	for _, tc := range []struct {
		action Action
		req    Request
		want   string
	}{
		{Click, Request{}, "a selector or x and y"},
		{Click, Request{Selector: "#go", X: number(1), Y: number(1)}, "a selector or x and y"},
		{Click, Request{X: number(1)}, "both x and y"},
		{Click, Request{X: number(1280), Y: number(0)}, "outside the 1280x800 desktop"},
		{Click, Request{X: number(1), Y: number(1), Button: "back"}, "left, middle or right"},
		{Drag, Request{Path: []Point{{1, 1}}}, "2 to 200 points"},
		{Drag, Request{Path: []Point{{1, 1}, {2, 900}}}, "outside"},
		{Scroll, Request{}, "scroll_x or scroll_y"},
		{Scroll, Request{ScrollY: 101}, "-100 to 100"},
		{Key, Request{}, "keys is required"},
		{Key, Request{Keys: []string{"NoSuchKey"}}, `unknown key "NoSuchKey"`},
		{Key, Request{Keys: []string{"a"}, HoldMS: number(20000)}, "hold_ms"},
		{Type, Request{Text: "a\x07"}, "control character"},
		{Screenshot, Request{X: number(0), Y: number(0)}, "x, y, width and height"},
		{Screenshot, Request{X: number(1000), Y: number(0), Width: 400, Height: 10}, "passes the edge"},
		{Launch, Request{Command: " "}, "command is required"},
		{Open, Request{URL: "x", Status: strings.Repeat("s", 81)}, "80 characters"},
		{"swipe", Request{}, "unknown computer action"},
	} {
		if err := Validate(tc.action, tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %+v: %v, want %q", tc.action, tc.req, err, tc.want)
		}
	}
	for _, ok := range []struct {
		action Action
		req    Request
	}{
		{Click, Request{Selector: "#go", Keys: []string{"shift"}}},
		{Click, Request{X: number(0), Y: number(799), Button: "right", Double: true}},
		{Screenshot, Request{X: number(880), Y: number(400), Width: 400, Height: 400}},
		{Key, Request{Keys: []string{"ctrl+shift+t", "F5", "Page_Down", "é", "+"}}},
		{Stop, Request{}},
	} {
		if err := Validate(ok.action, ok.req); err != nil {
			t.Errorf("%s %+v: %v", ok.action, ok.req, err)
		}
	}
}

func TestKeysTakeV1AliasesCharactersKeysymNamesAndChords(t *testing.T) {
	for _, tc := range []struct {
		keys []string
		want []uint32
	}{
		{[]string{"CTRL", "l"}, []uint32{0xffe3, 'l'}},
		{[]string{"ctrl+l"}, []uint32{0xffe3, 'l'}},
		{[]string{"enter"}, []uint32{0xff0d}},
		{[]string{"é"}, []uint32{0xe9}},
		{[]string{"日"}, []uint32{0x01000000 + 0x65e5}},
		{[]string{"+"}, []uint32{'+'}},
		{[]string{"Page_Down", "F5", "XF86AudioMute"}, []uint32{0xff56, 0xffc2, 0x1008ff12}},
		// v1's Shift rule: with Shift, a character is the one Shift types, so Xvnc keeps Shift down for it.
		{[]string{"ctrl+shift+t"}, []uint32{0xffe3, 0xffe1, 'T'}},
		{[]string{"shift+tab"}, []uint32{0xffe1, 0xff09}},
		{[]string{"SHIFT", "1", "minus"}, []uint32{0xffe1, '!', '_'}},
	} {
		if got, err := keysymsOf(tc.keys); err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%v: %x, %v; want %x", tc.keys, got, err, tc.want)
		}
	}
	for _, bad := range [][]string{{"NoSuchKey"}, {"ctrl+"}, {"\x07"}} {
		if _, err := keysymsOf(bad); err == nil {
			t.Errorf("%q is a key", bad)
		}
	}
}

func TestSettledSaysWhatChanged(t *testing.T) {
	if got := (Settled{After: time.Second, Still: true}).Describe(); got != "Nothing on the screen changed within 1000 ms." {
		t.Fatal(got)
	}
	r := Rect{10, 10, 5, 5}.union(Rect{0, 20, 2, 2})
	if got := (Settled{Changed: r, After: 40 * time.Millisecond, Still: true}).Describe(); got != "The screen changed after 40 ms in 15x12 at 0,10, then settled." {
		t.Fatal(got)
	}
}

type keyEvent struct {
	keysym uint32
	down   bool
	at     time.Time
}

// fakeVNC serves the display's socket as Xvnc does for the actions' keyboard: it records every key event and answers
// each picture request. desktopSocket points at it for the test.
func fakeVNC(t *testing.T) func() []keyEvent {
	socket := filepath.Join(t.TempDir(), "vnc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	previous := desktopSocket
	desktopSocket = socket
	t.Cleanup(func() { desktopSocket = previous })
	var mu sync.Mutex
	var events []keyEvent
	serve := func(conn net.Conn) {
		defer conn.Close()
		r := bufio.NewReader(conn)
		read := func(n int) ([]byte, error) { b := make([]byte, n); _, err := io.ReadFull(r, b); return b, err }
		_, _ = conn.Write([]byte("RFB 003.008\n\x01\x01"))
		_, _ = read(12 + 1)                                                // version, security None
		_, _ = conn.Write([]byte{0, 0, 0, 0})                              // security result
		_, _ = read(1)                                                     // shared
		_, _ = conn.Write(append([]byte{0, 1, 0, 1}, make([]byte, 20)...)) // 1x1, no name
		_, _ = read(20)                                                    // SetPixelFormat
		head, _ := read(4)                                                 // SetEncodings, then the encodings
		_, _ = read(4 * int(head[3]))
		for {
			kind, err := read(1)
			if err != nil {
				return
			}
			if kind[0] == 3 {
				_, _ = read(9)
				_, _ = conn.Write([]byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0, 0x55})
				continue
			}
			event, err := read(7)
			if err != nil {
				return
			}
			mu.Lock()
			events = append(events, keyEvent{binary.BigEndian.Uint32(event[3:]), event[0] == 1, time.Now()})
			mu.Unlock()
		}
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serve(conn)
		}
	}()
	return func() []keyEvent {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(events)
		events = nil
		return out
	}
}

// presses lists the keysyms that went down, in order.
func presses(events []keyEvent) []uint32 {
	var out []uint32
	for _, e := range events {
		if e.down {
			out = append(out, e.keysym)
		}
	}
	return out
}

// Typed text goes to Xvnc as key events, one keysym a character, Enter for each line break.
func TestTypeTextSendsAKeysymForEveryCharacter(t *testing.T) {
	sent := fakeVNC(t)
	if err := TypeText(context.Background(), desktopSocket, "Hé ü\r\nñ 日本\t→"); err != nil {
		t.Fatal(err)
	}
	want := []uint32{'H', 0xe9, ' ', 0xfc, 0xff0d, 0xf1, ' ', 0x01000000 + 0x65e5, 0x01000000 + 0x672c, 0xff09, 0x01000000 + 0x2192}
	if got := presses(sent()); !slices.Equal(got, want) {
		t.Fatalf("keysyms %x, want %x", got, want)
	}
}

// A chord goes down modifiers first and up in reverse, and stays down for its hold from when Xvnc took it.
func TestAKeyChordIsHeldAndLetGoInReverse(t *testing.T) {
	sent := fakeVNC(t)
	keys, _ := keysymsOf([]string{"shift+tab"})
	if err := press(context.Background(), keys, 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	events := sent()
	want := []keyEvent{{0xffe1, true, time.Time{}}, {0xff09, true, time.Time{}}, {0xff09, false, time.Time{}}, {0xffe1, false, time.Time{}}}
	if len(events) != len(want) {
		t.Fatalf("events %+v", events)
	}
	for i, e := range events {
		if e.keysym != want[i].keysym || e.down != want[i].down {
			t.Fatalf("event %d: %+v, want %+v", i, e, want[i])
		}
	}
	if held := events[2].at.Sub(events[1].at); held < 300*time.Millisecond {
		t.Fatalf("held %v", held)
	}
}

// Keys held around a click go down on Xvnc before xdotool clicks, and up after it.
func TestKeysHeldAroundAPointerChain(t *testing.T) {
	sent := fakeVNC(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "xdotool.log")
	if err := os.WriteFile(filepath.Join(dir, "xdotool"), []byte("#!/bin/sh\ndate +%s%N >> "+log+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if err := holding(context.Background(), []uint32{0xffe1}, []string{"click", "1"}); err != nil {
		t.Fatal(err)
	}
	stamp, _ := os.ReadFile(log)
	nanos, _ := strconv.ParseInt(strings.TrimSpace(string(stamp)), 10, 64)
	clicked := time.Unix(0, nanos)
	events := sent()
	if len(events) != 2 || !events[0].down || events[1].down || !events[0].at.Before(clicked) || !events[1].at.After(clicked) {
		t.Fatalf("shift %+v around the click at %v", events, clicked)
	}
}

// fakeAgent serves the box agent's endpoint on a socket, as Process.Handler answers it, with no desktop behind it.
func fakeAgent(t *testing.T, launched *[]string) Agent {
	socket := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req StartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Launch != "" {
			*launched = append(*launched, req.Launch)
		}
		_ = json.NewEncoder(w).Encode(Started{URL: "http://127.0.0.1:1", Log: "/tmp/app.log"})
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return NewAgent(socket)
}

// Each pointer action is one xdotool chain; its keys go to Xvnc.
func TestInputActionsSendOneXdotoolChain(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "xdotool.log")
	script := "#!/bin/sh\necho \"$DISPLAY $*\" >> " + log + "\n"
	if err := os.WriteFile(filepath.Join(dir, "xdotool"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	var launched []string
	agent := fakeAgent(t, &launched)
	for _, tc := range []struct {
		action Action
		req    Request
		want   string
	}{
		{Click, Request{X: number(10), Y: number(20), Button: "right", Double: true}, ":1 mousemove 10 20 click --repeat 2 --delay 80 3"},
		{Move, Request{X: number(5), Y: number(6)}, ":1 mousemove 5 6"},
		{Drag, Request{Path: []Point{{1, 2}, {3, 4}}, StepMS: number(120)}, ":1 mousemove 1 2 mousedown 1 sleep 0.120 mousemove 3 4 sleep 0.120 mouseup 1"},
		{Scroll, Request{ScrollY: -3, ScrollX: 2}, ":1 mousemove 640 400 click --repeat 3 --delay 20 4 click --repeat 2 --delay 20 7"},
	} {
		_ = os.Remove(log)
		result, err := Do(context.Background(), agent, tc.action, tc.req)
		if err != nil {
			t.Fatalf("%s: %v", tc.action, err)
		}
		sent, _ := os.ReadFile(log)
		if got := strings.TrimSuffix(string(sent), "\n"); got != tc.want {
			t.Errorf("%s sent\n%s\nwant\n%s", tc.action, got, tc.want)
		}
		if result.Text == "" {
			t.Errorf("%s says nothing", tc.action)
		}
	}
}

// A desktop that takes the connection but sends no picture, as with Chromium busy loading a page, ends a screenshot as a
// typed error that says so, not as the connection's raw read timeout.
func TestCaptureTellsADesktopThatSentNoFrame(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "vnc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		read := func(n int) { _, _ = io.ReadFull(r, make([]byte, n)) }
		_, _ = conn.Write([]byte("RFB 003.008\n\x01\x01"))
		read(12 + 1)
		_, _ = conn.Write([]byte{0, 0, 0, 0})
		read(1)
		_, _ = conn.Write(append([]byte{0, 1, 0, 1}, make([]byte, 20)...))
		_, _ = io.Copy(io.Discard, r) // takes the picture request and never answers it
	}()
	previous := captureTimeout
	captureTimeout = 100 * time.Millisecond
	t.Cleanup(func() { captureTimeout = previous })
	_, err = Capture(context.Background(), socket, Rect{})
	var noFrame *NoFrameError
	if !errors.As(err, &noFrame) || noFrame.Within != captureTimeout {
		t.Fatalf("error %v", err)
	}
	if !strings.HasPrefix(err.Error(), "the desktop sent no frame within 0.1 s") || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("text %q", err)
	}
}

func TestConcurrentActionsAreSerialized(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	agent := Agent{client: &http.Client{Transport: redirectTransport{base: server.URL}}}
	done := make(chan error, 2)
	go func() { _, err := Do(context.Background(), agent, Stop, Request{}); done <- err }()
	<-entered
	go func() { _, err := Do(context.Background(), agent, Stop, Request{}); done <- err }()
	select {
	case <-entered:
		t.Error("second action entered while first was running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

type redirectTransport struct{ base string }

func (r redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	base, err := url.Parse(r.base)
	if err != nil {
		return nil, err
	}
	copy.URL.Scheme, copy.URL.Host = base.Scheme, base.Host
	return http.DefaultTransport.RoundTrip(copy)
}
