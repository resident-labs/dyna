package model

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type cooldownHTTPError struct {
	base       *coreauth.Error
	retryAfter *time.Duration
}

func (e cooldownHTTPError) Error() string              { return e.base.Error() }
func (e cooldownHTTPError) Unwrap() error              { return e.base }
func (e cooldownHTTPError) RetryAfter() *time.Duration { return e.retryAfter }

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
		var retryAfter *time.Duration
		if hint := resp.Header.Get("Retry-After"); hint != "" {
			seconds, err := strconv.Atoi(hint)
			if err != nil {
				return executor.Response{}, err
			}
			delay := time.Duration(seconds) * time.Second
			retryAfter = &delay
		}
		return executor.Response{}, cooldownHTTPError{base: &coreauth.Error{Code: code, Message: string(body), HTTPStatus: resp.StatusCode}, retryAfter: retryAfter}
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
		hint           string
	}{
		{"server_error", 500, false, false, ""},
		{"bad_gateway", 502, false, false, ""},
		{"unavailable", 503, false, false, ""},
		{"timeout", 504, false, false, ""},
		{"other_5xx", 599, false, false, ""},
		{"unauthorized", 401, false, true, ""},
		{"forbidden", 403, false, true, ""},
		{"quota", 429, false, true, ""},
		{"capacity_without_retry_hint", 429, true, false, ""},
		{"quota_with_positive_hint", 429, false, true, "2"},
		{"capacity_with_positive_hint", 429, true, false, "2"},
		{"unavailable_with_positive_hint", 503, false, false, "2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					if tc.capacity {
						w.Header().Set("X-Fixture-Capacity", "true")
					}
					w.Header().Set("Retry-After", tc.hint)
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
		attemptTimes := make(chan time.Time, 6)
		errorHints := make(chan string, 2)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := calls.Add(1)
			attemptTimes <- time.Now()
			if n == 1 || n == 4 {
				if n == 4 {
					w.Header().Set("Retry-After", "1")
				}
				w.WriteHeader(503)
				_, _ = io.WriteString(w, `{"error":{"type":"server_error","code":"server_error","message":"upstream temporarily unavailable"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"fixture\",\"status\":\"completed\",\"output\":[]}}\n\n")
		}))
		defer upstream.Close()
		manager := newCooldownTestManager(t, cfg, upstream.URL)
		// A thin HTTP adapter calls the real CLIProxyAPI execution/error handlers.
		// Dyna's real route and Unix socket lead into the real auth manager;
		// only its executor and the upstream are fixtures. PiG owns retries.
		base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
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
			resp, _, problem := base.ExecuteWithAuthManager(r.Context(), "openai-response", "gpt-5.1-codex", payload, "")
			if problem != nil {
				c, _ := gin.CreateTestContext(w)
				c.Request = r
				base.WriteErrorResponse(c, problem)
				errorHints <- c.Writer.Header().Get("Retry-After")
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write(resp)
		})
		server := &http.Server{Handler: adapter}
		go func() { _ = server.Serve(p.Listener) }()
		defer server.Close()
		downstream := httptest.NewServer(p.Handler("fixture-session", []string{"gpt-5.1-codex"}, []string{"only-codex"}, "", false))
		defer downstream.Close()
		provider := ai.NewOpenAIResponsesProvider(ai.OpenAIResponsesConfig{BaseURL: downstream.URL + "/model/v1", APIKey: "fixture", Model: "gpt-5.1-codex"})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for i := 0; i < 4; i++ {
			stream, err := provider.Stream(ctx, ai.NormalizeContext(ai.Context{Messages: []ai.Message{ai.UserMessage{Content: ai.UserContentBlocks{ai.TextContent{Text: "hi"}}}}}), ai.StreamOptions{MaxRetries: new(1), MaxRetryDelayMs: new(2000)})
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
		<-attemptTimes // later independent successful request, with no cooldown outage
		hintedFirst, hintedSecond := <-attemptTimes, <-attemptTimes
		<-attemptTimes // another independent success after the hinted retry
		noHint, hint := <-errorHints, <-errorHints
		t.Logf("generated config retry rounds=%d; credential transient cooldown=%d; downstream Retry-After values=%q,%q; PiG retry delays=%s,%s", cfg.RequestRetry, cfg.TransientErrorCooldownSeconds, noHint, hint, second.Sub(first), hintedSecond.Sub(hintedFirst))
		if calls.Load() != 6 || second.Sub(first) < 50*time.Millisecond || hint != "1" || hintedSecond.Sub(hintedFirst) < 900*time.Millisecond {
			t.Fatalf("calls=%d backoff=%s hinted backoff=%s header=%q", calls.Load(), second.Sub(first), hintedSecond.Sub(hintedFirst), hint)
		}
	})

	t.Run("credential-level 401 stays cooled", func(t *testing.T) {
		var calls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(200) }))
		defer upstream.Close()
		manager := newCooldownTestManager(t, cfg, upstream.URL)
		manager.MarkResult(context.Background(), coreauth.Result{AuthID: "only-codex", Error: &coreauth.Error{HTTPStatus: 401, Message: "invalid authentication"}})
		auth, _ := manager.GetByID("only-codex")
		if !auth.NextRetryAfter.After(time.Now()) {
			t.Fatalf("credential 401 has no cooldown: %+v", auth)
		}
		_, err := manager.Execute(context.Background(), []string{"codex"}, executor.Request{Model: "gpt-5.1-codex"}, executor.Options{})
		if err == nil || calls.Load() != 0 {
			t.Fatalf("credential 401 was selectable: %v calls=%d", err, calls.Load())
		}
	})

	for _, cooldown := range []int{-1, 0} {
		for _, tc := range []struct {
			name   string
			status int
			model  bool
		}{
			{"model 401", 401, true}, {"credential 401", 401, false},
			{"model 403", 403, true},
			{"model quota 429", 429, true},
			{"insufficient balance 402", 402, true},
		} {
			t.Run(fmt.Sprintf("in-flight 503 after %s cooldown setting %d", tc.name, cooldown), func(t *testing.T) {
				coreauth.SetTransientErrorCooldownSeconds(cooldown)
				defer coreauth.SetTransientErrorCooldownSeconds(-1)
				started, release := make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				message := "legitimate auth/quota failure"
				if tc.status == 402 {
					message = "insufficient balance"
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if calls.Add(1) != 1 {
						// A persistent auth failure is returned by the next real request,
						// rather than manufactured by another MarkResult call.
						w.WriteHeader(tc.status)
						_, _ = fmt.Fprintf(w, `{"error":{"message":%q}}`, message)
						return
					}
					close(started)
					<-release
					w.WriteHeader(503)
					_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"late failure"}}`)
				}))
				defer upstream.Close()
				manager := newCooldownTestManager(t, cfg, upstream.URL)
				snapshot := func(label string) time.Time {
					t.Helper()
					a, _ := manager.GetByID("only-codex")
					deadline, unavailable, quota := a.NextRetryAfter, a.Unavailable, a.Quota
					if tc.model {
						state := a.ModelStates["gpt-5.1-codex"]
						if state == nil {
							t.Fatal("missing model cooldown state")
						}
						deadline, unavailable, quota = state.NextRetryAfter, state.Unavailable, state.Quota
					}
					t.Logf("%s: next_retry_after=%s remaining=%s quota=%+v unavailable=%v", label, deadline, time.Until(deadline), quota, unavailable)
					return deadline
				}
				done := make(chan error, 1)
				go func() {
					_, err := manager.Execute(context.Background(), []string{"codex"}, executor.Request{Model: "gpt-5.1-codex"}, executor.Options{})
					done <- err
				}()
				select {
				case <-started:
				case err := <-done:
					t.Fatalf("request never reached fixture upstream: %v", err)
				}
				model := ""
				if tc.model {
					model = "gpt-5.1-codex"
				}
				manager.MarkResult(context.Background(), coreauth.Result{AuthID: "only-codex", Model: model, Error: &coreauth.Error{HTTPStatus: tc.status, Message: message}})
				if !snapshot("before 503").After(time.Now()) {
					t.Error("concurrent failure never established its cooldown")
				}
				close(release)
				if err := <-done; err == nil {
					t.Fatal("late upstream failure disappeared")
				}
				after := snapshot("after 503")
				_, err := manager.Execute(context.Background(), []string{"codex"}, executor.Request{Model: "gpt-5.1-codex"}, executor.Options{})
				if cooldown == -1 && (tc.status == 401 || tc.status == 403) {
					// Accepted pinned limitation: a late 5xx clears a concurrent 401/403
					// cooldown; the next persistent 401/403 sets it again.
					if err == nil || calls.Load() != 2 || !after.IsZero() {
						t.Fatalf("auth characterization changed: %v calls=%d deadline=%s", err, calls.Load(), after)
					}
					snapshot("after repeated auth failure")
					_, err = manager.Execute(context.Background(), []string{"codex"}, executor.Request{Model: "gpt-5.1-codex"}, executor.Options{})
					if err == nil || calls.Load() != 2 {
						t.Fatalf("next %d did not restore cooling: %v calls=%d", tc.status, err, calls.Load())
					}
				} else if err == nil || calls.Load() != 1 {
					t.Fatalf("late 503 cleared %s cooldown: %v calls=%d", tc.name, err, calls.Load())
				}
			})
		}
	}
}

func newCooldownTestManager(t *testing.T, cfg *config.Config, upstream string) *coreauth.Manager {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	// Match generated service configuration (currently zero additional rounds);
	// PiG owns the short per-request backoff.
	manager.SetRetryConfig(cfg.RequestRetry, time.Duration(cfg.MaxRetryInterval)*time.Second, cfg.MaxRetryCredentials)
	manager.RegisterExecutor(cooldownHTTPExecutor{url: upstream})
	cliproxy.GlobalModelRegistry().RegisterClient("only-codex", "codex", []*cliproxy.ModelInfo{{ID: "gpt-5.1-codex"}})
	t.Cleanup(func() { cliproxy.GlobalModelRegistry().UnregisterClient("only-codex") })
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: "only-codex", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth}}); err != nil {
		t.Fatal(err)
	}
	return manager
}
