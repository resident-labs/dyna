package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const (
	compatTextPartial = `data: {"id":"chatcmpl_fixture","object":"chat.completion.chunk","model":"local-model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}` + "\n\n"
	compatTextFinish  = `data: {"id":"chatcmpl_fixture","object":"chat.completion.chunk","model":"local-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	compatTextUsage   = `data: {"id":"chatcmpl_fixture","object":"chat.completion.chunk","model":"local-model","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}` + "\n\n"
)

func runOpenAICompatTextFixture(t *testing.T, ctx context.Context, format sdktranslator.Format, handler http.HandlerFunc) (*cliproxyexecutor.StreamResult, *multiProviderUsageCapture) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("provider request path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	capture := &multiProviderUsageCapture{alias: t.Name(), records: make(chan usage.Record, 1)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })
	ctx = usage.WithRequestedModelAlias(ctx, t.Name())
	const provider = "openai-compatible-fixture"
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: provider, Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "fixture"}}
	result, err := NewOpenAICompatExecutor(provider, &config.Config{}).ExecuteStream(ctx, auth,
		cliproxyexecutor.Request{Model: "local-model", Payload: []byte(`{"stream":true,"messages":[{"role":"user","content":"hi"}]}`)},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: format, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	return result, capture
}

func TestOpenAICompatTextCancellationAtRenderedBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, frame, after string
		format             sdktranslator.Format
		failure            bool
	}{
		{"chat_before_finish", compatTextPartial, "hello", sdktranslator.FormatOpenAI, true},
		{"chat_after_finish", compatTextPartial + compatTextFinish, `"finish_reason":"stop"`, sdktranslator.FormatOpenAI, false},
		{"claude_before_stop", compatTextPartial, "hello", sdktranslator.FormatClaude, true},
		{"claude_after_stop", compatTextPartial + compatTextFinish + compatTextUsage, `"type":"message_stop"`, sdktranslator.FormatClaude, false},
		{"responses_before_completion", compatTextPartial, `"type":"response.output_text.delta"`, sdktranslator.FormatOpenAIResponse, true},
		{"responses_after_completion", compatTextPartial + compatTextFinish + compatTextUsage + "data: [DONE]\n\n", `"type":"response.completed"`, sdktranslator.FormatOpenAIResponse, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, timeout := context.WithTimeout(t.Context(), 5*time.Second)
			defer timeout()
			ctx, cancel := context.WithCancelCause(ctx)
			defer cancel(nil)
			result, capture := runOpenAICompatTextFixture(t, ctx, tc.format, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.frame)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			})
			found := false
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				if strings.Contains(string(chunk.Payload), tc.after) {
					found = true
					cancel(errors.New("fixture client disconnected"))
					break
				}
			}
			if !found {
				t.Fatalf("terminal boundary %s was not emitted", tc.after)
			}
			record := capture.await(t)
			if record.Failed != tc.failure || record.Detail.TotalTokens != 15 || (tc.failure && record.Fail.StatusCode != clienterror.StatusClientClosedRequest) {
				t.Fatalf("incorrect cancellation outcome: %+v", record)
			}
			for chunk := range result.Chunks {
				t.Errorf("unexpected output after cancellation: %+v", chunk)
			}
		})
	}
}

func TestOpenAICompatTextAcceptedReadCut(t *testing.T) {
	const intact = `data: {"error":{"status":429,"message":"upstream throttle"}}`
	const cut = `data: {"usage":{"prompt_tokens":99,"total_tokens":99},"error":{"status":429,"message":`
	for _, tc := range []struct {
		name, finalFrame string
		status           int
		truncated        bool
	}{
		{"partial_error", cut, 502, true},
		{"intact_error", intact, 429, false},
		{"no_error_frame", "", 502, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			body := compatTextPartial + tc.finalFrame
			result, capture := runOpenAICompatTextFixture(t, ctx, sdktranslator.FormatOpenAIResponse, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", fmt.Sprint(len(body)+100))
				_, _ = io.WriteString(w, body)
			})
			var err error
			for chunk := range result.Chunks {
				if openAICompatTextChunkIsTerminal(sdktranslator.FormatOpenAIResponse, chunk.Payload) {
					t.Fatal("read cut synthesized a successful terminal")
				}
				if chunk.Err != nil {
					err = chunk.Err
				}
			}
			status, ok := errors.AsType[statusErr](err)
			if !ok || status.StatusCode() != tc.status || status.IsStreamTruncated() != tc.truncated {
				t.Fatalf("terminal error = %#v", err)
			}
			record := capture.await(t)
			if !record.Failed || record.Detail.TotalTokens != 15 || record.Fail.StatusCode != tc.status || (tc.truncated && record.Fail.Code != usage.FailureStreamTruncatedCode) {
				t.Fatalf("incorrect usage for read cut: %+v", record)
			}
		})
	}
}

