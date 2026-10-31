package computer

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestWatchForwardsAndAcknowledgesFrame(t *testing.T) {
	acked := make(chan int, 1)
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
			switch m.Method {
			case "Page.enable", "Page.startScreencast":
				if m.Method == "Page.startScreencast" {
					var params struct {
						EveryNthFrame int `json:"everyNthFrame"`
					}
					if json.Unmarshal(m.Params, &params) != nil || params.EveryNthFrame > 1 {
						t.Error("static pages must not skip their first frame")
					}
				}
				reply, _ := json.Marshal(message{ID: m.ID, Result: json.RawMessage(`{}`)})
				if conn.Write(r.Context(), websocket.MessageText, reply) != nil {
					return
				}
				if m.Method == "Page.startScreencast" {
					frame := []byte(`{"method":"Page.screencastFrame","params":{"data":"anBlZw==","sessionId":17}}`)
					if conn.Write(r.Context(), websocket.MessageText, frame) != nil {
						return
					}
				}
			case "Page.screencastFrameAck":
				var params struct {
					SessionID int `json:"sessionId"`
				}
				_ = json.Unmarshal(m.Params, &params)
				acked <- params.SessionID
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- watch(ctx, server.URL, &out) }()
	if id := <-acked; id != 17 {
		t.Fatalf("ack session: %d", id)
	}
	cancel()
	<-done
	var n uint32
	if err := binary.Read(&out, binary.BigEndian, &n); err != nil || n != 4 || out.String() != "jpeg" {
		t.Fatalf("frame: %d %q %v", n, out.String(), err)
	}
}
