package computer

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
			case "Page.captureScreenshot":
				reply, _ := json.Marshal(message{ID: m.ID, Result: json.RawMessage(`{"data":"c2hvdA=="}`)})
				if conn.Write(r.Context(), websocket.MessageText, reply) != nil {
					return
				}
				for _, frame := range []string{
					`{"method":"Page.screencastFrame","params":{"data":"b2xk","sessionId":18,"metadata":{"timestamp":1}}}`,
					fmt.Sprintf(`{"method":"Page.screencastFrame","params":{"data":"bmV3","sessionId":19,"metadata":{"timestamp":%d}}}`, time.Now().Unix()+60),
				} {
					if conn.Write(r.Context(), websocket.MessageText, []byte(frame)) != nil {
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
	go func() { done <- watch(ctx, server.URL, &out, func(context.Context) error { return nil }) }()
	for _, want := range []int{17, 18, 19} {
		if id := <-acked; id != want {
			t.Fatalf("ack session: got %d, want %d", id, want)
		}
	}
	cancel()
	<-done
	var first uint32
	if err := binary.Read(&out, binary.BigEndian, &first); err != nil || first != 4 {
		t.Fatalf("screenshot length: %d %v", first, err)
	}
	shot := make([]byte, first)
	_, _ = out.Read(shot)
	if string(shot) != "shot" {
		t.Fatalf("screenshot: %q", shot)
	}
	var n uint32
	if err := binary.Read(&out, binary.BigEndian, &n); err != nil || n != 3 || out.String() != "new" {
		t.Fatalf("new frame: %d %q %v", n, out.String(), err)
	}
}

func TestWatchSendsScreenshotOnIdlePage(t *testing.T) {
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
			result := json.RawMessage(`{}`)
			if m.Method == "Page.captureScreenshot" {
				result = json.RawMessage(`{"data":"anBlZw=="}`)
			}
			reply, _ := json.Marshal(message{ID: m.ID, Result: result})
			if conn.Write(r.Context(), websocket.MessageText, reply) != nil {
				return
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- watch(ctx, server.URL, writer, func(context.Context) error { return nil }) }()
	var size uint32
	if err := binary.Read(reader, binary.BigEndian, &size); err != nil || size != 4 {
		t.Fatalf("size %d: %v", size, err)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(reader, data); err != nil || string(data) != "jpeg" {
		t.Fatalf("frame %q: %v", data, err)
	}
	cancel()
	reader.Close()
	<-done
}

type expiredContext struct{ context.Context }

func (expiredContext) Err() error { return context.DeadlineExceeded }

func TestConnectTimesOutWithoutPage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
		cancel()
	}))
	defer server.Close()
	_, err := connect(expiredContext{ctx}, server.URL)
	var timeout ConnectTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("got %v, want ConnectTimeoutError", err)
	}
}
