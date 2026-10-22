// Package opencodefixture runs the pinned OpenCode service against a local
// provider. It is for tests only and never contacts an external model.
package opencodefixture

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type Fixture struct {
	Binary   string
	Env      map[string]string
	mu       sync.Mutex
	requests []string
}

// New starts one service. Respond may return one OpenAI-compatible stream
// chunk; nil uses a plain text response. Only model requests with tools are
// recorded, so title generation does not change assertions.
func New(t testing.TB, config map[string]any, respond func(map[string]any) string) *Fixture {
	t.Helper()
	binary := os.Getenv("OPENCODE_INTEGRATION_BINARY")
	if binary == "" {
		t.Skip("set OPENCODE_INTEGRATION_BINARY to the pinned v2 bundle")
	}
	f := &Fixture{Binary: binary}
	root := t.TempDir()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
			f.mu.Lock()
			f.requests = append(f.requests, string(raw))
			f.mu.Unlock()
		}
		response := ""
		if respond != nil {
			response = respond(body)
		}
		if response == "" {
			response = Text("done")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+response+"\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(provider.Close)
	if config == nil {
		config = map[string]any{}
	}
	if config["update"] == nil {
		config["update"] = "disable"
	}
	providers, _ := config["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
		config["providers"] = providers
	}
	canary, _ := providers["canary"].(map[string]any)
	if canary == nil {
		canary = map[string]any{}
		providers["canary"] = canary
	}
	canary["env"] = []string{"CANARY_API_KEY"}
	canary["package"] = "@opencode/ai/providers/openai-compatible"
	canary["settings"] = map[string]any{"baseURL": provider.URL + "/v1"}
	if canary["models"] == nil {
		canary["models"] = map[string]any{"probe": map[string]any{"name": "Probe", "limit": map[string]any{"context": 200000, "output": 4096}}}
	}
	configPath := filepath.Join(root, "config.json")
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	f.Env = map[string]string{"HOME": root, "XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_DATA_HOME": filepath.Join(root, "data"), "XDG_CACHE_HOME": filepath.Join(root, "cache"), "XDG_STATE_HOME": filepath.Join(root, "state"), "OPENCODE_TEST_HOME": root, "OPENCODE_CONFIG_DIR": filepath.Join(root, "config", "opencode"), "OPENCODE_DB": filepath.Join(root, "data", "opencode.db"), "OPENCODE_CONFIG": configPath, "OPENCODE_CONFIG_CONTENT": "{}", "OPENCODE_DISABLE_MODELS_FETCH": "1", "CANARY_API_KEY": "fixture", "OPENCODE_PASSWORD": "fixture-password"}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, binary, "serve", "--service", "--hostname", "127.0.0.1", "--port", "0")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"}
	for k, v := range f.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Dir = root
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		pattern := regexp.MustCompile(`http://127\.0\.0\.1:[0-9]+`)
		for scanner.Scan() {
			if url := pattern.FindString(scanner.Text()); url != "" {
				ready <- url
				return
			}
		}
		ready <- ""
	}()
	var url string
	select {
	case url = <-ready:
	case <-ctx.Done():
		t.Fatal("OpenCode service did not become ready")
	}
	if url == "" {
		t.Fatal("OpenCode service exited before readiness")
	}
	binDir := filepath.Join(root, "bin")
	if err = os.Mkdir(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(binary, filepath.Join(binDir, "opencode")); err != nil {
		t.Fatal(err)
	}
	f.Env["PATH"] = binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	return f
}

func (f *Fixture) Last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return ""
	}
	return f.requests[len(f.requests)-1]
}
func (f *Fixture) Requests() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.requests, "\n")
}
func Text(text string) string { return chunk(map[string]any{"content": text}, "stop") }
func Tool(name string, args any) string {
	raw, _ := json.Marshal(args)
	return chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_canary", "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}, "tool_calls")
}
func chunk(delta any, reason string) string {
	raw, _ := json.Marshal(map[string]any{"id": "probe", "object": "chat.completion.chunk", "created": 0, "model": "probe", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}, "usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 1, "total_tokens": 121}})
	return string(raw)
}
