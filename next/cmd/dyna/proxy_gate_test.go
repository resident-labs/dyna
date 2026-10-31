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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
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
	var gotPath, gotAuth, gotModel, gotHeader string
	redirectAuth := make(chan string, 1)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectAuth <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer redirect.Close()
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotPath, gotAuth, gotModel, gotHeader = r.URL.Path, r.Header.Get("Authorization"), body.Model, r.Header.Get("X-Static")
		if body.Model == "redirect" {
			http.Redirect(w, r, strings.Replace(redirect.URL, "127.0.0.1", "localhost", 1), http.StatusTemporaryRedirect)
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
	entry := config.OpenAICompatibility{Name: "fixture", Prefix: "fixture", BaseURL: upstream.URL + "/v1", DynaCatalogOnly: true, Headers: map[string]string{"X-Static": "fixed"}, Models: []config.OpenAICompatibilityModel{{Name: "same", Alias: "same"}, {Name: "redirect", Alias: "redirect"}}}
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
		{"key", "fixture", "key-secret", "same", entry},
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
	for _, tc := range []struct{ model, key, bare string }{{"plan_one/same", "one-secret", "same"}, {"plan_two/same", "two-secret", "same"}, {"fixture/same", "key-secret", "same"}, {"equal/equal", "equal-secret", "equal"}} {
		status, data := call(tc.model)
		if status != 200 || !strings.Contains(data, "hello") || gotPath != "/v1/chat/completions" || gotAuth != "Bearer "+tc.key || gotModel != tc.bare {
			t.Fatalf("%s: status=%d body=%q path=%q auth=%q model=%q", tc.model, status, data, gotPath, gotAuth, gotModel)
		}
		if tc.model == "fixture/same" && gotHeader != "fixed" {
			t.Fatalf("static header=%q", gotHeader)
		}
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
