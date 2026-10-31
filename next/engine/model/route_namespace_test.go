package model

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRouteNamespacesAndSplices(t *testing.T) {
	var path, rawPath, body string
	up := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, rawPath = r.URL.Path, r.URL.RawPath
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer up.Close()
	tests := []struct {
		name, namespace, path, body, wantPath, wantBody string
		status                                          int
	}{
		{"plan", "plan_anthropic", "/model/v1/messages", `{"model":"same","x":1}`, "/v1/messages", `{"model":"plan_anthropic/same","x":1}`, 204},
		{"plan alias", "plan_kimi-coding", "/model/v1/chat/completions", `{"model":"k3"}`, "/v1/chat/completions", `{"model":"plan_kimi-coding/kimi-k3"}`, 204},
		{"key slash and spacing", "openrouter", "/model/v1/chat/completions", `{ "model"  : "vendor/one", "x": 1 }`, "/v1/chat/completions", `{ "model"  : "openrouter/vendor/one", "x": 1 }`, 204},
		{"escaped", "deepseek", "/model/v1/chat/completions", `{"model":"s\u0061me"}`, "/v1/chat/completions", `{"model":"deepseek/same"}`, 204},
		{"duplicate", "deepseek", "/model/v1/chat/completions", `{"model":"same","model":"same"}`, "", "", 400},
		{"trailing", "deepseek", "/model/v1/chat/completions", `{"model":"same"} {}`, "", "", 400},
		{"gemini escaped", "google", "/model/v1beta/models/vendor%2Fone:generateContent", `{}`, "/v1beta/models/google/vendor/one:generateContent", `{}`, 204},
		{"gemini plan", "plan_anthropic", "/model/v1beta/models/same:generateContent", `{}`, "", "", 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, rawPath, body = "", "", ""
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", tt.path, strings.NewReader(tt.body))
			Handler(up.Listener.Addr().String(), "key", "session", tt.namespace, []string{"same", "vendor/one", "k3"}, []string{"credential"}).ServeHTTP(w, req)
			if w.Code != tt.status || path != tt.wantPath || body != tt.wantBody {
				t.Fatalf("status=%d path=%q rawPath=%q body=%q", w.Code, path, rawPath, body)
			}
			if tt.name == "gemini escaped" && !strings.Contains(strings.ToLower(rawPath), "%2f") {
				t.Fatalf("missing escaped model path: %q", rawPath)
			}
		})
	}
}
