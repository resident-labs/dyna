package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// The real proxy carries the image blocks Pi writes to the provider, on API keys so that the test stays on this machine:
// a Claude sign-in first asks Anthropic for its profile. The bodies have the shape real Pi sends for an attached image.
func TestProxyCarriesImagesToTheProvider(t *testing.T) {
	const image = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	var mu sync.Mutex
	upstream := map[string]string{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		upstream[r.URL.Path] = string(data)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(r.URL.Path, "/messages") {
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-image-test\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer provider.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := fmt.Sprintf(`auth-dir: %s
api-keys: [client-key]
claude-api-key:
  - api-key: sk-ant-api03-image-test
    base-url: %s
    models:
      - {name: claude-image-test, alias: claude-image-test}
codex-api-key:
  - api-key: codex-image-test
    base-url: %s
    models:
      - {name: gpt-image-test, alias: gpt-image-test}
`, filepath.Join(dir, "auth"), provider.URL, provider.URL)
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "proxy.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	service, err := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(path).WithListener(listener).Build()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	go func() { _ = service.Run(ctx) }()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	for route, body := range map[string]string{
		"/v1/messages":  `{"model":"claude-image-test","max_tokens":16,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"[Dyna] Frame."},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + image + `"}}]}]}`,
		"/v1/responses": `{"model":"gpt-image-test","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"[Dyna] Frame."},{"type":"input_image","detail":"auto","image_url":"data:image/png;base64,` + image + `"}]}]}`,
	} {
		var status int
		var reply string
		for ctx.Err() == nil {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://proxy"+route, bytes.NewBufferString(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer client-key")
			resp, err := client.Do(req)
			if err == nil {
				data, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				status, reply = resp.StatusCode, string(data)
				if status == http.StatusOK {
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s", route, status, reply)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(upstream) != 2 {
		t.Fatalf("the provider saw %v", len(upstream))
	}
	for path, body := range upstream {
		if !strings.Contains(body, image) {
			t.Errorf("%s lost the image: %.400s", path, body)
		}
		if strings.Contains(path, "/messages") && !strings.Contains(body, `"media_type":"image/png"`) {
			t.Errorf("%s: %.400s", path, body)
		}
		if !strings.Contains(path, "/messages") && !strings.Contains(body, "input_image") {
			t.Errorf("%s: %.400s", path, body)
		}
	}
}
