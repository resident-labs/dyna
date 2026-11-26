package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const truncatedStreamChatModel = "truncated-stream-chat-model"

type mockStreamExecutor struct {
	chunks      []string
	trailingErr error
}

func (*mockStreamExecutor) Identifier() string { return "mock-stream-executor" }

func (*mockStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *mockStreamExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	total := len(e.chunks)
	if e.trailingErr != nil {
		total++
	}
	chunks := make(chan coreexecutor.StreamChunk, total)
	for _, c := range e.chunks {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(c)}
	}
	if e.trailingErr != nil {
		chunks <- coreexecutor.StreamChunk{Err: e.trailingErr}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*mockStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*mockStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*mockStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func runMockStreamTest(t *testing.T, chunks []string, authID string) string {
	return runMockStreamTestWithError(t, chunks, nil, authID)
}

func runMockStreamTestWithError(t *testing.T, chunks []string, trailingErr error, authID string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)

	executor := &mockStreamExecutor{chunks: chunks, trailingErr: trailingErr}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: truncatedStreamChatModel}})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/chat/completions", h.ChatCompletions)

	body := `{"model":"truncated-stream-chat-model","messages":[{"role":"user","content":"hi"}],"stream":true}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder.Body.String()
}

// Issue #6371: A stream that emits multiple chunks and then cleanly closes without
// any chunk carrying finish_reason is truncated. It must NOT be answered with [DONE],
// but must surface an error so clients can detect truncation and retry.
func TestChatCompletionsStreamWithoutFinishReasonIsReportedAsError(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"do_thing","arguments":"{\"a\":1"}}]}}]}`,
	}
	out := runMockStreamTest(t, chunks, "truncated-stream-auth-multi-chunk-missing")
	if strings.Contains(out, "data: [DONE]") {
		t.Errorf("truncated stream was terminated with [DONE] as if it had succeeded: %q", out)
	}
	if !strings.Contains(out, "finish_reason") {
		t.Errorf("truncated stream did not surface an error naming the missing terminator: %q", out)
	}
	if !strings.Contains(out, `"error":{"message":"upstream stream closed before any chunk carried finish_reason"`) {
		t.Errorf("truncated stream did not format structured error payload: %q", out)
	}
}

// A translated terminal error chunk ends the stream on its own: the handler
// must not add a second "closed before finish_reason" error after it.
func TestChatCompletionsStreamTerminalErrorChunkIsTheOnlyError(t *testing.T) {
	for _, tc := range []struct {
		name        string
		chunks      []string
		trailingErr error
	}{
		{name: "after content", chunks: []string{
			`{"choices":[{"index":0,"delta":{"role":"assistant","content":"partial"},"finish_reason":null}]}`,
			`{"error":{"message":"context window filled","type":"invalid_request_error","code":"context_length_exceeded"}}`,
		}},
		{name: "first chunk", chunks: []string{
			`{"error":{"message":"context window filled","type":"invalid_request_error","code":"context_length_exceeded"}}`,
		}},
		{name: "eof before message_stop after error", chunks: []string{
			`{"choices":[{"index":0,"delta":{"role":"assistant","content":"partial"},"finish_reason":null}]}`,
			`{"error":{"message":"context window filled","type":"invalid_request_error","code":"context_length_exceeded"}}`,
		}, trailingErr: errors.New("upstream stream closed before message_stop")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runMockStreamTestWithError(t, tc.chunks, tc.trailingErr, "terminal-error-"+strings.ReplaceAll(tc.name, " ", "-"))
			if got := strings.Count(out, `"error":`); got != 1 {
				t.Fatalf("error objects = %d, want 1: %q", got, out)
			}
			if !strings.Contains(out, `"code":"context_length_exceeded"`) {
				t.Fatalf("terminal error missing: %q", out)
			}
			if strings.Contains(out, "upstream stream closed before") || strings.Contains(out, "data: [DONE]") {
				t.Fatalf("synthetic truncation error or [DONE] added after terminal error: %q", out)
			}
			if strings.Contains(out, `"finish_reason":"`) || strings.Contains(out, `"usage"`) {
				t.Fatalf("success finish or usage emitted: %q", out)
			}
		})
	}
}

