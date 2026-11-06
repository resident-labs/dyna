package model

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// cooldownHTTPExecutor is a fixture, not the Codex wire translator. It forwards
// to a local fake upstream and returns its status/body through the real manager's
// executor contract. Selection, configured error classification and cooldown
// mutation below are the pinned CLIProxyAPI implementation, not a fake manager.
type cooldownHTTPExecutor struct {
	coreauth.ProviderExecutor
	url string
}

func (e cooldownHTTPExecutor) Identifier() string { return "codex" }
func (e cooldownHTTPExecutor) Execute(ctx context.Context, _ *coreauth.Auth, _ executor.Request, _ executor.Options) (executor.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, nil)
	if err != nil {
		return executor.Response{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return executor.Response{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return executor.Response{}, err
	}
	if resp.StatusCode >= 400 {
		code := "server_error"
		if resp.Header.Get("X-Fixture-Capacity") != "" {
			code = coreauth.ErrorCodeTransientRateLimit
		}
		return executor.Response{}, &coreauth.Error{Code: code, Message: string(body), HTTPStatus: resp.StatusCode}
	}
	return executor.Response{Payload: body}, nil
}

func TestProxySingleCodexCredentialCooldownPolicy(t *testing.T) {
	p, err := NewProxy(filepath.Join(t.TempDir(), "proxy"), t.TempDir(), "management")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Listener.Close()
	cfg, err := config.LoadConfig(p.Config)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DisableCooling || cfg.TransientErrorCooldownSeconds != 0 {
		t.Fatal("broad cooling settings must retain defaults")
	}
	cases := []struct {
		name           string
		status         int
		capacity, cool bool
	}{
		{"server_error", 500, false, false},
		{"bad_gateway", 502, false, false},
		{"unavailable", 503, false, false},
		{"timeout", 504, false, false},
		{"other_5xx", 599, false, false},
		{"unauthorized", 401, false, true},
		{"forbidden", 403, false, true},
		{"quota", 429, false, true},
		{"capacity_without_retry_hint", 429, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					if tc.capacity {
						w.Header().Set("X-Fixture-Capacity", "true")
					}
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"transient fixture failure"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			defer upstream.Close()
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			// A zero retry budget isolates successive downstream requests. PiG owns
			// short model-call retries; this must work even after a request ends in 5xx.
			manager.SetRetryConfig(0, 0, 1)
			manager.RegisterExecutor(cooldownHTTPExecutor{url: upstream.URL})
			ctx := context.Background()
			cliproxy.GlobalModelRegistry().RegisterClient("only-codex", "codex", []*cliproxy.ModelInfo{{ID: "gpt-5.1-codex"}})
			t.Cleanup(func() { cliproxy.GlobalModelRegistry().UnregisterClient("only-codex") })
			if _, err := manager.Register(ctx, &coreauth.Auth{ID: "only-codex", Provider: "codex", Status: coreauth.StatusActive,
				Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth}}); err != nil {
				t.Fatal(err)
			}
			req := executor.Request{Model: "gpt-5.1-codex"}
			_, err := manager.Execute(ctx, []string{"codex"}, req, executor.Options{})
			if err == nil {
				t.Fatal("first request must return upstream failure")
			}
			resp, nextErr := manager.Execute(ctx, []string{"codex"}, req, executor.Options{})
			if !tc.cool {
				if nextErr != nil || string(resp.Payload) != `{"ok":true}` || calls.Load() != 2 {
					t.Fatalf("one credential failed to recover after %d: response=%s error=%v upstream calls=%d", tc.status, resp.Payload, nextErr, calls.Load())
				}
			} else if nextErr == nil || calls.Load() != 1 {
				t.Fatalf("%d cooldown was weakened: error=%v upstream calls=%d", tc.status, nextErr, calls.Load())
			}
		})
	}
}
