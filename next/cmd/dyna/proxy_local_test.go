package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	stdlog "log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// TestMain lets the engine's supervisor start this binary as its proxy child.
func TestMain(m *testing.M) {
	if os.Getenv("DYNA_TEST_CLI") != "" {
		if newRootCmd().Execute() != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// proxyKeyAPI serves the owner methods this stack needs from the real service.
type proxyKeyAPI struct {
	api.Service
	real *engine.Service
}

func (s proxyKeyAPI) ProxyKey(ctx context.Context, p contract.Principal, r contract.ProxyKeyRequest) (contract.ProxyKeyResponse, *contract.Error) {
	return s.real.ProxyKey(ctx, p, r)
}
func (s proxyKeyAPI) ProxyUsage(ctx context.Context, p contract.Principal, r contract.ProxyUsageRequest) (contract.ProxyUsageResponse, *contract.Error) {
	return s.real.ProxyUsage(ctx, p, r)
}

// fakeProvider is an OpenAI-style upstream. A streamed reply stops after its first
// chunk until the client says it arrived, so nothing on the way can buffer it.
type fakeProvider struct {
	calls   atomic.Int64
	arrived chan struct{}
}

func (f *fakeProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if r.Header.Get("Authorization") != "Bearer upstream-key" || json.NewDecoder(r.Body).Decode(&req) != nil || r.URL.Path != "/chat/completions" {
		http.Error(w, "unexpected request", http.StatusBadRequest)
		return
	}
	usage := `"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}`
	if !req.Stream {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],%s}`, req.Model, usage)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(rest string) {
		fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":%q,%s}\n\n", req.Model, rest)
		w.(http.Flusher).Flush()
	}
	chunk(`"choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]`)
	select {
	case <-f.arrived:
	case <-time.After(20 * time.Second):
	}
	chunk(`"choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]`)
	chunk(`"choices":[],` + usage)
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// localStack runs the real proxy child, the real owner socket and box socket, and dyna proxy serve.
type localStack struct {
	t        *testing.T
	st       *store.Store
	proxy    *model.Proxy
	provider *fakeProvider
	socket   string
	box      string
	port     int
	ctx      context.Context
}

const managementKey = "invented-management-key"
const localAccount = "dyna-key-00112233445566778899aabbccddeeff"

func startLocalStack(t *testing.T) *localStack {
	t.Helper()
	t.Setenv("DYNA_TEST_CLI", "1")
	ctx, cancel := context.WithCancel(context.Background())
	data := t.TempDir()
	st, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := model.NewProxy(filepath.Join(data, "proxy"), filepath.Join(data, "proxy", "auth"), managementKey)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{arrived: make(chan struct{})}
	upstream := httptest.NewServer(provider)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	service := engine.New(st, nil, nil, time.Now)
	stopped := make(chan struct{})
	go func() {
		proxy.Supervise(ctx, executable, st, nil, nil)
		close(stopped)
	}()
	server := api.New(proxyKeyAPI{real: service}, func(c api.Caller) http.Handler {
		return proxy.Handler(string(c.Principal.Session), []string{"claude-opus-5-5"}, []string{localAccount}, "", false)
	})
	server.SetLocalModelRoute(proxy.Local(st))
	runtime := filepath.Join(data, "run")
	sockets := api.NewSockets(server, runtime, uint32(os.Getuid()), nil)
	if err := sockets.OpenOwner(); err != nil {
		t.Fatal(err)
	}
	if err := sockets.AddBox("box_1", "ses_1", "room_1"); err != nil {
		t.Fatal(err)
	}
	s := &localStack{t: t, st: st, proxy: proxy, provider: provider, socket: filepath.Join(runtime, "owner.sock"), box: filepath.Join(runtime, "boxes", "box_1.sock"), ctx: ctx}
	s.addAccount(upstream.URL)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.port = listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	served := make(chan struct{})
	go func() {
		defer close(served)
		_, _ = s.cliOutput("proxy", "serve", "--port", strconv.Itoa(s.port))
	}()
	s.waitForServe()
	t.Cleanup(func() {
		cancel()
		<-served
		<-stopped
		_ = sockets.Close()
		upstream.Close()
		_ = proxy.Listener.Close()
		_ = st.Close()
	})
	return s
}

func (s *localStack) waitForServe() {
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.port))); err == nil {
			_ = c.Close()
			return
		}
	}
	s.t.Fatal("dyna proxy serve did not start")
}

