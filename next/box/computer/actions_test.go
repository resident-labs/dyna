package computer

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

func TestKeyNamesTakeV1AliasesCharactersAndChords(t *testing.T) {
	got, err := keyNames([]string{"CTRL", "enter", "l", "é", "ctrl+shift+t", "+", "Page_Down"})
	want := "Control_L Return U006C U00E9 Control_L Shift_L U0074 U002B Page_Down"
	if err != nil || strings.Join(got, " ") != want {
		t.Fatalf("keys %v, %v", got, err)
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

// Each input action is one xdotool chain, with held keys around it, modifiers before the key they modify.
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
		{Click, Request{X: number(10), Y: number(20), Button: "right", Double: true, Keys: []string{"SHIFT"}}, ":1 keydown Shift_L sleep 0.05 mousemove 10 20 click --repeat 2 --delay 80 3 keyup Shift_L"},
		{Move, Request{X: number(5), Y: number(6)}, ":1 mousemove 5 6"},
		{Drag, Request{Path: []Point{{1, 2}, {3, 4}}, StepMS: number(120)}, ":1 mousemove 1 2 mousedown 1 sleep 0.120 mousemove 3 4 sleep 0.120 mouseup 1"},
		{Scroll, Request{ScrollY: -3, ScrollX: 2}, ":1 mousemove 640 400 click --repeat 3 --delay 20 4 click --repeat 2 --delay 20 7"},
		{Key, Request{Keys: []string{"l", "CTRL"}, HoldMS: number(0)}, ":1 keydown Control_L sleep 0.05 keydown U006C sleep 0.000 keyup U006C keyup Control_L"},
		{Key, Request{Keys: []string{"SHIFT"}}, ":1 keydown Shift_L sleep 0.050 keyup Shift_L"},
		{Type, Request{Text: "hi\r\nthere"}, ":1 type --delay 8 -- hi\nthere"},
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