// Issue #6371: A stream with explicit null or empty finish_reason must still be detected as truncated.
func TestChatCompletionsStreamExplicitNullOrEmptyFinishReasonIsReportedAsError(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"part1"},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"content":"part2"},"finish_reason":""}]}`,
	}
	out := runMockStreamTest(t, chunks, "truncated-stream-auth-null-finish")
	if strings.Contains(out, "data: [DONE]") {
		t.Errorf("stream with null/empty finish_reason was terminated with [DONE]: %q", out)
	}
	if !strings.Contains(out, "finish_reason") {
		t.Errorf("stream with null/empty finish_reason did not surface error: %q", out)
	}
}

// Issue #6371: A stream that emits only 1 chunk without finish_reason and closes must also
// be flagged as truncated, not [DONE].
func TestChatCompletionsStreamSingleChunkWithoutFinishReasonIsReportedAsError(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
	}
	out := runMockStreamTest(t, chunks, "truncated-stream-auth-single-chunk-missing")
	if strings.Contains(out, "data: [DONE]") {
		t.Errorf("single-chunk truncated stream was terminated with [DONE] as if it had succeeded: %q", out)
	}
	if !strings.Contains(out, "finish_reason") {
		t.Errorf("single-chunk truncated stream did not surface an error naming the missing terminator: %q", out)
	}
}

// Control: A multi-chunk stream that does carry finish_reason must complete with [DONE].
func TestChatCompletionsStreamWithFinishReasonStillCompletes(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"do_thing","arguments":"{\"a\":1}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	out := runMockStreamTest(t, chunks, "truncated-stream-auth-multi-chunk-present")
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("complete stream was not terminated with [DONE]: %q", out)
	}
	if strings.Contains(out, "upstream stream closed before") {
		t.Errorf("complete stream was wrongly flagged as truncated: %q", out)
	}
}

// Control: A single-chunk stream that carries finish_reason must complete with [DONE].
func TestChatCompletionsStreamSingleChunkWithFinishReasonStillCompletes(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"content":"instant"},"finish_reason":"stop"}]}`,
	}
	out := runMockStreamTest(t, chunks, "truncated-stream-auth-single-chunk-present")
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("single-chunk complete stream was not terminated with [DONE]: %q", out)
	}
	if strings.Contains(out, "upstream stream closed before") {
		t.Errorf("single-chunk complete stream was wrongly flagged as truncated: %q", out)
	}
}