func unixHTTP(socket string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}, Timeout: 30 * time.Second}
}

// addAccount saves a key as the engine does. A preset's models keep their plain names,
// so the provider answers for real model names, and its address is the fake's.
func (s *localStack) addAccount(url string) {
	s.t.Helper()
	client := unixHTTP(s.proxy.Socket)
	send := func(path string, body map[string]any) {
		s.t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, "http://cliproxy"+path, bytes.NewReader(data))
		if err != nil {
			s.t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+managementKey)
		res, err := client.Do(req)
		if err != nil {
			s.t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			s.t.Fatalf("%s: status %d", path, res.StatusCode)
		}
	}
	send("/dyna/keys", map[string]any{"id": localAccount, "provider": "zai", "key": "upstream-key", "baseUrl": url, "models": []string{"claude-opus-5-5", "gpt-5.6"}})
	if err := s.st.BindAccount(context.Background(), "account_1", localAccount, "zai", "Invented"); err != nil {
		s.t.Fatal(err)
	}
	send("/dyna/credentials/activate", map[string]any{"id": localAccount, "model": "claude-opus-5-5"})
}

func (s *localStack) cliOutput(args ...string) (string, error) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"--socket", s.socket}, args...))
	err := root.ExecuteContext(s.ctx)
	return out.String(), err
}

func (s *localStack) cli(args ...string) string {
	s.t.Helper()
	out, err := s.cliOutput(args...)
	if err != nil {
		s.t.Fatalf("dyna %v: %v", args, err)
	}
	return out
}

// env runs dyna proxy env as an agent would, and reads back what a shell would export.
func (s *localStack) env(tool string) map[string]string {
	s.t.Helper()
	env := map[string]string{}
	for _, line := range strings.Split(s.cli("proxy", "env", "--tool", tool, "--port", strconv.Itoa(s.port)), "\n") {
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "codex() {") || line == "" {
			continue
		}
		name, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok || !strings.HasPrefix(line, "export ") {
			s.t.Fatalf("not an export line: %q", line)
		}
		env[name] = value
	}
	return env
}

func sendJSON(t *testing.T, client *http.Client, url, key, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out bytes.Buffer
	_, _ = out.ReadFrom(res.Body)
	return res.StatusCode, out.String()
}

// stream reads a streamed reply and lets the provider finish once the first text arrives.
func (s *localStack) stream(url, key, body, first string) string {
	s.t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		s.t.Fatalf("stream status %d", res.StatusCode)
	}
	var all strings.Builder
	reader := bufio.NewReader(res.Body)
	released := false
	for {
		line, err := reader.ReadString('\n')
		all.WriteString(line)
		if !released && strings.Contains(line, first) {
			released = true
			s.provider.arrived <- struct{}{}
		}
		if err != nil {
			break
		}
	}
	if !released {
		s.t.Fatalf("first text never arrived: %s", all.String())
	}
	return all.String()
}

