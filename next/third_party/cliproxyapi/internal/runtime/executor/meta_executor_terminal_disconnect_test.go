package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type metaTerminalDisconnectUsageCapture struct {
	authID  string
	records chan usage.Record
}

func (c *metaTerminalDisconnectUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if c == nil || record.Provider != "meta" || record.AuthID != c.authID {
		return
	}
	select {
	case c.records <- record:
	default:
	}
}

type metaTerminalDisconnectUsageNoop struct{}

func (metaTerminalDisconnectUsageNoop) HandleUsage(context.Context, usage.Record) {}

const (
	metaTerminalDisconnectDelta     = "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n"
	metaTerminalDisconnectCompleted = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"muse-wonder-1.0\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n"
)

// The upstream keeps the body open after the written events, as Meta does
// while it still has to send response.subscription_usage. A Claude client
// cancels once it sees cancelAfter.
func runMetaClaudeDisconnect(t *testing.T, upstreamSSE, cancelAfter string) usage.Record {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamSSE)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}
	capture := &metaTerminalDisconnectUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), metaTerminalDisconnectUsageNoop{})
	})

	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	result, errExecute := NewMetaExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: []byte(`{"model":"muse-wonder-1.0","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Stream:       true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	canceled := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error before cancellation: %v", chunk.Err)
		}
		if strings.Contains(string(chunk.Payload), cancelAfter) {
			canceled = true
			cancel(errors.New("client disconnected"))
			break
		}
	}
	if !canceled {
		t.Fatalf("stream ended before %q", cancelAfter)
	}

	select {
	case record := <-capture.records:
		return record
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for usage record")
		return usage.Record{}
	}
}

func TestMetaStream_ClientDisconnectAfterTerminalEventIsNotFailed(t *testing.T) {
	record := runMetaClaudeDisconnect(t, metaTerminalDisconnectDelta+metaTerminalDisconnectCompleted, "message_stop")
	if record.Failed {
		t.Fatalf("usage record marked failed: status=%d", record.Fail.StatusCode)
	}
	if record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 5 {
		t.Fatalf("reported usage = %+v, want input 10 output 5", record.Detail)
	}
}

func TestMetaStream_ClientDisconnectBeforeTerminalEventIsFailed(t *testing.T) {
	record := runMetaClaudeDisconnect(t, metaProgressUsage+metaTerminalDisconnectDelta, "content_block_delta")
	if !record.Failed {
		t.Fatal("usage record not marked failed")
	}
	if record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 5 {
		t.Fatalf("reported usage = %+v, want input 10 output 5", record.Detail)
	}
	if record.Fail.StatusCode != clienterror.StatusClientClosedRequest {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, clienterror.StatusClientClosedRequest)
	}
}

// A usage-bearing progress event lets the cancellation and EOF fixtures check
// that failure publication does not discard usage already observed upstream.
const metaProgressUsage = "data: {\"type\":\"response.in_progress\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n"

func TestMetaStream_TerminalHTTP(t *testing.T) {
	done := strings.Replace(metaTerminalDisconnectCompleted, "response.completed", "response.done", 1)
	incomplete := strings.Replace(done, `"status":"completed"`, `"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}`, 1)
	failed := strings.Replace(done, `"status":"completed"`, `"status":"failed"`, 1)
	for _, tc := range []struct {
		name   string
		format sdktranslator.Format
		body   string
		want   string
		status int
		cut    bool
	}{
		{"empty", sdktranslator.FormatOpenAIResponse, "", "", 502, true},
		{"partial", sdktranslator.FormatOpenAIResponse, metaProgressUsage + metaTerminalDisconnectDelta, "hello", 502, true},
		{"read_cut", sdktranslator.FormatOpenAIResponse, metaProgressUsage, `"type":"response.in_progress"`, 502, true},
		{"native", sdktranslator.FormatOpenAIResponse, "event: response.done\n" + done, "event: response.done\ndata: {\"type\":\"response.done\"", 0, false},
		{"claude", sdktranslator.FormatClaude, metaTerminalDisconnectDelta + done, `"type":"message_stop"`, 0, false},
		{"incomplete", sdktranslator.FormatClaude, metaTerminalDisconnectDelta + incomplete, `"stop_reason":"max_tokens"`, 0, false},
		{"failed_done", sdktranslator.FormatOpenAIResponse, failed, "", 502, false},
		{"failed", sdktranslator.FormatOpenAIResponse, strings.Replace(failed, "response.done", "response.failed", 1), "", 502, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.name == "read_cut" {
					w.Header().Set("Content-Length", "2048")
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "meta", Attributes: map[string]string{"api_key": "meta-token", "base_url": server.URL}}
			capture := &metaTerminalDisconnectUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), metaTerminalDisconnectUsageNoop{}) })
			result, err := NewMetaExecutor(&config.Config{}).ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{
				Model: "muse-wonder-1.0", Payload: []byte(`{"model":"muse-wonder-1.0","input":"hello"}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: tc.format, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var payload strings.Builder
			var streamErr error
			for chunk := range result.Chunks {
				if streamErr != nil {
					t.Fatalf("unexpected chunk after stream error: %+v", chunk)
				}
				streamErr = chunk.Err
				if len(chunk.Payload) > 0 {
					payload.WriteString(string(chunk.Payload))
					payload.WriteByte('\n')
				}
			}
			if tc.status != 0 {
				status, ok := streamErr.(statusErr)
				if !ok || status.StatusCode() != tc.status || status.IsStreamTruncated() != tc.cut {
					t.Fatalf("stream error = %#v, want status %d, truncated %t", streamErr, tc.status, tc.cut)
				}
			} else if streamErr != nil {
				t.Fatal(streamErr)
			}
			if !strings.Contains(payload.String(), tc.want) || (tc.want == "" && payload.Len() != 0) {
				t.Fatalf("payload = %q, want %q", payload.String(), tc.want)
			}
			select {
			case record := <-capture.records:
				if record.Failed != (tc.status != 0) || record.Fail.StatusCode != tc.status {
					t.Fatalf("unexpected usage outcome: %+v", record)
				}
				if tc.cut && record.Fail.Code != usage.FailureStreamTruncatedCode {
					t.Fatalf("expected stream-truncated usage code, got %+v", record.Fail)
				}
				if tc.body != "" && (record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 5) {
					t.Fatalf("usage = %+v, want input 10 output 5", record.Detail)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for usage")
			}
		})
	}
}

func TestMetaExecute_ResponseDoneHTTP(t *testing.T) {
	done := strings.Replace(metaTerminalDisconnectCompleted, "response.completed", "response.done", 1)
	for _, tc := range []struct{ name, contentType, body string }{
		{"SSE", "text/event-stream", "event: response.done\n" + done},
		{"JSON", "application/json", strings.TrimSpace(strings.TrimPrefix(done, "data: "))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "meta", Attributes: map[string]string{"api_key": "meta-token", "base_url": server.URL}}
			result, err := NewMetaExecutor(&config.Config{}).Execute(t.Context(), auth, cliproxyexecutor.Request{
				Model: "muse-wonder-1.0", Payload: []byte(`{"model":"muse-wonder-1.0","input":"hello"}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
			if err != nil || !strings.Contains(string(result.Payload), `"output_tokens":5`) || !strings.Contains(string(result.Payload), `"text":"hello"`) {
				t.Fatalf("response = %s, error = %v", result.Payload, err)
			}
		})
	}
}

