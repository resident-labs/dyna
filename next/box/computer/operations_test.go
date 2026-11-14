package computer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSameDocumentNavigationDoesNotWaitForLoad(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"type":"page","webSocketDebuggerUrl":%q}]`, "ws"+strings.TrimPrefix(server.URL, "http")+"/page")
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for i := 0; i < 3; i++ { // enable, navigate, then the title
			_, data, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var req struct {
				ID int `json:"id"`
			}
			if json.Unmarshal(data, &req) != nil {
				return
			}
			_ = ws.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`{"id":%d,"result":{"frameId":"main"}}`, req.ID)))
		}
	})
	d, err := Connect(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := navigate(context.Background(), d, "https://example.test/#section"); err != nil {
		t.Fatal(err)
	}
}

func TestFullScreenshotUsesContentSize(t *testing.T) {
	for _, height := range []int{2000, 20000} {
		t.Run(fmt.Sprint(height), func(t *testing.T) {
			mux := http.NewServeMux()
			server := httptest.NewServer(mux)
			defer server.Close()
			mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `[{"type":"page","webSocketDebuggerUrl":%q}]`, "ws"+strings.TrimPrefix(server.URL, "http")+"/page")
			})
			clips := make(chan viewport, 1)
			mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer ws.CloseNow()
				for i := 0; i < 2; i++ {
					_, data, err := ws.Read(r.Context())
					if err != nil {
						return
					}
					var req struct {
						ID     int    `json:"id"`
						Method string `json:"method"`
						Params struct {
							Clip viewport `json:"clip"`
						} `json:"params"`
					}
					if json.Unmarshal(data, &req) != nil {
						return
					}
					result := fmt.Sprintf(`{"cssContentSize":{"x":0,"y":0,"width":800,"height":%d}}`, height)
					if req.Method == "Page.captureScreenshot" {
						clips <- req.Params.Clip
						result = `{"data":"` + strings.Repeat("cG5n", 12000) + `"}`
					}
					_ = ws.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`{"id":%d,"result":%s}`, req.ID, result)))
				}
			})
			d, err := Connect(context.Background(), server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			_, clipped, err := pageScreenshot(context.Background(), d)
			if clipped != (height > 16384) {
				t.Fatalf("clipped = %v", clipped)
			}
			if err != nil {
				t.Fatal(err)
			}
			clip := <-clips
			if clip.Width != 800 || clip.Height != float64(min(height, 16384)) || clip.Scale != 1 {
				t.Fatalf("clip = %+v", clip)
			}
		})
	}
}

func TestPageScreenshotTimeout(t *testing.T) {
	previous := pageActionTimeout
	pageActionTimeout = 50 * time.Millisecond
	defer func() { pageActionTimeout = previous }()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"type":"page","webSocketDebuggerUrl":%q}]`, "ws"+strings.TrimPrefix(server.URL, "http")+"/page")
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			_, data, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var req message
			if json.Unmarshal(data, &req) != nil {
				return
			}
			if req.Method == "Page.captureScreenshot" {
				continue
			}
			_ = ws.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`{"id":%d,"result":{"cssContentSize":{"width":800,"height":2000}}}`, req.ID)))
		}
	})
	_, err := run(context.Background(), Agent{}, server.URL, Screenshot, Request{Page: true})
	if err == nil || !strings.Contains(err.Error(), "take a desktop screenshot or use the visual tool") {
		t.Fatalf("error = %v", err)
	}
}
