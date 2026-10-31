package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/rpc"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	engstore "github.com/resident-labs/dyna/engine/store"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

type gateStore struct {
	mu    sync.Mutex
	auths map[string]*coreauth.Auth
}

func (s *gateStore) List(context.Context) ([]*coreauth.Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*coreauth.Auth, 0, len(s.auths))
	for _, a := range s.auths {
		out = append(out, a.Clone())
	}
	return out, nil
}
func (s *gateStore) Save(_ context.Context, a *coreauth.Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auths[a.ID] = a.Clone()
	return a.ID, nil
}
func (s *gateStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.auths, id)
	return nil
}

type gateListener struct {
	net.Listener
	once  sync.Once
	ready chan struct{}
}

func (l *gateListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.ready) })
	return l.Listener.Accept()
}

// TestCompletionsProxyGate runs the real proxy with only TLS loopback upstreams.
func TestCompletionsProxyGate(t *testing.T) {
	var gotPath, gotAuth, gotModel, gotHeader, gotSystem string
	redirectAuth := make(chan string, 1)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectAuth <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer redirect.Close()
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model  string          `json:"model"`
			System json.RawMessage `json:"system"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotPath, gotAuth, gotModel, gotHeader, gotSystem = r.URL.Path, r.Header.Get("Authorization"), body.Model, r.Header.Get("X-Static"), string(body.System)
		if body.Model == "redirect" {
			http.Redirect(w, r, strings.Replace(redirect.URL, "127.0.0.1", "localhost", 1), http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path == "/v1/messages" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"fixture","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil || (host != "127.0.0.1" && host != "localhost") {
			return nil, errors.New("test blocks external network")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	oldTransport := http.DefaultTransport
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = oldTransport; transport.CloseIdleConnections() }()

	dir := t.TempDir()
	cfg, err := config.ParseConfigBytes([]byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.AuthDir = dir
	cfg.APIKeys = []string{"client"}
	cfg.ForceModelPrefix = true
	entry := config.OpenAICompatibility{Name: "fixture", Prefix: "fixture", BaseURL: upstream.URL + "/v1", DynaCatalogOnly: true, Headers: map[string]string{"X-Static": "fixed"}, Models: []config.OpenAICompatibilityModel{{Name: "same", Alias: "same"}, {Name: "redirect", Alias: "redirect"}, {Name: "plan_equal", Alias: "plan_equal"}}}
	equal := config.OpenAICompatibility{Name: "equal", Prefix: "equal", BaseURL: upstream.URL + "/v1", DynaCatalogOnly: true, Models: []config.OpenAICompatibilityModel{{Name: "equal", Alias: "equal"}}}
	cfg.OpenAICompatibility = []config.OpenAICompatibility{entry, equal}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	store := &gateStore{auths: make(map[string]*coreauth.Auth)}
	sdkauth.RegisterTokenStore(store)
	defer sdkauth.RegisterTokenStore(nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	watched := &gateListener{Listener: listener, ready: make(chan struct{})}
	service, err := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(path).WithListener(watched).Build()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	select {
	case <-watched.ready:
	case err := <-done:
		cancel()
		t.Fatalf("proxy did not start: %v", err)
	}
	defer func() { cancel(); <-done }()
	for _, item := range []struct {
		id, prefix, key, model string
		entry                  config.OpenAICompatibility
	}{
		{"one", "plan_one", "one-secret", "same", entry},
		{"two", "plan_two", "two-secret", "same", entry},
		{"plan-equal", "plan_equal", "plan-equal-secret", "plan_equal", entry},
		{"equal", "equal", "equal-secret", "equal", equal},
	} {
		auth := keyAuth(item.entry, item.id, item.key)
		auth.Prefix = item.prefix
		if err := service.RegisterDynaCredential(ctx, auth); err != nil {
			t.Fatal(err)
		}
		if err := service.ActivateDynaCredential(ctx, item.id, item.prefix+"/"+item.model, item.prefix); err != nil {
			t.Fatal(err)
		}
	}
	// Add and activate the key through the real management routes.
	keyID := "dyna-key-00000000000000000000000000000001"
	rpcServer := rpc.NewServer()
	if err := rpcServer.RegisterName("Auth", &catalogAuthRPC{records: []engstore.ProxyAuth{{ID: keyID, Bound: true}}}); err != nil {
		t.Fatal(err)
	}
	serverConn, clientConn := net.Pipe()
	go rpcServer.ServeConn(serverConn)
	credentials := &proxyStore{client: rpc.NewClient(clientConn), catalog: []config.OpenAICompatibility{entry}}
	defer credentials.client.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte("management"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	proxyKeyRoutes(router, &service, credentials, string(hash))
	manage := func(path, body string) {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer management")
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("management %s: %d", path, response.Code)
		}
	}
	manage("/dyna/keys", `{"id":"`+keyID+`","provider":"fixture","key":"key-secret"}`)
	manage("/dyna/credentials/activate", `{"id":"`+keyID+`","model":"fixture/same","prefix":"fixture"}`)
	plan := &coreauth.Auth{ID: "real-plan", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": upstream.URL}, Metadata: map[string]any{"access_token": "sk-ant-oat-fixture", "account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}}
	if err := service.RegisterDynaCredential(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if err := service.ActivateDynaCredential(ctx, plan.ID, "plan_anthropic/claude-sonnet-4-6", "plan_anthropic"); err != nil {
		t.Fatal(err)
	}
	planBody := `{"model":"plan_anthropic/claude-sonnet-4-6","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`
	planRequest, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+listener.Addr().String()+"/v1/messages", strings.NewReader(planBody))
	planRequest.Header.Set("Authorization", "Bearer client")
	planRequest.Header.Set("Content-Type", "application/json")
	planResponse, err := http.DefaultClient.Do(planRequest)
	if err != nil {
		t.Fatal(err)
	}
	planData, _ := io.ReadAll(planResponse.Body)
	planResponse.Body.Close()
	if planResponse.StatusCode != http.StatusOK || !strings.Contains(string(planData), "hello") || gotPath != "/v1/messages" || gotModel != "claude-sonnet-4-6" || gotAuth != "Bearer sk-ant-oat-fixture" || !strings.Contains(gotSystem, "You are Claude Code") {
		t.Fatalf("Claude plan status=%d body=%q path=%q model=%q", planResponse.StatusCode, planData, gotPath, gotModel)
	}
	call := func(model string) (int, string) {
		payload, _ := json.Marshal(struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}{model, true})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+listener.Addr().String()+"/v1/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer client")
		req.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(data)
	}
	for _, tc := range []struct{ model, key, bare string }{{"plan_one/same", "one-secret", "same"}, {"plan_two/same", "two-secret", "same"}, {"fixture/same", "key-secret", "same"}, {"equal/equal", "equal-secret", "equal"}, {"plan_equal/plan_equal", "plan-equal-secret", "plan_equal"}} {
		status, data := call(tc.model)
		if status != 200 || !strings.Contains(data, "hello") || gotPath != "/v1/chat/completions" || gotAuth != "Bearer "+tc.key || gotModel != tc.bare {
			t.Fatalf("%s: status=%d body=%q path=%q auth=%q model=%q", tc.model, status, data, gotPath, gotAuth, gotModel)
		}
		if tc.model == "fixture/same" && gotHeader != "fixed" {
			t.Fatalf("static header=%q", gotHeader)
		}
	}
	if status, _ := call("fixture/equal"); status == 200 {
		t.Fatal("model from another provider routed")
	}
	if status, _ := call("plan_equal"); status == 200 {
		t.Fatal("bare plan prefix-equal model routed")
	}
	if status, _ := call("equal"); status == 200 {
		t.Fatal("bare prefix-equal model routed")
	}
	call("fixture/redirect")
	select {
	case auth := <-redirectAuth:
		if auth != "" {
			t.Fatal("credential crossed hosts on redirect")
		}
	default:
	}
}
