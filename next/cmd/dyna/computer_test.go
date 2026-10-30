package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestComputerText(t *testing.T) {
	mux := http.NewServeMux()
	devtools := httptest.NewServer(mux)
	defer devtools.Close()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"type":"page","webSocketDebuggerUrl":%q}]`, "ws"+strings.TrimPrefix(devtools.URL, "http")+"/page")
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		_, data, err := ws.Read(r.Context())
		if err != nil {
			return
		}
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(data, &request) != nil || request.Method != "Runtime.evaluate" {
			return
		}
		response := fmt.Sprintf(`{"id":%d,"result":{"result":{"value":"hello"}}}`, request.ID)
		_ = ws.Write(context.Background(), websocket.MessageText, []byte(response))
	})
	path := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/computer" {
			http.Error(w, "unexpected", 400)
			return
		}
		fmt.Fprintf(w, `{"url":%q,"exit":""}`, devtools.URL)
	})}
	go server.Serve(listener)
	defer server.Close()
	cmd := computerCommand(path)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"text", "main"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello\n" {
		t.Fatalf("text = %q", out.String())
	}
}
