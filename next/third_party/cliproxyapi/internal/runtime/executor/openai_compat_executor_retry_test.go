package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestOpenAICompatRetryAfter(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		provider string
		status   int
		headers  http.Header
		body     string
		want     *time.Duration
	}{
		{
			name:    "delta seconds header",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": {"17"}},
			want:    durationPointer(17 * time.Second),
		},
		{
			name:    "http date header",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": {now.Add(23 * time.Second).Format(http.TimeFormat)}},
			want:    durationPointer(23 * time.Second),
		},
		{
			name:   "explicit TPM code fallback",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"code":"ModelAccountTpmRateLimitExceeded","message":"TPM limit exceeded"}}`,
			want:   durationPointer(time.Minute),
		},
		{
			name:   "TPM message fallback",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"TPM (Tokens Per Minute) limit of this model is exceeded"}}`,
			want:   durationPointer(time.Minute),
		},
		{
			name:    "provider header wins over fallback",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": {"5"}},
			body:    `{"error":{"code":"ModelAccountTpmRateLimitExceeded"}}`,
			want:    durationPointer(5 * time.Second),
		},
		{
			name:     "z.ai request-rate limit without hint",
			provider: "openai-compatible-zai",
			status:   http.StatusTooManyRequests,
			body:     `{"code":"1302","message":"Rate limit reached for requests"}`,
			want:     durationPointer(time.Minute),
		},
		{
			name:     "z.ai overload without hint",
			provider: "openai-compatible-zai",
			status:   http.StatusTooManyRequests,
			body:     `{"error":{"code":1305,"message":"The service may be temporarily overloaded"}}`,
			want:     durationPointer(20 * time.Second),
		},
		{
			name:     "explicit z.ai hint wins",
			provider: "openai-compatible-zai",
			status:   http.StatusTooManyRequests,
			headers:  http.Header{"Retry-After": {"15"}},
			body:     `{"code":"1302"}`,
			want:     durationPointer(15 * time.Second),
		},
		{
			name:     "z.ai code from another provider has no special cooldown",
			provider: "not-zai",
			status:   http.StatusTooManyRequests,
			body:     `{"code":"1302"}`,
		},
		{
			name:   "generic 429 has no invented deadline",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"code":"rate_limit"}}`,
		},
		{
			name:    "non-429 ignores header",
			status:  http.StatusServiceUnavailable,
			headers: http.Header{"Retry-After": {"30"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := openAICompatRetryAfter(test.provider, test.status, test.headers, []byte(test.body), now)
			if test.want == nil {
				if got != nil {
					t.Fatalf("retry-after = %v, want nil", *got)
				}
				return
			}
			if got == nil || *got != *test.want {
				t.Fatalf("retry-after = %v, want %v", got, *test.want)
			}
		})
	}
}

func TestOpenAICompatExecutorPropagatesRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit","message":"try later"}}`))
	}))
	t.Cleanup(server.Close)

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	request := cliproxyexecutor.Request{
		Model:   "compatible-model",
		Payload: []byte(`{"model":"compatible-model","messages":[{"role":"user","content":"hi"}]}`),
	}
	tests := []struct {
		name   string
		invoke func() error
	}{
		{
			name: "nonstream",
			invoke: func() error {
				_, errExecute := executor.Execute(context.Background(), auth, request, cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FromString("openai"),
				})
				return errExecute
			},
		},
		{
			name: "stream bootstrap",
			invoke: func() error {
				_, errExecute := executor.ExecuteStream(context.Background(), auth, request, cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FromString("openai"),
					Stream:       true,
				})
				return errExecute
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errExecute := test.invoke()
			if errExecute == nil {
				t.Fatal("expected rate-limit error")
			}
			retryable, ok := errExecute.(interface{ RetryAfter() *time.Duration })
			if !ok || retryable.RetryAfter() == nil || *retryable.RetryAfter() != 7*time.Second {
				t.Fatalf("retry-after = %v, want 7s", retryable)
			}
		})
	}
}

func durationPointer(value time.Duration) *time.Duration {
	return &value
}