func TestOpenAICompatTextTerminalMustBeStructural(t *testing.T) {
	for _, payload := range []string{
		`event: response.completed`,
		`data: {"type":"response.completed"}`,
		`data: {"type":"response.output_text.delta","delta":"{\"type\":\"response.completed\"}"}`,
		`data: {"other":{"type":"response.completed","response":{}}}`,
	} {
		if openAICompatTextChunkIsTerminal(sdktranslator.FormatOpenAIResponse, []byte(payload)) {
			t.Fatalf("classified %q as a terminal event", payload)
		}
	}
}

func TestOpenAICompatResponsesUsageWaitsForDelivery(t *testing.T) {
	for _, delivered := range []bool{true, false} {
		t.Run(fmt.Sprintf("delivered=%t", delivered), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ctx, finish := usage.WithStreamDelivery(ctx)
			defer finish(context.Canceled)
			result, capture := runOpenAICompatTextFixture(t, ctx, sdktranslator.FormatOpenAIResponse, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, compatTextPartial+compatTextFinish+compatTextUsage+"data: [DONE]\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			})
			terminal := false
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				terminal = terminal || openAICompatTextChunkIsTerminal(sdktranslator.FormatOpenAIResponse, chunk.Payload)
			}
			if !terminal || !usage.StreamDeliverySupported(ctx) {
				t.Fatal("expected terminal chunk and live delivery acknowledgment")
			}
			select {
			case record := <-capture.records:
				t.Fatalf("usage published before delivery acknowledgment: %+v", record)
			default:
			}
			if delivered {
				finish(nil)
			} else {
				finish(context.Canceled)
			}
			record := capture.await(t)
			if record.Failed == delivered || record.Detail.TotalTokens != 15 {
				t.Fatalf("incorrect delivery outcome: %+v", record)
			}
		})
	}
}

func TestOpenAICompatTextCleanEOFTerminals(t *testing.T) {
	for _, tc := range []struct {
		name, upstream, terminal string
		format                   sdktranslator.Format
	}{
		{"chat_finish_only", compatTextPartial + compatTextFinish, `"finish_reason":"stop"`, sdktranslator.FormatOpenAI},
		{"chat_explicit_done", compatTextPartial + compatTextFinish + compatTextUsage + "data: [DONE]\n\n", `"finish_reason":"stop"`, sdktranslator.FormatOpenAI},
		{"claude_terminal_with_usage", compatTextPartial + compatTextUsage, `"type":"message_stop"`, sdktranslator.FormatClaude},
		{"responses_confirmed_finish_only", compatTextPartial + compatTextFinish + compatTextUsage, `"type":"response.completed"`, sdktranslator.FormatOpenAIResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, capture := runOpenAICompatTextFixture(t, t.Context(), tc.format, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.upstream)
			})
			var body strings.Builder
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				body.Write(chunk.Payload)
			}
			if strings.Count(body.String(), tc.terminal) != 1 || !strings.Contains(body.String(), "hello") {
				t.Fatalf("missing or repeated terminal %q in %s", tc.terminal, body.String())
			}
			if record := capture.await(t); record.Failed || record.Detail.TotalTokens != 15 {
				t.Fatalf("incorrect terminal usage: %+v", record)
			}
		})
	}
}
