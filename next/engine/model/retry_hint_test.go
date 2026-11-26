package model

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
)

func TestModelRouteCarriesRetryHintInPiGError(t *testing.T) {
	for _, tc := range []struct {
		name, body, hint string
		status           int
		want             string
	}{
		{"messages", `{"type":"error","error":{"type":"rate_limit_error","message":"All credentials for model fixture are cooling down"}}`, "120", 429, `"reset_seconds":120`},
		{"responses", `{"error":{"code":"1305","message":"The service may be temporarily overloaded"}}`, "76", 429, `"reset_seconds":76`},
		{"completions", "", "42", 502, `"reset_seconds":42`},
		{"provider_quota", `{"error":{"code":"usage_limit_reached","message":"Weekly quota exhausted"}}`, "259200", 429, `"reset_seconds":259200`},
		{"provider_quota_capped", `{"error":{"code":"usage_limit_reached","message":"Weekly quota exhausted"}}`, "259200", 429, `Server requested 259200s retry delay (max: 1s). 429 Weekly quota exhausted`},
		{"guard_quota", `{"type":"error","error":{"type":"rate_limit_error","message":"All accounts are out of quota"}}`, "1209600", 429, `"reset_seconds":1209600`},
		{"no_hint_429", `{"error":{"code":"unknown","message":"failed"}}`, "", 429, ""},
		{"excessive_quota_hint", `{"error":{"message":"failed"}}`, "2678401", 429, ""},
		{"long_transient_429", `{"error":{"code":"transient_rate_limit","message":"failed"}}`, "7200", 429, ""},
		{"long_503", `{"error":{"message":"try again later"}}`, "7200", 503, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", tc.hint)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer up.Close()
			down := httptest.NewServer(Handler(up.Listener.Addr().String(), "secret", "session", []string{"fixture"}, []string{"account"}, "", nil, false))
			defer down.Close()
			var provider ai.Provider
			switch tc.name {
			case "messages", "guard_quota":
				provider = ai.NewAnthropicProvider(ai.AnthropicConfig{BaseURL: down.URL + "/model", APIKey: "box", Model: "fixture"})
			case "completions":
				provider = ai.NewOpenAIProvider(ai.OpenAIConfig{BaseURL: down.URL + "/model/v1", APIKey: "box", Model: "fixture"})
			default:
				provider = ai.NewOpenAIResponsesProvider(ai.OpenAIResponsesConfig{BaseURL: down.URL + "/model/v1", APIKey: "box", Model: "fixture"})
			}
			defer provider.Close()
			ctx := t.Context()
			opts := ai.StreamOptions{MaxRetries: new(0)}
			if tc.name == "provider_quota_capped" || tc.name == "guard_quota" {
				opts = ai.StreamOptions{MaxRetries: new(1), MaxRetryDelayMs: new(1000)}
			}
			stream, err := provider.Stream(ctx, ai.NormalizeContext(ai.Context{Messages: []ai.Message{ai.UserMessage{Content: ai.UserText("hi")}}}), opts)
			if err != nil {
				t.Fatal(err)
			}
			for range stream.Events(ctx) {
			}
			result := stream.Result()
			if result.StopReason != ai.StopReasonError || (tc.want != "" && !strings.Contains(result.ErrorMessage, tc.want)) || (tc.want == "" && strings.Contains(result.ErrorMessage, `"reset_seconds"`)) {
				t.Fatalf("wanted retry hint %q in a model failure; got %q, %q", tc.want, result.StopReason, result.ErrorMessage)
			}
		})
	}
}

func TestModelRoutePreservesHTTPDateRetryAfter(t *testing.T) {
	body := `{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`
	deadline := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	date := deadline.Format(http.TimeFormat)
	resp := &http.Response{StatusCode: 503, Header: http.Header{"Retry-After": {date}}, Body: io.NopCloser(strings.NewReader(body))}
	before := time.Now()
	if err := preserveModelRetryHint(resp); err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	encoded, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.Header.Get("Retry-After") != date || resp.ContentLength != int64(len(encoded)) {
		t.Fatalf("retry date %q, length %d, body %q: %v", resp.Header.Get("Retry-After"), resp.ContentLength, encoded, err)
	}
	var response struct {
		Error struct {
			ResetSeconds int `json:"reset_seconds"`
		} `json:"error"`
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	reset := time.Duration(response.Error.ResetSeconds) * time.Second
	if before.Add(reset).After(deadline.Add(time.Second)) || after.Add(reset).Before(deadline) {
		t.Fatalf("retry delay %s doesn't reflect deadline %s", reset, deadline)
	}
}
