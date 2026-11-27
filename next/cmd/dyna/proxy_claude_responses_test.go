package main

import (
	"io"
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

type claudeFixtureCutBody struct{ io.ReadCloser }

func (b claudeFixtureCutBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func TestActiveDynaClaudeResponses(t *testing.T) {
	for _, tc := range []struct {
		name, first, stream, failure string
		status                       int
		cut                          bool
	}{
		{"partial", `data: {"type":"error","error":{"type":"overloaded_error","message":`, "true", "", 502, false},
		{"missing-stop", "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n", "false", "", 502, false},
		{"missing-delta", "data: {\"type\":\"message_stop\"}\n\n", "false", "", 502, false},
		{"provider-error", "data: {\"type\":\"error\",\ndata: \"error\":{\"type\":\"overloaded_error\",\"message\":\"provider fixture\"}}", "true", `"code":"overloaded_error"`, 200, true},
		{"buffered-provider-error", "data: {\"type\":\"error\",\ndata: \"error\":{\"type\":\"overloaded_error\",\"message\":\"provider fixture\",\"private_field\":\"do not project\"}}", "false", "claude executor: upstream returned error event: provider fixture", 502, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, service, router := importTestRoutes(t)
			const model = "claude-sonnet-5"
			id := "dyna-claude-fixture-" + tc.name
			auth := &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "fixture", "base_url": "https://api.anthropic.com"}}
			if _, err := manager.Register(coreauth.WithSkipPersist(t.Context()), auth); err != nil {
				t.Fatal(err)
			}
			if err := service.ActivateDynaCredential(t.Context(), id, model); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cliproxy.GlobalModelRegistry().UnregisterClient(id) })
			var attempts atomic.Int32
			manager.SetRoundTripperProvider(compatStreamTestTransport{func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.anthropic.com" || r.URL.Path != "/v1/messages" || r.Header.Get("X-Api-Key") != "fixture" {
					t.Errorf("wrong Claude route: %s", r.URL)
				}
				request, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				if !strings.Contains(string(request), `"tool_choice":{"type":"none"}`) || !strings.Contains(string(request), `"name":"lookup"`) {
					t.Errorf("tool choice not translated: %s", request)
				}
				w := httptest.NewRecorder()
				w.Header().Set("Content-Type", "text/event-stream")
				start := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"model\":\"" + model + "\",\"usage\":{\"input_tokens\":12}}}\n\n"
				if attempts.Add(1) == 1 {
					if tc.name != "partial" {
						_, _ = w.WriteString(start)
					}
					_, _ = w.WriteString(tc.first)
				} else {
					_, _ = w.WriteString(start + "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
				}
				resp := w.Result()
				if tc.cut && attempts.Load() == 1 {
					resp.Body = claudeFixtureCutBody{resp.Body}
				}
				return resp, nil
			}})
			h := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
			router.POST("/v1/responses", h.Responses)
			serve := func(stream string) *httptest.ResponseRecorder {
				r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+model+`","input":"hello","stream":`+stream+`,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":"none"}`))
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				return w
			}
			w := serve(tc.stream)
			body := w.Body.String()
			if w.Code != tc.status || attempts.Load() != 1 || strings.Contains(body, `"type":"response.completed"`) || !strings.Contains(body, tc.failure) {
				t.Fatalf("first request: HTTP %d, attempts %d: %s", w.Code, attempts.Load(), body)
			}
			if tc.name == "buffered-provider-error" {
				if strings.Contains(body, "private_field") || strings.Contains(body, "overloaded_error") {
					t.Fatalf("buffered failure leaked upstream fields: %s", body)
				}
				return
			}
			if tc.failure != "" {
				if strings.Count(body, `"type":"response.failed"`) != 1 || !strings.Contains(body, `"input_tokens":12`) {
					t.Fatalf("failure lost terminal or usage: %s", body)
				}
				return // Genuine provider failures retain existing cooldown semantics.
			}
			w = serve("true")
			if w.Code != 200 || attempts.Load() != 2 || !strings.Contains(w.Body.String(), `"total_tokens":15`) {
				t.Fatalf("second request: HTTP %d, attempts %d: %s", w.Code, attempts.Load(), w.Body.String())
			}
		})
	}
}
