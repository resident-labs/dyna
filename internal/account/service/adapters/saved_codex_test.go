package adapters

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

func savedCodexCredential(t *testing.T) store.Credential {
	t.Helper()
	jwt := func(v any) string {
		raw, _ := json.Marshal(v)
		return "header." + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
	}
	raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     jwt(map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_user_id": "user", "chatgpt_account_id": "workspace"}}),
		"access_token": jwt(map[string]int64{"exp": time.Now().Add(time.Hour).Unix()}), "refresh_token": "saved-refresh", "account_id": "workspace"}, "future_field": true})
	payload, _ := json.Marshal(map[string]json.RawMessage{"auth.json": raw})
	return store.Credential{Schema: "codex/auth-file@1", Payload: payload}
}

type savedCodexTransport func(*http.Request) (*http.Response, error)

func (f savedCodexTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSavedCodexRefreshRateLimitIsTyped(t *testing.T) {
	credential := savedCodexCredential(t)
	var payload map[string]json.RawMessage
	var doc map[string]json.RawMessage
	var tokens map[string]json.RawMessage
	_ = json.Unmarshal(credential.Payload, &payload)
	_ = json.Unmarshal(payload["auth.json"], &doc)
	_ = json.Unmarshal(doc["tokens"], &tokens)
	expired, _ := json.Marshal(map[string]int64{"exp": time.Now().Add(-time.Hour).Unix()})
	tokens["access_token"], _ = json.Marshal("header." + base64.RawURLEncoding.EncodeToString(expired) + ".signature")
	doc["tokens"], _ = json.Marshal(tokens)
	payload["auth.json"], _ = json.Marshal(doc)
	credential.Payload, _ = json.Marshal(payload)
	previous := http.DefaultTransport
	defer func() { http.DefaultTransport = previous }()
	calls := 0
	http.DefaultTransport = savedCodexTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.String() != "https://auth.openai.com/oauth/token" {
			t.Error("unexpected refresh request")
		}
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"23"}}, Body: io.NopCloser(strings.NewReader("provider-secret"))}, nil
	})
	manager, ok := native.For("codex", native.Host{})
	if !ok {
		t.Fatal("missing manager")
	}
	_, err := manager.Refresh(context.Background(), native.Captured{Identity: "openai:user:workspace", Credential: credential})
	var quota *native.QuotaError
	if calls != 1 || !errors.As(err, &quota) || quota.Status != 429 || quota.RetryAfter != 23*time.Second || strings.Contains(err.Error(), "provider-secret") {
		t.Fatalf("refresh rate limit lost: %v", err)
	}
}