// Issue #6371: Legacy /v1/completions stream that ends without finish_reason must also be flagged as error.
func TestCompletionsStreamWithoutFinishReasonIsReportedAsError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"trunc"}}]}`,
	}
	executor := &mockStreamExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "truncated-legacy-auth-missing", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "legacy-model"}})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/completions", h.Completions)

	body := `{"model":"legacy-model","prompt":"hi","stream":true}`
	request := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	out := recorder.Body.String()

	if strings.Contains(out, "data: [DONE]") {
		t.Errorf("truncated legacy completions stream was terminated with [DONE]: %q", out)
	}
	if !strings.Contains(out, "finish_reason") {
		t.Errorf("truncated legacy completions stream did not surface error: %q", out)
	}
}

// Control: Legacy /v1/completions stream that carries finish_reason completes with [DONE].
func TestCompletionsStreamWithFinishReasonStillCompletes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
	}
	executor := &mockStreamExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "truncated-legacy-auth-present", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "legacy-model-ok"}})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/completions", h.Completions)

	body := `{"model":"legacy-model-ok","prompt":"hi","stream":true}`
	request := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	out := recorder.Body.String()

	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("complete legacy completions stream was not terminated with [DONE]: %q", out)
	}
	if strings.Contains(out, "upstream stream closed before") {
		t.Errorf("complete legacy completions stream was wrongly flagged as truncated: %q", out)
	}
}

// Control: A stream with usage chunk arriving after finish_reason chunk must complete with [DONE].
func TestChatCompletionsStreamUsageAfterFinishReasonStillCompletes(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
	}
	out := runMockStreamTest(t, chunks, "truncated-stream-auth-usage-after-finish")
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("stream with usage after finish_reason was not terminated with [DONE]: %q", out)
	}
	if strings.Contains(out, "upstream stream closed before") {
		t.Errorf("stream with usage after finish_reason was wrongly flagged as truncated: %q", out)
	}
}

// Control: Legacy /v1/completions single-chunk stream with finish_reason completes with [DONE].
func TestCompletionsStreamSingleChunkWithFinishReasonStillCompletes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := []string{
		`{"choices":[{"index":0,"delta":{"content":"single"},"finish_reason":"stop"}]}`,
	}
	executor := &mockStreamExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "truncated-legacy-auth-single-present", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "legacy-model-single-ok"}})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/completions", h.Completions)

	body := `{"model":"legacy-model-single-ok","prompt":"hi","stream":true}`
	request := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	out := recorder.Body.String()

	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("single chunk legacy completions stream was not terminated with [DONE]: %q", out)
	}
	if strings.Contains(out, "upstream stream closed before") {
		t.Errorf("single chunk legacy completions stream was wrongly flagged as truncated: %q", out)
	}
}

// Test the real executor and auth manager, not just the handler's own bootstrap
// retry counter: the manager can otherwise pick a second credential before any
// framed output reaches the client, even with handler bootstrap retries disabled.
func TestKimiChatAcceptedStreamLocalFailureDoesNotReplayOrCool(t *testing.T) {
	const (
		model         = "kimi-k3.1-pro"
		line          = `data: {"id":"chat_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}` + "\n"
		upstreamError = `data: {"error":{"message":"provider overloaded","status":429}}` + "\n"
	)
	for _, tc := range []struct {
		name, initial, errorText           string
		shortBody, oversized, wantCooldown bool
		status                             int
	}{
		{name: "clean_eof", initial: line, errorText: "closed before [DONE]", status: http.StatusOK},
		{name: "short_before_delimiter", initial: line, shortBody: true, errorText: "interrupted before [DONE]", status: http.StatusBadGateway},
		{name: "short_after_delimiter", initial: line + "\n", shortBody: true, errorText: "interrupted before [DONE]", status: http.StatusOK},
		{name: "cap_before_first_payload", oversized: true, errorText: "frame exceeds 50 MiB", status: http.StatusBadGateway},
		{name: "cap_after_first_payload", initial: line + "\n", oversized: true, errorText: "frame exceeds 50 MiB", status: http.StatusOK},
		{name: "intact_provider_error_before_transport_cut", initial: line + "\n" + upstreamError, shortBody: true, errorText: "provider overloaded", status: http.StatusOK, wantCooldown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
					t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
				}
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.shortBody {
					w.Header().Set("Content-Length", fmt.Sprint(len(tc.initial)+12))
				}
				_, _ = io.WriteString(w, tc.initial)
				if tc.oversized {
					// A valid JSON event larger than 50 MiB in total, with each data line below the scanner cap.
					padding := strings.Repeat("a", 26*1024*1024)
					_, _ = io.WriteString(w, `data: {"choices":[],"first":"`)
					_, _ = io.WriteString(w, padding)
					_, _ = io.WriteString(w, "\",\ndata: \"second\":\"")
					_, _ = io.WriteString(w, padding)
					_, _ = io.WriteString(w, "\"}\n\n")
				}
				w.(http.Flusher).Flush()
			}))
			t.Cleanup(server.Close)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor.NewKimiExecutor(&sdkconfig.Config{}))
			authIDs := make([]string, 0, 2)
			for i := range 2 {
				id := fmt.Sprintf("%s/%d", t.Name(), i)
				auth := &coreauth.Auth{ID: id, Provider: "kimi", Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": server.URL, "api_key": fmt.Sprint(i)}}
				if _, err := manager.Register(ctx, auth); err != nil {
					t.Fatal(err)
				}
				authIDs = append(authIDs, id)
				registry.GetGlobalRegistry().RegisterClient(id, "kimi", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			const path = "/v1/chat/completions"
			router := gin.New()
			router.POST(path, NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)).ChatCompletions)
			body := `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`
			out := httptest.NewRecorder()
			router.ServeHTTP(out, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx))
			if calls.Load() != 1 || out.Code != tc.status || !strings.Contains(out.Body.String(), tc.errorText) || strings.Contains(out.Body.String(), "data: [DONE]") {
				t.Fatalf("POSTs=%d status=%d body=%q; want one POST and HTTP %d with %q", calls.Load(), out.Code, out.Body.String(), tc.status, tc.errorText)
			}
			cooled := 0
			for _, id := range authIDs {
				auth, _ := manager.GetByID(id)
				state := auth.ModelStates[model]
				if !auth.NextRetryAfter.IsZero() || (state != nil && !state.NextRetryAfter.IsZero()) {
					cooled++
					if tc.wantCooldown && (auth.LastError == nil || auth.LastError.HTTPStatus != http.StatusTooManyRequests) {
						t.Errorf("upstream 429 was not preserved: %+v", auth.LastError)
					}
				}
			}
			if (tc.wantCooldown && cooled != 1) || (!tc.wantCooldown && cooled != 0) {
				t.Errorf("%d credentials cooled; want a cooldown only for the genuine upstream error", cooled)
			}
		})
	}
}
