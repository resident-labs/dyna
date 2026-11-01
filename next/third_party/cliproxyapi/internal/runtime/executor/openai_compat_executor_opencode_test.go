package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestOpenCodeSessionForwarding(t *testing.T) {
	seen := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("x-opencode-session")
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	for _, tc := range []struct {
		provider string
		stream   bool
		want     string
	}{
		{"opencode-go", false, "conversation-1"}, {"opencode-go", true, "conversation-1"}, {"other", false, ""},
	} {
		exec := NewOpenAICompatExecutor(tc.provider, &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: tc.provider}}})
		auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "test", "compat_name": tc.provider, "provider_key": tc.provider}}
		req := cliproxyexecutor.Request{Model: "test-model", Payload: []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`)}
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}
		ctx := util.WithSessionID(context.Background(), "affinity:conversation-1")
		if tc.stream {
			result, err := exec.ExecuteStream(ctx, auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			for range result.Chunks {
			}
		} else {
			_, _ = exec.Execute(ctx, auth, req, opts)
		}
		if got := <-seen; got != tc.want {
			t.Errorf("%s stream=%v: session = %q, want %q", tc.provider, tc.stream, got, tc.want)
		}
	}
}
