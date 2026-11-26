package openai

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestXAIHTTPTerminal(t *testing.T) {
	const (
		model        = "grok-4.7"
		delta        = `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n"
		malformed    = `data: {"type":"response.completed","response":{"output":[` + "\n"
		limited      = "event: response.failed\ndata: {\"type\":\"response.failed\",\ndata: \"response\":{\"status\":\"failed\",\"error\":{\"code\":\"resource_exhausted\",\"message\":\"provider overloaded\"}}}\n"
		wrappedLimit = `data: {"type":"error","error":{},"body":{"error":{"code":"resource_exhausted","message":"wrapped provider limit"}}}`
		plainError   = "event: error\ndata: upstream gateway refusal"
		done         = `data: {"type":"response.done","response":{"id":"resp_ok","status":"completed","model":"grok-4.7","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ready"}]}],"usage":{"input_tokens":7,"output_tokens":3}}}` + "\n\n"
	)
	for _, tc := range []struct {
		name, body, want                               string
		stream, chat, short, limit, recover, truncated bool
		status, posts, cooled                          int
	}{
		{name: "buffered_empty", body: ": comment\n\n", want: "empty upstream stream", status: 502, posts: 1},
		{name: "buffered_cut", body: malformed, want: "closed before", short: true, status: 502, posts: 1},
		{name: "buffered_raw_error", body: `{"code":"invalid_argument","error":"invalid provider input"}`, want: "invalid provider input", status: 400, posts: 1},
		{name: "buffered_reverse_error_fields", body: "data: {\"code\":\"invalid_argument\",\"message\":\"invalid provider input\"}\nevent: error\n\n", want: "invalid provider input", status: 400, posts: 1},
		{name: "buffered_retry_429", body: limited, want: "ready", limit: true, recover: true, status: 200, posts: 2, cooled: 1},
		{name: "stream_empty", stream: true, body: ": comment\n\n", want: "empty upstream stream", status: 502, posts: 1},
		{name: "stream_cut_before_delimiter", stream: true, body: malformed, want: "closed before", short: true, status: 502, posts: 1},
		{name: "chat_cut_before_translated_output", stream: true, chat: true, body: `data: {"type":"response.created","response":{"id":"started"}}` + "\n", want: "closed before", short: true, status: 502, posts: 1},
		{name: "stream_cut_after_content", stream: true, body: delta + "\n" + malformed, want: "closed before", short: true, status: 200, posts: 1},
		{name: "stream_cut_complete_line_no_delimiter", stream: true, body: delta, want: "closed before", short: true, status: 200, posts: 1},
		{name: "stream_clean_eof_after_content", stream: true, body: delta + "\n", want: "closed before", status: 200, posts: 1},
		{name: "stream_retry_429", stream: true, body: limited, want: "response.done", short: true, limit: true, recover: true, status: 200, posts: 2, cooled: 1},
		{name: "stream_429_after_content", stream: true, body: delta + "\n" + limited, want: "provider overloaded", short: true, limit: true, status: 200, posts: 1, cooled: 1},
		{name: "stream_chat_done", stream: true, chat: true, body: delta + "\n" + done, want: `"finish_reason":"stop"`, status: 200, posts: 1},
		{name: "stream_chat_done_incomplete", stream: true, chat: true, body: delta + "\n" + strings.Replace(done, `"status":"completed"`, `"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}`, 1), want: `"finish_reason":"length"`, status: 200, posts: 1},
		{name: "stream_429_returns_retry_after", stream: true, body: limited, want: "provider overloaded", limit: true, status: 429, posts: 2, cooled: 2},
		{name: "buffered_done", body: done, want: "ready", status: 200, posts: 1},
		{name: "buffered_plain_error_read_cut", body: "event: error\ndata: upstream ga", want: "closed before", short: true, status: 502, posts: 1, truncated: true},
		{name: "stream_plain_error_read_cut", stream: true, body: "event: error\ndata: upstream ga", want: "closed before", short: true, status: 502, posts: 1, truncated: true},
		{name: "buffered_complete_plain_error", body: plainError + "\n", want: "upstream gateway refusal", short: true, status: 502, posts: 2, cooled: 2},
		{name: "stream_complete_plain_error", stream: true, body: delta + "\n" + plainError + "\n", want: "upstream gateway refusal", short: true, status: 200, posts: 1, cooled: 1},
		{name: "buffered_wrapped_429", body: wrappedLimit, want: "wrapped provider limit", short: true, limit: true, status: 429, posts: 2, cooled: 2},
		{name: "stream_wrapped_429_before_output", stream: true, body: wrappedLimit, want: "response.done", short: true, limit: true, recover: true, status: 200, posts: 2, cooled: 1},
		{name: "stream_wrapped_429_after_output", stream: true, body: delta + "\n" + wrappedLimit, want: "wrapped provider limit", short: true, limit: true, status: 200, posts: 1, cooled: 1},
		{name: "stream_wrapped_429_retry_after", stream: true, body: wrappedLimit, want: "wrapped provider limit", short: true, limit: true, status: 429, posts: 2, cooled: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/responses" {
					t.Errorf("unexpected upstream request %s %s", r.Method, r.URL.Path)
				}
				call := posts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.recover && call > 1 {
					_, _ = io.WriteString(w, done)
					return
				}
				if tc.limit {
					w.Header().Set("Retry-After", "73")
				}
				if tc.short {
					w.Header().Set("Content-Length", fmt.Sprint(len(tc.body)+100))
				}
				_, _ = io.WriteString(w, tc.body)
				w.(http.Flusher).Flush()
			}))
			defer upstream.Close()
			cfg := &sdkconfig.Config{}
			cfg.Streaming.BootstrapRetries = 2
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(executor.NewXAIAutoExecutor(cfg))
			records := make(chan usage.Record, 4)
			for i := range 2 {
				id := fmt.Sprintf("%s/%d", t.Name(), i)
				auth := &coreauth.Auth{ID: id, Provider: "xai", Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": upstream.URL, "api_key": fmt.Sprint(i)}}
				if _, err := manager.Register(t.Context(), auth); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "xai", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if tc.truncated {
					usage.RegisterNamedPlugin(id, xaiHTTPUsageCapture{id: id, records: records})
					t.Cleanup(func() { usage.RegisterNamedPlugin(id, xaiHTTPUsageCapture{}) })
				}
			}
			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			router := gin.New()
			path, payload := "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hi","stream":%t}`, model, tc.stream)
			if tc.chat {
				path, payload = "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":%t}`, model, tc.stream)
				router.POST(path, NewOpenAIAPIHandler(base).ChatCompletions)
			} else {
				router.POST(path, NewOpenAIResponsesAPIHandler(base).Responses)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload)).WithContext(t.Context()))
			if int(posts.Load()) != tc.posts || rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("POSTs=%d HTTP=%d body=%q; want POSTs=%d HTTP=%d containing %q", posts.Load(), rec.Code, rec.Body.String(), tc.posts, tc.status, tc.want)
			}
			if tc.status == http.StatusTooManyRequests && rec.Header().Get("Retry-After") != "73" {
				t.Errorf("Retry-After = %q, want 73", rec.Header().Get("Retry-After"))
			}
			if tc.limit && !tc.recover && strings.Contains(rec.Body.String(), "closed before") {
				t.Errorf("provider failure was treated as local truncation: %q", rec.Body.String())
			}
			cooled := 0
			for i := range 2 {
				auth, _ := manager.GetByID(fmt.Sprintf("%s/%d", t.Name(), i))
				if state := auth.ModelStates[model]; state != nil && !state.NextRetryAfter.IsZero() {
					cooled++
					if tc.limit && (state.LastError == nil || state.LastError.HTTPStatus != http.StatusTooManyRequests || time.Until(state.NextRetryAfter) < 65*time.Second) {
						t.Errorf("lost provider 429 or its retry delay: %+v", state)
					}
				} else if !auth.NextRetryAfter.IsZero() {
					t.Errorf("unexpected credential-level cooldown: %+v", auth)
				}
			}
			if cooled != tc.cooled {
				t.Errorf("cooled=%d, want %d", cooled, tc.cooled)
			}
			if tc.truncated {
				select {
				case record := <-records:
					if !record.Failed || record.Fail.StatusCode != http.StatusBadGateway || record.Fail.Code != usage.FailureStreamTruncatedCode {
						t.Errorf("cut plain error is not typed 502 truncation: %+v", record.Fail)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("missing usage failure record")
				}
			}
		})
	}
}

