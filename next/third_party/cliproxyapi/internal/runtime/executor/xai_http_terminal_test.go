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
)

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
