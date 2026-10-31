package computer

import (
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

func TestWatchOrdersScreenshotAndScreencast(t *testing.T) {
	for _, frameFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("frameFirst=%t", frameFirst), func(t *testing.T) {
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
					if m.Method == "Page.screencastFrameAck" {
						continue
					}
					reply, _ := json.Marshal(message{ID: m.ID, Result: json.RawMessage(`{}`)})
					if m.Method == "Page.captureScreenshot" {
						frame := []byte(`{"method":"Page.screencastFrame","params":{"data":"bmV3","sessionId":17}}`)
						if frameFirst && conn.Write(r.Context(), websocket.MessageText, frame) != nil {
							return
						}
						reply, _ = json.Marshal(message{ID: m.ID, Result: json.RawMessage(`{"data":"b2xk"}`)})
						if conn.Write(r.Context(), websocket.MessageText, reply) != nil {
							return
						}
						if !frameFirst && conn.Write(r.Context(), websocket.MessageText, frame) != nil {
							return
						}
						end := []byte(`{"method":"Page.screencastFrame","params":{"data":"ZW5k","sessionId":18}}`)
						if conn.Write(r.Context(), websocket.MessageText, end) != nil {
							return
						}
						continue
					}
					if conn.Write(r.Context(), websocket.MessageText, reply) != nil {
						return
					}
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			reader, writer := io.Pipe()
			done := make(chan error, 1)
			go func() {
				err := watch(ctx, server.URL, writer, func(context.Context) error { return nil })
				writer.CloseWithError(err)
				done <- err
			}()
			defer cancel()
			defer reader.Close()
			want := []string{"old", "new", "end"}
			if frameFirst {
				want = []string{"new", "end"}
			}
			for _, expected := range want {
				var size uint32
				if err := binary.Read(reader, binary.BigEndian, &size); err != nil || size != uint32(len(expected)) {
					t.Fatalf("size %d: %v", size, err)
				}
				data := make([]byte, size)
				if _, err := io.ReadFull(reader, data); err != nil || string(data) != expected {
					t.Fatalf("frame %q, want %q: %v", data, expected, err)
				}
			}
			cancel()
			reader.Close()
			<-done
		})
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

func TestWatchSettlesAfterAFrameDuringCapture(t *testing.T) {
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
		shots := 0
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var m message
			if json.Unmarshal(data, &m) != nil {
				return
			}
			if m.Method == "Page.screencastFrameAck" {
				continue
			}
			result := json.RawMessage(`{}`)
			if m.Method == "Page.captureScreenshot" {
				shots++
				if shots == 1 {
					frame := []byte(`{"method":"Page.screencastFrame","params":{"data":"YQ==","sessionId":1}}`)
					if conn.Write(r.Context(), websocket.MessageText, frame) != nil {
						return
					}
				}
				result = json.RawMessage(`{"data":"Yg=="}`)
			}
			reply, _ := json.Marshal(message{ID: m.ID, Result: result})
			if conn.Write(r.Context(), websocket.MessageText, reply) != nil {
				return
			}
			if m.Method == "Page.captureScreenshot" && shots == 1 {
				marker := []byte(`{"method":"Page.screencastFrame","params":{"data":"YQ==","sessionId":2}}`)
				if conn.Write(r.Context(), websocket.MessageText, marker) != nil {
					return
				}
			}
		}
	})
	tick := make(chan time.Time, 1)
	armed := make(chan struct{}, 2)
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := watchWithTimer(ctx, server.URL, writer, func(context.Context) error { return nil }, func() (<-chan time.Time, func()) { armed <- struct{}{}; return tick, func() {} })
		writer.CloseWithError(err)
		done <- err
	}()
	defer cancel()
	defer reader.Close()
	read := func(want string) {
		t.Helper()
		var n uint32
		if err := binary.Read(reader, binary.BigEndian, &n); err != nil || n != uint32(len(want)) {
			t.Fatalf("length %d: %v", n, err)
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(reader, data); err != nil || string(data) != want {
			t.Fatalf("frame %q, want %q: %v", data, want, err)
		}
	}
	read("a")
	read("a")
	<-armed
	<-armed
	tick <- time.Time{}
	read("b")
	cancel()
	reader.Close()
	<-done
}