type xaiHTTPUsageCapture struct {
	id      string
	records chan usage.Record
}

func (c xaiHTTPUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if record.AuthID == c.id {
		c.records <- record
	}
}

// The upstream writes a full JSON data line but does not delimit or close its
// stream. Read the delimited frame off an actual downstream HTTP connection,
// then disconnect and assert that accounting reflects its terminal state.
func TestXAIHTTPDisconnectUsage(t *testing.T) {
	const model = "grok-4.7"
	for _, kind := range []string{"response.in_progress", "response.completed", "response.incomplete", "response.done"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "event: %s\ndata: {\"type\":%q,\"response\":{\"id\":\"resp_usage\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n", kind, kind)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-ctx.Done():
				}
			}))
			defer upstream.Close()
			cfg := &sdkconfig.Config{}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor.NewXAIAutoExecutor(cfg))
			id := t.Name()
			auth := &coreauth.Auth{ID: id, Provider: "xai", Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": upstream.URL, "api_key": "test"}}
			if _, err := manager.Register(ctx, auth); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(id, "xai", []*registry.ModelInfo{{ID: model}})
			defer registry.GetGlobalRegistry().UnregisterClient(id)
			records := make(chan usage.Record, 4)
			usage.RegisterNamedPlugin(id, xaiHTTPUsageCapture{id: id, records: records})
			defer usage.RegisterNamedPlugin(id, xaiHTTPUsageCapture{})
			router := gin.New()
			router.POST("/v1/responses", NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)).Responses)
			server := httptest.NewServer(router)
			defer server.Close()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"`+model+`","input":"hi","stream":true}`))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			reader := bufio.NewReader(resp.Body)
			var frame string
			for {
				line, errRead := reader.ReadString('\n')
				if errRead != nil {
					t.Fatalf("reading terminal/progress frame: %v (frame=%q)", errRead, frame)
				}
				if line == "\n" {
					break
				}
				frame += line
			}
			if resp.StatusCode != 200 || !strings.Contains(frame, fmt.Sprintf(`"type":%q`, kind)) {
				t.Fatalf("HTTP %d unexpected frame %q", resp.StatusCode, frame)
			}
			_ = resp.Body.Close()
			select {
			case record := <-records:
				wantFailed := kind == "response.in_progress"
				if record.Detail.TotalTokens != 10 || record.Failed != wantFailed || (wantFailed && record.Fail.StatusCode != clienterror.StatusClientClosedRequest) {
					t.Fatalf("unexpected usage after disconnect (want failed=%t, total=10): %+v", wantFailed, record)
				}
			case <-ctx.Done():
				t.Fatal("missing usage record after disconnect")
			}
		})
	}
}
