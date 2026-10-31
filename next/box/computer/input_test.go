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

// fakePage serves a DevTools page with a 640x400 CSS viewport and records every call it gets. It answers
// a call with an error when fail says so.
func fakePage(t *testing.T, fail func(message) bool) (string, *[]message) {
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
			if fail != nil && fail(m) {
				if conn.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`{"id":%d,"error":{"message":"boom"}}`, m.ID))) != nil {
					return
				}
				continue
			}
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
	url, sent := fakePage(t, nil)
	in := NewInput(url)
	// The frame is twice the CSS viewport, so a click at frame (640, 400) lands at CSS (320, 200).
	batch := contract.ComputerInputBatch{Frame: contract.FrameSize{Width: 1280, Height: 800}, Events: []contract.ComputerInput{
		{Kind: contract.InputMouseDown, Button: contract.ButtonLeft, X: 640, Y: 400},
		{Kind: contract.InputKeyDown, Key: "a"},
	}}
	if err := in.Send(context.Background(), false, batch); err != nil {
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
	url, sent := fakePage(t, nil)
	in := NewInput(url)
	press := contract.ComputerInputBatch{Frame: contract.FrameSize{Width: 1280, Height: 800}, Events: []contract.ComputerInput{
		{Kind: contract.InputMouseDown, Button: contract.ButtonLeft, X: 640, Y: 400},
		{Kind: contract.InputKeyDown, Key: "a"},
	}}
	if err := in.Send(context.Background(), false, press); err != nil {
		t.Fatal(err)
	}
	defer in.d.Close()
	// A press is queued, then its release is dropped because the queue is full.
	in.Enqueue(press)
	for range 64 {
		in.Enqueue(contract.ComputerInputBatch{})
	}
	*sent = nil
	batches, release := in.collect(<-in.queue)
	if err := in.Send(context.Background(), release, batches...); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range *sent {
		got = append(got, m.Method+" "+string(m.Params))
	}
	want := []string{
		"Page.getLayoutMetrics ",
		`Input.dispatchMouseEvent {"type":"mousePressed","x":320,"y":200,"button":"left","buttons":1,"clickCount":1,"deltaX":0,"deltaY":0}`,
		`Input.dispatchKeyEvent {"type":"keyDown","key":"a","text":"a","windowsVirtualKeyCode":65}`,
		`Input.dispatchKeyEvent {"type":"keyUp","key":"a","windowsVirtualKeyCode":65}`,
		`Input.dispatchMouseEvent {"type":"mouseReleased","x":320,"y":200,"button":"left","buttons":0,"clickCount":1,"deltaX":0,"deltaY":0}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// If a batch fails while a release is due, the release is retried at once on a new connection.
func TestInputRetriesReleaseAfterDispatchFailure(t *testing.T) {
	url, sent := fakePage(t, func(m message) bool {
		return m.Method == "Input.dispatchKeyEvent" && strings.Contains(string(m.Params), `"type":"keyDown","key":"b"`)
	})
	in := NewInput(url)
	frame := contract.FrameSize{Width: 1280, Height: 800}
	press := contract.ComputerInputBatch{Frame: frame, Events: []contract.ComputerInput{
		{Kind: contract.InputMouseDown, Button: contract.ButtonLeft, X: 640, Y: 400},
		{Kind: contract.InputKeyDown, Key: "a"},
	}}
	if err := in.Send(context.Background(), false, press); err != nil {
		t.Fatal(err)
	}
	*sent = nil
	bad := contract.ComputerInputBatch{Frame: frame, Events: []contract.ComputerInput{{Kind: contract.InputKeyDown, Key: "b"}}}
	if err := in.Send(context.Background(), true, bad); err == nil {
		t.Fatal("want the failed batch reported")
	}
	defer in.d.Close()
	got := map[string]bool{}
	for _, m := range *sent {
		got[m.Method+" "+string(m.Params)] = true
	}
	for _, want := range []string{
		`Input.dispatchKeyEvent {"type":"keyUp","key":"a","windowsVirtualKeyCode":65}`,
		`Input.dispatchMouseEvent {"type":"mouseReleased","x":320,"y":200,"button":"left","buttons":0,"clickCount":1,"deltaX":0,"deltaY":0}`,
	} {
		if !got[want] {
			t.Fatalf("missing %s in %v", want, got)
		}
	}
	if in.overflow.Load() {
		t.Fatal("release succeeded, so no retry is owed")
	}
}

// A mouse release that fails stays owed: the retry sends it again instead of treating the button as lifted.
func TestInputRetriesFailedMouseRelease(t *testing.T) {
	failed := false
	url, sent := fakePage(t, func(m message) bool {
		if m.Method != "Input.dispatchMouseEvent" || !strings.Contains(string(m.Params), `"mouseReleased"`) || failed {
			return false
		}
		failed = true
		return true
	})
	in := NewInput(url)
	press := contract.ComputerInputBatch{Frame: contract.FrameSize{Width: 1280, Height: 800}, Events: []contract.ComputerInput{
		{Kind: contract.InputMouseDown, Button: contract.ButtonLeft, X: 640, Y: 400},
	}}
	if err := in.Send(context.Background(), false, press); err != nil {
		t.Fatal(err)
	}
	*sent = nil
	if err := in.Send(context.Background(), true); err == nil {
		t.Fatal("want the failed release reported")
	}
	defer in.d.Close()
	releases := 0
	for _, m := range *sent {
		if strings.Contains(string(m.Params), `"mouseReleased"`) {
			releases++
		}
	}
	if releases != 2 || in.buttons != 0 {
		t.Fatalf("%d release calls, buttons %d; want 2 and 0", releases, in.buttons)
	}
}
