package auth_test

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	clipexec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Keep HTTP in memory so the real retry timers use synctest's clock.
type zaiUpstream http.HandlerFunc

func (h zaiUpstream) RoundTripperFor(*auth.Auth) http.RoundTripper { return h }

func (h zaiUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	defer r.Body.Close()
	response := httptest.NewRecorder()
	h(response, r)
	return response.Result(), nil
}

func TestDynaZAIRequestRetry(t *testing.T) {
	const id, provider, model = "dyna-zai-retry", "openai-compatible-zai", "glm-5.4"
	const rateLimit = `{"code":"1302","message":"Rate limit reached for requests"}`
	const partial = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"
	const done = "data: [DONE]\n\n"
	usage.StartDefault(t.Context())
	for _, tc := range []struct {
		name          string
		stream        bool
		midstream     bool
		admissionWait time.Duration
	}{
		{name: "execute waits and succeeds"},
		{name: "stream bootstrap waits and succeeds", stream: true},
		{name: "midstream 429 is never replayed", stream: true, midstream: true},
		{name: "admission and retry share a deadline", admissionWait: 280 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var starts []time.Time
				manager := auth.NewManager(nil, nil, nil)
				manager.SetRetryConfig(0, 300*time.Second, 0)
				manager.RegisterExecutor(executor.NewOpenAICompatExecutor(provider, &config.Config{}))
				manager.SetRoundTripperProvider(zaiUpstream(func(w http.ResponseWriter, r *http.Request) {
					starts = append(starts, time.Now())
					if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture" {
						t.Errorf("unexpected upstream request: %s", r.URL.Path)
					}
					switch {
					case tc.midstream:
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, partial+`data: {"error":{"status":429,"code":"1302","message":"Rate limit reached for requests"}}`+"\n\n")
					case len(starts) <= 2:
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusTooManyRequests)
						_, _ = io.WriteString(w, rateLimit)
					case tc.stream:
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, partial+done)
					default:
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`)
					}
				}))
				ctx := auth.WithDynaEligibleAuth(t.Context(), []string{base64.RawURLEncoding.EncodeToString([]byte(id))})
				if _, err := manager.Register(ctx, &auth.Auth{
					ID: id, Provider: provider, Status: auth.StatusActive,
					Attributes: map[string]string{"api_key": "fixture", "base_url": "https://upstream.invalid/v1"},
					Metadata:   map[string]any{"request_retry": 3},
				}); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
				defer registry.GetGlobalRegistry().UnregisterClient(id)
				if tc.admissionWait != 0 {
					manager.MarkResult(ctx, auth.Result{AuthID: id, Model: model, RetryAfter: &tc.admissionWait, Error: &auth.Error{HTTPStatus: 429, Message: rateLimit}})
				}
				req := clipexec.Request{Model: model, Payload: []byte(`{"model":"glm-5.4","messages":[{"role":"user","content":"hi"}]}`)}
				opts := clipexec.Options{SourceFormat: translator.FormatOpenAI, Stream: tc.stream}
				start := time.Now()
				var payload strings.Builder
				var err error
				if tc.stream {
					stream, streamErr := manager.ExecuteStream(ctx, []string{provider}, req, opts)
					if streamErr != nil {
						t.Fatalf("stream failed at bootstrap: %v", streamErr)
					}
					for chunk := range stream.Chunks {
						payload.Write(chunk.Payload)
						if chunk.Err != nil {
							err = chunk.Err
						}
					}
				} else {
					resp, execErr := manager.Execute(ctx, []string{provider}, req, opts)
					payload.Write(resp.Payload)
					err = execErr
				}
				if tc.midstream || tc.admissionWait != 0 {
					var status interface{ StatusCode() int }
					if !errors.As(err, &status) || status.StatusCode() != 429 || !strings.Contains(err.Error(), "1302") || len(starts) != 1 {
						t.Fatalf("want original 429 and no replay; got err=%v, starts=%v", err, starts)
					}
					if tc.midstream && (!strings.Contains(payload.String(), "hello") || time.Since(start) > 0) {
						t.Fatalf("midstream response = %q, elapsed = %s", payload.String(), time.Since(start))
					}
					if elapsed := time.Since(start); tc.admissionWait != 0 && (elapsed < tc.admissionWait || elapsed > 330*time.Second) {
						t.Fatalf("admission and retry took %s", elapsed)
					}
					return
				}
				if err != nil || !strings.Contains(payload.String(), "hello") || len(starts) != 3 {
					t.Fatalf("want success on third attempt; got payload=%q, err=%v, starts=%v", payload.String(), err, starts)
				}
				for i := 1; i < len(starts); i++ {
					if delay := starts[i].Sub(starts[i-1]); delay < time.Minute || delay > 62*time.Second {
						t.Fatalf("delay before attempt %d: %s, want 60s plus up to 2s of jitter", i+1, delay)
					}
				}
			})
		})
	}
}
