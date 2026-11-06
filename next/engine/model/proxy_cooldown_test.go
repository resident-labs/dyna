package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"

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
func (e cooldownHTTPExecutor) Execute(ctx context.Context, _ *coreauth.Auth, input executor.Request, _ executor.Options) (executor.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(input.Payload))
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
	if cfg.DisableCooling || cfg.TransientErrorCooldownSeconds != -1 {
		t.Error("transient cooling must be disabled, without disabling auth/quota cooling")
	}
	// These are the same global setters used by Service.applyRetryConfig.
	coreauth.SetQuotaCooldownDisabled(cfg.DisableCooling)
	coreauth.SetTransientErrorCooldownSeconds(cfg.TransientErrorCooldownSeconds)
	t.Cleanup(func() { coreauth.SetTransientErrorCooldownSeconds(0); coreauth.SetQuotaCooldownDisabled(false) })
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
		{"capacity_without_retry_hint", 429, true, false},
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
			manager := newCooldownTestManager(t, cfg, upstream.URL)
			ctx := context.Background()
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

	t.Run("original request retries with PiG backoff then later request succeeds", func(t *testing.T) {
		var calls atomic.Int32
		attemptTimes := make(chan time.Time, 3)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := calls.Add(1)
			attemptTimes <- time.Now()
			if n == 1 {
				w.WriteHeader(503)
				_, _ = io.WriteString(w, `{"error":{"type":"server_error","code":"server_error","message":"upstream temporarily unavailable"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"fixture\",\"status\":\"completed\",\"output\":[]}}\n\n")
		}))
		defer upstream.Close()
		manager := newCooldownTestManager(t, cfg, upstream.URL)
		// This local adapter stands in for CLIProxyAPI's wire handler; Dyna's
		// real model route and Unix socket lead into the real auth manager.
		// PiG's real openai-responses provider owns the retry and delay.
		adapter := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+p.clientKey || r.URL.Path != "/v1/responses" {
				t.Error("Dyna route did not replace credentials and path")
				w.WriteHeader(403)
				return
			}
			payload, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			resp, err := manager.Execute(r.Context(), []string{"codex"}, executor.Request{Model: "gpt-5.1-codex", Payload: payload}, executor.Options{})
			if err != nil {
				var status interface{ StatusCode() int }
				code := 500
				if errors.As(err, &status) {
					code = status.StatusCode()
				}
				w.WriteHeader(code)
				_, _ = io.WriteString(w, err.Error())
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write(resp.Payload)
		})
		server := &http.Server{Handler: adapter}
		go func() { _ = server.Serve(p.Listener) }()
		defer server.Close()
		downstream := httptest.NewServer(p.Handler("fixture-session", []string{"gpt-5.1-codex"}, []string{"only-codex"}, "", false))
		defer downstream.Close()
		provider := ai.NewOpenAIResponsesProvider(ai.OpenAIResponsesConfig{BaseURL: downstream.URL + "/model/v1", APIKey: "fixture", Model: "gpt-5.1-codex"})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for i := 0; i < 2; i++ {
			stream, err := provider.Stream(ctx, ai.NormalizeContext(ai.Context{Messages: []ai.Message{ai.UserMessage{Content: ai.UserContentBlocks{ai.TextContent{Text: "hi"}}}}}), ai.StreamOptions{MaxRetries: new(1), MaxRetryDelayMs: new(1000)})
			if err != nil {
				t.Fatal(err)
			}
			for range stream.Events(ctx) {
			}
			if result := stream.Result(); result.StopReason != ai.StopReasonStop {
				t.Fatalf("request %d failed: %+v", i, result)
			}
		}
		first, second := <-attemptTimes, <-attemptTimes
		if calls.Load() != 3 || second.Sub(first) < 50*time.Millisecond {
			t.Fatalf("calls=%d retry backoff=%s", calls.Load(), second.Sub(first))
		}
	})

	for _, cooldown := range []int{-1, 0} {
		for _, status := range []int{403, 429} {
			t.Run(fmt.Sprintf("in-flight 503 after %d cooldown setting %d", status, cooldown), func(t *testing.T) {
				coreauth.SetTransientErrorCooldownSeconds(cooldown)
				defer coreauth.SetTransientErrorCooldownSeconds(-1)
				started, release := make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if calls.Add(1) != 1 {
						w.WriteHeader(200)
						return
					}
					close(started)
					<-release
					w.WriteHeader(503)
					_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"late failure"}}`)
				}))
				defer upstream.Close()
				manager := newCooldownTestManager(t, cfg, upstream.URL)
				done := make(chan error, 1)
				go func() {
					_, err := manager.Execute(context.Background(), []string{"codex"}, executor.Request{Model: "gpt-5.1-codex"}, executor.Options{})
					done <- err
				}()
				<-started
				manager.MarkResult(context.Background(), coreauth.Result{AuthID: "only-codex", Model: "gpt-5.1-codex", Error: &coreauth.Error{HTTPStatus: status, Message: "legitimate auth/quota failure"}})
				before, _ := manager.GetByID("only-codex")
				beforeState := before.ModelStates["gpt-5.1-codex"]
				t.Logf("before 503: next_retry_after=%s remaining=%s quota=%+v unavailable=%v", beforeState.NextRetryAfter, time.Until(beforeState.NextRetryAfter), beforeState.Quota, beforeState.Unavailable)
				close(release)
				if err := <-done; err == nil {
					t.Fatal("late upstream failure disappeared")
				}
				after, _ := manager.GetByID("only-codex")
				afterState := after.ModelStates["gpt-5.1-codex"]
				t.Logf("after 503: next_retry_after=%s remaining=%s quota=%+v unavailable=%v", afterState.NextRetryAfter, time.Until(afterState.NextRetryAfter), afterState.Quota, afterState.Unavailable)
				_, err := manager.Execute(context.Background(), []string{"codex"}, executor.Request{Model: "gpt-5.1-codex"}, executor.Options{})
				if status == 403 && cooldown == -1 {
					// Accepted pinned-library limitation, not a desired guarantee:
					// disabled transient cooling clears a concurrent model 403 gate.
					// The default-config control below preserves its ~30 minute gate.
					if err != nil || calls.Load() != 2 || !afterState.NextRetryAfter.IsZero() {
						t.Fatalf("403 characterization changed: %v calls=%d state=%+v", err, calls.Load(), afterState)
					}
				} else if err == nil || calls.Load() != 1 {
					t.Fatalf("late 503 cleared %d cooldown: %v calls=%d", status, err, calls.Load())
				}
			})
		}
	}
}

func newCooldownTestManager(t *testing.T, cfg *config.Config, upstream string) *coreauth.Manager {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	// A zero manager retry budget isolates requests and leaves short retries to
	// PiG. The service installs these same config setters in applyRetryConfig.
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(cooldownHTTPExecutor{url: upstream})
	cliproxy.GlobalModelRegistry().RegisterClient("only-codex", "codex", []*cliproxy.ModelInfo{{ID: "gpt-5.1-codex"}})
	t.Cleanup(func() { cliproxy.GlobalModelRegistry().UnregisterClient("only-codex") })
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: "only-codex", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth}}); err != nil {
		t.Fatal(err)
	}
	return manager
}