func TestProxyEnvDrivesRealClients(t *testing.T) {
	var logged bytes.Buffer
	stdlog.SetOutput(&logged)
	defer stdlog.SetOutput(os.Stderr)
	s := startLocalStack(t)

	claude, codex := s.env("claude"), s.env("codex")
	if len(claude) != 2 || len(codex) != 2 {
		t.Fatalf("env: %v %v", claude, codex)
	}
	key := claude["ANTHROPIC_AUTH_TOKEN"]
	if key == "" || codex["OPENAI_API_KEY"] != key {
		t.Fatal("one key should serve every tool")
	}
	anthropic, openai := claude["ANTHROPIC_BASE_URL"], codex["OPENAI_BASE_URL"]
	message := `{"model":"claude-opus-5-5","max_tokens":20,"messages":[{"role":"user","content":"hi"}]%s}`
	chat := `{"model":"gpt-5.6","messages":[{"role":"user","content":"hi"}]%s}`
	responses := `{"model":"gpt-5.6","input":"hi"%s}`
	client := http.DefaultClient

	// A box's own traffic comes first. It must not appear in the local client's totals below.
	box := unixHTTP(s.box)
	if code, body := sendJSON(t, box, "http://box/model/v1/messages", "", fmt.Sprintf(message, "")); code != http.StatusOK {
		t.Fatalf("box route: %d %s", code, body)
	}
	if code, _ := sendJSON(t, box, "http://box/model/v1/chat/completions", key, fmt.Sprintf(chat, "")); code != http.StatusForbidden {
		t.Fatalf("the owner's key lifted a box's profile: %d", code)
	}
	if code, _ := sendJSON(t, box, "http://box/v1/proxy.key", "", `{}`); code != http.StatusForbidden {
		t.Fatalf("a box read the key: %d", code)
	}

	if code, body := sendJSON(t, client, anthropic+"/v1/messages", key, fmt.Sprintf(message, "")); code != http.StatusOK || !strings.Contains(body, "hello") {
		t.Fatalf("messages: %d %s", code, body)
	}
	if body := s.stream(anthropic+"/v1/messages", key, fmt.Sprintf(message, `,"stream":true`), "hello"); !strings.Contains(body, "message_stop") || !strings.Contains(body, " world") {
		t.Fatalf("messages stream: %s", body)
	}
	if code, body := sendJSON(t, client, openai+"/chat/completions", codex["OPENAI_API_KEY"], fmt.Sprintf(chat, "")); code != http.StatusOK || !strings.Contains(body, "hello") {
		t.Fatalf("chat: %d %s", code, body)
	}
	if body := s.stream(openai+"/chat/completions", key, fmt.Sprintf(chat, `,"stream":true`), "hello"); !strings.Contains(body, "[DONE]") {
		t.Fatalf("chat stream: %s", body)
	}
	if body := s.stream(openai+"/responses", key, fmt.Sprintf(responses, `,"stream":true`), "hello"); !strings.Contains(body, "response.completed") {
		t.Fatalf("responses stream: %s", body)
	}

	req, err := http.NewRequest(http.MethodGet, openai+"/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var listed bytes.Buffer
	_, _ = listed.ReadFrom(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(listed.String(), "claude-opus-5-5") {
		t.Fatalf("models: %d %s", res.StatusCode, listed.String())
	}

	before := s.provider.calls.Load()
	if code, body := sendJSON(t, client, anthropic+"/v1/messages", "wrong", fmt.Sprintf(message, "")); code != http.StatusUnauthorized || !strings.Contains(body, "dyna proxy env") {
		t.Fatalf("wrong key: %d %s", code, body)
	}
	if code, body := sendJSON(t, client, anthropic+"/v1/messages", key, strings.Replace(fmt.Sprintf(message, ""), "claude-opus-5-5", "no-such-model", 1)); code != http.StatusBadRequest || !strings.Contains(body, "unknown provider for model no-such-model") {
		t.Fatalf("unknown model: %d %s", code, body)
	}
	if code, _ := sendJSON(t, client, fmt.Sprintf("http://127.0.0.1:%d/v1/room.list", s.port), key, `{}`); code != http.StatusNotFound {
		t.Fatalf("the port reached the owner API: %d", code)
	}
	if s.provider.calls.Load() != before {
		t.Fatal("a refused request reached the provider")
	}

	s.waitForUsage(contract.ProxyUsage{Model: "claude-opus-5-5", Requests: 2, InputTokens: 22, OutputTokens: 14}, contract.ProxyUsage{Model: "gpt-5.6", Requests: 3, InputTokens: 33, OutputTokens: 21})
	if table := s.cli("proxy", "usage"); !strings.Contains(table, "local") || !strings.Contains(table, "claude-opus-5-5") {
		t.Fatalf("usage table: %s", table)
	}

	s.cli("proxy", "reset")
	if code, _ := sendJSON(t, client, anthropic+"/v1/messages", key, fmt.Sprintf(message, "")); code != http.StatusUnauthorized {
		t.Fatalf("the old key survived a reset: %d", code)
	}
	next := s.env("claude")["ANTHROPIC_AUTH_TOKEN"]
	if next == key {
		t.Fatal("reset kept the key")
	}
	if code, body := sendJSON(t, client, anthropic+"/v1/messages", next, fmt.Sprintf(message, "")); code != http.StatusOK {
		t.Fatalf("new key: %d %s", code, body)
	}
	for _, secret := range []string{key, next} {
		if strings.Contains(logged.String(), secret) {
			t.Fatal("the proxy key reached a log")
		}
	}
}

// waitForUsage waits for the totals, which arrive after the replies, and holds no more than these models.
func (s *localStack) waitForUsage(want ...contract.ProxyUsage) {
	s.t.Helper()
	var got []contract.ProxyUsage
	matches := func() bool {
		if len(got) != len(want) {
			return false
		}
		for _, w := range want {
			found := false
			for _, g := range got {
				g.LastAt = time.Time{}
				found = found || g == w
			}
			if !found {
				return false
			}
		}
		return true
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var err error
		if got, err = s.st.LocalUsage(context.Background()); err != nil {
			s.t.Fatal(err)
		}
		if matches() {
			return
		}
	}
	s.t.Fatalf("usage = %+v, want %+v", got, want)
}

func TestUsageCountsOnlyTheLocalClient(t *testing.T) {
	for id, want := range map[string]bool{
		"claude:local":           true,
		"claude:local:agent:one": true,
		"claude:ses_1":           false,
		"claude:localish":        false,
		"":                       false,
	} {
		if got := fromLocalClient(coreusage.Record{SessionID: id}); got != want {
			t.Errorf("%q: %v", id, got)
		}
	}
	if fromLocalClient(coreusage.Record{SessionID: "claude:local", Failed: true}) {
		t.Error("a failed attempt counted")
	}
}

func TestProxyEnvPrintsOnlyWhatTheToolNeeds(t *testing.T) {
	claude, codex := proxyEnv("claude", 7791, "key"), proxyEnv("codex", 7791, "key")
	if strings.Contains(claude, "OPENAI") || strings.Contains(claude, "codex()") || !strings.Contains(claude, "export ANTHROPIC_BASE_URL=http://127.0.0.1:7791/model\n") {
		t.Fatalf("claude: %s", claude)
	}
	if strings.Contains(codex, "ANTHROPIC") || !strings.Contains(codex, "export OPENAI_BASE_URL=http://127.0.0.1:7791/model/v1\n") || !strings.Contains(codex, `model_providers.dyna.base_url="http://127.0.0.1:7791/model/v1"`) {
		t.Fatalf("codex: %s", codex)
	}
	if both := proxyEnv("", 7791, "key"); !strings.Contains(both, "ANTHROPIC_AUTH_TOKEN") || !strings.Contains(both, "OPENAI_API_KEY") {
		t.Fatalf("both: %s", both)
	}
}

func TestProxyEnvRefusesAnUnknownTool(t *testing.T) {
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"proxy", "env", "--tool", "other"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "claude or codex") {
		t.Fatalf("err = %v", err)
	}
}

func TestProxyWithoutConfigShowsHelp(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"proxy"})
	if err := root.Execute(); err != nil || !strings.Contains(out.String(), "serve") {
		t.Fatalf("err = %v, help = %s", err, out.String())
	}
}
