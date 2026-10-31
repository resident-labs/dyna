package computer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/resident-labs/dyna/contract"
)

// With no worker draining the queue, as when Chromium hangs, Enqueue returns at once and drops the overflow.
func TestInputEnqueueNeverBlocks(t *testing.T) {
	in := NewInput("http://unused")
	accepted := 0
	for range 100 {
		if in.Enqueue(contract.ComputerInputBatch{}) {
			accepted++
		}
	}
	if accepted != 64 {
		t.Fatalf("accepted %d batches, want 64", accepted)
	}
}

// fakePage serves a DevTools page with a 640x400 CSS viewport and records every call it gets.
func fakePage(t *testing.T) (string, *[]message) {
	t.Helper()
	sent := &[]message{}
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `[{"type":"page","webSocketDebuggerUrl":%q}]`, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws")
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var m message
			if json.Unmarshal(data, &m) != nil {
				return
			}
			*sent = append(*sent, m)
			result := `{}`
			if m.Method == "Page.getLayoutMetrics" {
				result = `{"cssVisualViewport":{"clientWidth":640,"clientHeight":400}}`
			}
			if conn.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`{"id":%d,"result":%s}`, m.ID, result))) != nil {
				return
			}
		}
	})
	return server.URL, sent
}

func TestInputMapsFramePixelsToCSSPixels(t *testing.T) {
	url, sent := fakePage(t)
	in := NewInput(url)
	// The frame is twice the CSS viewport, so a click at frame (640, 400) lands at CSS (320, 200).
	batch := contract.ComputerInputBatch{Frame: contract.FrameSize{Width: 1280, Height: 800}, Events: []contract.ComputerInput{
		{Kind: contract.InputMouseDown, Button: contract.ButtonLeft, X: 640, Y: 400},
		{Kind: contract.InputKeyDown, Key: "a"},
	}}
	if err := in.Send(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	defer in.d.Close()
	var got []string
	for _, m := range *sent {
		got = append(got, m.Method+" "+string(m.Params))
	}
	want := []string{
		"Page.getLayoutMetrics ",
		`Input.dispatchMouseEvent {"type":"mousePressed","x":320,"y":200,"button":"left","buttons":1,"clickCount":1,"deltaX":0,"deltaY":0}`,
		`Input.dispatchKeyEvent {"type":"keyDown","key":"a","text":"a","windowsVirtualKeyCode":65}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A dropped batch may have held the release of a key or button. The next batch lifts them first.
func TestInputReleasesHeldInputAfterOverflow(t *testing.T) {
	url, sent := fakePage(t)
	in := NewInput(url)
	press := contract.ComputerInputBatch{Frame: contract.FrameSize{Width: 1280, Height: 800}, Events: []contract.ComputerInput{
		{Kind: contract.InputMouseDown, Button: contract.ButtonLeft, X: 640, Y: 400},
		{Kind: contract.InputKeyDown, Key: "a"},
	}}
	if err := in.Send(context.Background(), press); err != nil {
		t.Fatal(err)
	}
	defer in.d.Close()
	for range 65 {
		in.Enqueue(contract.ComputerInputBatch{})
	}
	*sent = nil
	if err := in.Send(context.Background(), contract.ComputerInputBatch{}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range *sent {
		got = append(got, m.Method+" "+string(m.Params))
	}
	want := []string{
		`Input.dispatchKeyEvent {"type":"keyUp","key":"a","windowsVirtualKeyCode":65}`,
		`Input.dispatchMouseEvent {"type":"mouseReleased","x":320,"y":200,"button":"left","buttons":0,"clickCount":1,"deltaX":0,"deltaY":0}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
