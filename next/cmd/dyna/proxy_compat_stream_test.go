package main

import (
	"github.com/resident-labs/dyna/engine/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type compatStreamTestTransport struct{ discoveryFunc }

func (p compatStreamTestTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper {
	return p.discoveryFunc
}

func TestActiveDynaCompatKeysStreamResponses(t *testing.T) {
	for _, tc := range []struct{ provider, name, host, model, prefix string }{
		{"openai-compatible-zai", "zai", "api.z.ai", "glm-5.4", ""},
		{"openai-compatible-opencode-go", "opencode-go", "opencode.ai", "kimi-k3", ""},
		{"openai-compatible-deepseek", "deepseek", "api.deepseek.com", "deepseek-chat", ""},
		{"openai-compatible-custom-fixture", "custom-fixture", "fixture.invalid", "local-model", "custom-fixture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, service, router := importTestRoutes(t)
			baseURL := "https://" + tc.host + "/v1"
			if tc.prefix == "" {
				preset, ok := model.PresetForProxy(tc.provider)
				if !ok {
					t.Fatalf("no Dyna preset for %s", tc.provider)
				}
				baseURL = preset.URL
			}
			id := "dyna-key-fixture-" + tc.name
			auth := &coreauth.Auth{ID: id, Provider: tc.provider, Prefix: tc.prefix, Status: coreauth.StatusActive, Attributes: map[string]string{
				"api_key": "fixture", "auth_kind": "apikey", "base_url": baseURL, "compat_name": tc.name, "provider_key": tc.provider,
				cliproxy.DynaModelsAttribute: tc.model,
			}, Metadata: map[string]any{"type": "openai-compatibility"}}
			if _, err := manager.Register(coreauth.WithSkipPersist(t.Context()), auth); err != nil {
				t.Fatal(err)
			}
			publicModel := tc.model
			if tc.prefix != "" {
				publicModel = tc.prefix + "/" + publicModel
			}
			if err := service.ActivateDynaCredential(t.Context(), id, publicModel); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cliproxy.GlobalModelRegistry().UnregisterClient(id) })

			seen := make(chan *http.Request, 2)
			var attempts atomic.Int32
			manager.SetRoundTripperProvider(compatStreamTestTransport{func(r *http.Request) (*http.Response, error) {
				select {
				case seen <- r:
				default:
					t.Error("active request replayed")
				}
				w := httptest.NewRecorder()
				w.Header().Set("Content-Type", "text/event-stream")
				if attempts.Add(1) == 1 {
					// A partial error is not an upstream 429. This attempt must not
					// replay, and the same key must still serve the next request.
					_, _ = w.WriteString("data: {\"error\":{\"status\":429,\"message\":")
					return w.Result(), nil
				}
				_, _ = w.WriteString(`data: {"id":"ct_fixture","object":"chat.completion.chunk","model":"` + tc.model + `","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}` + "\n\n")
				_, _ = w.WriteString(`data: {"id":"ct_fixture","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n")
				_, _ = w.WriteString(`data: {"id":"ct_fixture","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}` + "\n\n")
				return w.Result(), nil // A complete translated terminal without [DONE].
			}})
			h := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
			router.POST("/v1/responses", h.Responses)
			serve := func() *httptest.ResponseRecorder {
				request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+publicModel+`","input":"hello","stream":true}`))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("X-Claude-Code-Session-Id", "canonical-fixture")
				request.Header.Set("X-Opencode-Session", "lower-priority-fixture")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				return response
			}
			if response := serve(); response.Code != http.StatusBadGateway || attempts.Load() != 1 {
				t.Fatalf("accepted truncation: status %d, requests %d: %s", response.Code, attempts.Load(), response.Body.String())
			}
			response := serve()
			if attempts.Load() != 2 {
				t.Fatalf("accepted truncation replayed or cooled the active key: %d requests", attempts.Load())
			}
			body := response.Body.String()
			if response.Code != http.StatusOK || !strings.Contains(body, `"type":"response.completed"`) || !strings.Contains(body, `"total_tokens":15`) || !strings.Contains(body, `"delta":"hello"`) {
				t.Fatalf("active Dyna stream returned %d: %s", response.Code, body)
			}
			for range 2 {
				select {
				case r := <-seen:
					if r.URL.Host != tc.host || !strings.HasSuffix(r.URL.Path, "/chat/completions") || r.Header.Get("Authorization") != "Bearer fixture" {
						t.Fatalf("request missed active provider: %s", r.URL)
					}
					wantSession := ""
					if tc.name == "opencode-go" {
						wantSession = "canonical-fixture"
					}
					if got := r.Header.Get("X-Opencode-Session"); got != wantSession {
						t.Fatalf("forwarded session = %q, want %q", got, wantSession)
					}
				default:
					t.Fatal("provider was not reached")
				}
			}
		})
	}
}