func assertMetaTruncation(t *testing.T, err error) {
	t.Helper()
	status, ok := errors.AsType[statusErr](err)
	if !ok || status.StatusCode() != http.StatusBadGateway || !status.IsStreamTruncated() || !strings.HasPrefix(status.Error(), metaIncompleteStreamMessage) {
		t.Fatalf("expected typed Meta 502 truncation, got %v", err)
	}
}

func TestMetaTruncationDoesNotReplayOrCool(t *testing.T) {
	for _, name := range []string{"buffered", "streamed"} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.done\"}\n\n")
			}))
			defer server.Close()
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(NewMetaExecutor(&config.Config{}))
			manager.SetRetryConfig(2, time.Second, 2)
			const model = "muse-wonder-1.0"
			ids := []string{t.Name() + "/a", t.Name() + "/b"}
			for _, id := range ids {
				auth := &cliproxyauth.Auth{ID: id, Provider: "meta", Status: cliproxyauth.StatusActive, Attributes: map[string]string{"api_key": id, "base_url": server.URL}}
				if _, err := manager.Register(t.Context(), auth); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "meta", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"input":"hello"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: name == "streamed"}
			if opts.Stream {
				result, err := manager.ExecuteStream(t.Context(), []string{"meta"}, req, opts)
				if err == nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							err = chunk.Err
						}
					}
				}
				assertMetaTruncation(t, err)
			} else {
				_, err := manager.Execute(t.Context(), []string{"meta"}, req, opts)
				assertMetaTruncation(t, err)
			}
			if calls := requests.Load(); calls != 1 {
				t.Fatalf("accepted generation was replayed: %d upstream requests", calls)
			}
			for _, id := range ids {
				auth, ok := manager.GetByID(id)
				if !ok || auth.Unavailable || !auth.NextRetryAfter.IsZero() {
					t.Fatalf("credential cooled after truncation: %+v", auth)
				}
				if state := auth.ModelStates[model]; state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero()) {
					t.Fatalf("model cooled after truncation: %+v", state)
				}
			}
		})
	}
}

