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

func TestInputMapsFramePixelsToCSSPixels(t *testing.T) {
	var sent []message
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
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
			sent = append(sent, m)
			result := `{}`
			if m.Method == "Page.getLayoutMetrics" {
				result = `{"cssVisualViewport":{"clientWidth":640,"clientHeight":400}}`
			}
			if conn.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`{"id":%d,"result":%s}`, m.ID, result))) != nil {
				return
			}
		}
	})
	in := &Input{Base: server.URL}
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
	for _, m := range sent {
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
