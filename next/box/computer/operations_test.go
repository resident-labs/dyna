package computer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestFullScreenshotUsesContentSize(t *testing.T) {
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
			result := `{"cssContentSize":{"x":0,"y":0,"width":800,"height":2000}}`
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
	if err := Execute(context.Background(), d, Screenshot, []string{filepath.Join(t.TempDir(), "shot.png")}, "", true, io.Discard); err != nil {
		t.Fatal(err)
	}
	clip := <-clips
	if clip.Width != 800 || clip.Height != 2000 || clip.Scale != 1 {
		t.Fatalf("clip = %+v", clip)
	}
}
