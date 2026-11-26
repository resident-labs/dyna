package executor

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestXAIHTTPTerminalErrorPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, input, code, kind, param, message string
		status                                  int
	}{
		{name: "outer fields win", input: `{"type":"error","error":{"code":"resource_exhausted","type":"rate_limit_error","param":"outer","message":"outer message"},"response":{"status":"failed","error":{"code":"forbidden","type":"permission_error","param":"response","message":"response message"}},"code":"invalid_argument","error_type":"invalid_request_error","param":"root","message":"root message","body":{"error":{"code":"not_found","type":"not_found_error","param":"body","message":"body message"}}}`, code: "resource_exhausted", kind: "rate_limit_error", param: "outer", message: "outer message", status: 429},
		{name: "response fills missing outer fields", input: `{"type":"error","error":{"message":"outer message"},"response":{"status":"failed","error":{"code":"resource_exhausted","type":"rate_limit_error","message":"response message"}},"code":"invalid_argument","error_type":"invalid_request_error","param":"root","body":{"error":{"code":"not_found","param":"body"}}}`, code: "resource_exhausted", kind: "rate_limit_error", param: "root", message: "outer message", status: 429},
		{name: "root fields precede wrapped fields", input: `{"type":"error","error":{"message":"outer message","code":" "},"code":"resource_exhausted","error_type":"rate_limit_error","param":"root","body":{"error":{"code":"invalid_argument","type":"invalid_request_error","param":"body","message":"body message"}}}`, code: "resource_exhausted", kind: "rate_limit_error", param: "root", message: "outer message", status: 429},
		{name: "body fields fill remaining gaps", input: `{"type":"error","error":{"message":"outer message"},"body":{"error":{"code":"resource_exhausted","type":"rate_limit_error","param":"body","message":"body message"}}}`, code: "resource_exhausted", kind: "rate_limit_error", param: "body", message: "outer message", status: 429},
		{name: "failed response fields win and fall back to root", input: `{"type":"response.failed","response":{"error":{"message":"response message","param":"response"}},"error":{"code":"forbidden","message":"outer message"},"code":"resource_exhausted","error_type":"rate_limit_error","body":{"error":{"code":"invalid_argument"}}}`, code: "resource_exhausted", kind: "rate_limit_error", param: "response", message: "response message", status: 429},
		{name: "failed response without error falls back to outer", input: `{"type":"response.failed","response":{},"error":{"code":"resource_exhausted","message":"outer message"}}`, code: "resource_exhausted", message: "outer message", status: 429},
		{name: "failed response with empty error falls back to outer", input: `{"type":"response.failed","response":{"error":{}},"error":{"code":"resource_exhausted","message":"outer message"}}`, code: "resource_exhausted", message: "outer message", status: 429},
		{name: "plain outer detail takes only message slot", input: `{"type":"error","error":"outer message","response":{"error":{"code":"resource_exhausted","message":"response message"}}}`, code: "resource_exhausted", message: "outer message", status: 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{"Retry-After": []string{"73"}}
			err, ok := xaiTerminalError("", []byte(tc.input), true, headers)
			if !ok || err.StatusCode() != tc.status || err.IsStreamTruncated() {
				t.Fatalf("terminal failure = %+v (found %v), want HTTP %d provider error", err, ok, tc.status)
			}
			if delay := err.RetryAfter(); delay == nil || *delay != 73*time.Second {
				t.Errorf("Retry-After = %v, want 73s", delay)
			}
			detail := gjson.Get(err.Error(), "error")
			if detail.Get("code").String() != tc.code || detail.Get("type").String() != tc.kind || detail.Get("param").String() != tc.param || detail.Get("message").String() != tc.message {
				t.Errorf("error detail = %s, want code=%q type=%q param=%q message=%q", detail.Raw, tc.code, tc.kind, tc.param, tc.message)
			}
		})
	}
}

func TestXAIHTTPFailureUsage(t *testing.T) {
	const progress = `data: {"type":"response.in_progress","response":{"usage":{"input_tokens":7,"output_tokens":3}}}` + "\n\n"
	const failure = "event: response.failed\ndata: {\"type\":\"response.failed\",\ndata: \"response\":{\"error\":{\"code\":\"subscription:free-usage-exhausted\",\"message\":\"Free allowance used.\"},\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n"
	for _, tc := range []struct {
		name, body    string
		stream, short bool
		status        int
		retryAfter    time.Duration
		truncated     bool
	}{
		{name: "provider_failure_buffered", body: failure, status: 429, retryAfter: 24 * time.Hour},
		{name: "provider_failure_stream", body: failure, stream: true, short: true, status: 429, retryAfter: 24 * time.Hour},
		{name: "clean_eof_buffered", body: progress, status: 502, truncated: true},
		{name: "transport_cut_stream", body: progress, stream: true, short: true, status: 502, truncated: true},
		{name: "transport_cut_buffered", body: progress, short: true, status: 502, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.short {
					w.Header().Set("Content-Length", fmt.Sprint(len(tc.body)+8))
				}
				_, _ = io.WriteString(w, tc.body)
				w.(http.Flusher).Flush()
			}))
			defer upstream.Close()
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "xai", Attributes: map[string]string{"api_key": "test", "base_url": upstream.URL}}
			checkUsage := task6CaptureFailureUsageWithCheck(t, auth.ID, func(record usage.Record) {
				if record.Detail.TotalTokens != 10 || record.Fail.StatusCode != tc.status || (record.Fail.Code == usage.FailureStreamTruncatedCode) != tc.truncated {
					t.Errorf("incorrect usage outcome: status=%d reason=%q tokens=%d", record.Fail.StatusCode, record.Fail.Code, record.Detail.TotalTokens)
				}
			})
			defer checkUsage()
			ex := NewXAIExecutor(&config.Config{})
			req := cliproxyexecutor.Request{Model: "grok-4.7", Payload: []byte(`{"model":"grok-4.7","input":"hi"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: tc.stream}
			var execErr error
			if tc.stream {
				result, err := ex.ExecuteStream(t.Context(), auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						if execErr != nil {
							t.Errorf("duplicate stream error: %v", chunk.Err)
						}
						execErr = chunk.Err
					}
				}
			} else {
				_, execErr = ex.Execute(t.Context(), auth, req, opts)
			}
			status, ok := errors.AsType[statusErr](execErr)
			if !ok || status.StatusCode() != tc.status || status.IsStreamTruncated() != tc.truncated {
				t.Fatalf("error=%v; want status=%d truncated=%t", execErr, tc.status, tc.truncated)
			}
			if got := status.RetryAfter(); (got == nil && tc.retryAfter != 0) || (got != nil && *got != tc.retryAfter) {
				t.Errorf("Retry-After=%v, want %s", got, tc.retryAfter)
			}
		})
	}
}