func TestMetaAcceptedBodyValidation(t *testing.T) {
	done := strings.Replace(metaTerminalDisconnectCompleted, "response.completed", "response.done", 1)
	const partialError = `{"type":"error","response":{"usage":{"input_tokens":99}},"error":{"code":429,"message":`
	const intactError = `{"type":"error","error":{"code":429,"message":"too many requests"}}`
	const patch = `{"input":"hello","tools":[{"type":"custom","name":"apply_patch"}]}`
	for _, tc := range []struct {
		name, body, payload string
		stream, cut, fail   bool
		providerStatus      int
		tokens              int64
	}{
		{"bare_done_stream", "event: response.done\ndata: {\"type\":\"response.done\"}\n\n", patch, true, false, true, 0, 0},
		{"bare_done_sse", "data: {\"type\":\"response.done\"}\n\n", patch, false, false, true, 0, 0},
		{"bare_done_json", `{"type":"response.done"}`, patch, false, false, true, 0, 0},
		{"invalid_terminal_json", "data: {\"type\":\"response.done\",\"response\":{}\n\n", patch, false, false, true, 0, 0},
		{"raw_response", `{"object":"response","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5}}`, `{"input":"hello"}`, false, false, false, 0, 10},
		{"terminal_before_cut", done + "event: response.subscription_usage\ndata: {", `{"input":"hello"}`, false, true, false, 0, 10},
		{"progress_before_cut", metaProgressUsage, `{"input":"hello"}`, false, true, true, 0, 10},
		{"error_cut_stream", metaProgressUsage + "data: " + partialError, patch, true, true, true, 0, 10},
		{"error_cut_sse", metaProgressUsage + "data: " + partialError, patch, false, true, true, 0, 10},
		{"error_cut_json", partialError, patch, false, true, true, 0, 0},
		{"intact_error_stream", metaProgressUsage + "data: " + intactError + "\n\n", patch, true, true, true, 429, 10},
		{"intact_error_sse", metaProgressUsage + "data: " + intactError + "\n\n", patch, false, true, true, 429, 10},
		{"intact_error_json", intactError, patch, false, false, true, 429, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.cut {
					w.Header().Set("Content-Length", "2048")
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "meta", Attributes: map[string]string{"api_key": "meta-token", "base_url": server.URL}}
			capture := &metaTerminalDisconnectUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), metaTerminalDisconnectUsageNoop{}) })
			exec := NewMetaExecutor(&config.Config{})
			req := cliproxyexecutor.Request{Model: "muse-wonder-1.0", Payload: []byte(tc.payload)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: tc.stream}
			assertError := func(err error) {
				t.Helper()
				if tc.providerStatus == 0 {
					assertMetaTruncation(t, err)
					return
				}
				status, ok := errors.AsType[statusErr](err)
				if !ok || status.IsStreamTruncated() || status.StatusCode() != tc.providerStatus {
					t.Fatalf("expected provider status %d, got %v", tc.providerStatus, err)
				}
			}
			if tc.stream {
				result, err := exec.ExecuteStream(t.Context(), auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				var streamErr error
				for chunk := range result.Chunks {
					if streamErr != nil || (len(chunk.Payload) != 0 && !strings.Contains(string(chunk.Payload), "response.in_progress")) {
						t.Fatalf("unexpected terminal output: %+v", chunk)
					}
					streamErr = chunk.Err
				}
				assertError(streamErr)
			} else {
				result, err := exec.Execute(t.Context(), auth, req, opts)
				if tc.fail {
					assertError(err)
					if len(result.Payload) != 0 {
						t.Fatalf("unexpected response on truncation: %s", result.Payload)
					}
				} else if err != nil || !strings.Contains(string(result.Payload), `"status":"completed"`) {
					t.Fatalf("response = %s, error = %v", result.Payload, err)
				}
			}
			select {
			case record := <-capture.records:
				if record.Failed != tc.fail || record.Detail.InputTokens != tc.tokens ||
					(tc.providerStatus != 0 && record.Fail.StatusCode != tc.providerStatus) ||
					(tc.fail && tc.providerStatus == 0 && record.Fail.Code != usage.FailureStreamTruncatedCode) {
					t.Fatalf("unexpected usage outcome: %+v", record)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for usage")
			}
		})
	}
}
