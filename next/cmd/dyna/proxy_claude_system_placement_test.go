package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

func TestActiveDynaClaudeResponsesInstructionPrefix(t *testing.T) {
	manager, service, router := importTestRoutes(t)
	const model, id = "claude-sonnet-5", importTestID
	auth := &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{
		"type": "claude", "access_token": "sk-ant-oat01-invented", "account_uuid": "invented-uuid",
	}}
	if _, err := manager.Register(coreauth.WithSkipPersist(t.Context()), auth); err != nil {
		t.Fatal(err)
	}
	if err := service.ActivateDynaCredential(t.Context(), id, model); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cliproxy.GlobalModelRegistry().UnregisterClient(id) })
	type capture struct {
		body  []byte
		betas string
	}
	requests := make(chan capture, 4)
	manager.SetRoundTripperProvider(compatStreamTestTransport{func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.anthropic.com" || r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "Bearer sk-ant-oat01-invented" {
			t.Errorf("unexpected upstream %s", r.URL)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		select {
		case requests <- capture{body, strings.Join(r.Header["anthropic-beta"], ",")}:
		default:
			t.Error("unexpected repeat upstream request")
		}
		w := httptest.NewRecorder()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.WriteString("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"model\":\"" + model + "\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
		return w.Result(), nil
	}})
	h := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router.POST("/v1/responses", h.Responses)
	const history = `{"role":"user","content":[{"type":"input_text","text":"original user","cache_control":{"type":"ephemeral"}}]},{"role":"assistant","content":[{"type":"output_text","text":"prior answer"}]},{"role":"user","content":"next user"}`
	const laterTurn = `,{"role":"assistant","content":"next answer"},{"role":"developer","content":[{"type":"input_text","text":"late rule","cache_control":{"type":"ephemeral"}}]},{"role":"user","content":"current user"}`
	const trailingRule = `,{"role":"assistant","content":"current answer"},{"role":"developer","content":"trailing rule"}`
	const ineligibleRule = `,{"role":"assistant","content":"current answer"},{"type":"custom_note","role":"developer","content":"trailing rule"},{"role":"user","content":"another user"}`
	for _, suffix := range []string{"", laterTurn, laterTurn + trailingRule, laterTurn + ineligibleRule} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+model+`","instructions":"original rule","prompt_cache_options":{"mode":"explicit"},"stream":true,"input":[`+history+suffix+`]}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"type":"response.completed"`) {
			t.Fatalf("response = %d %s", w.Code, w.Body.String())
		}
	}
	if len(requests) != 4 {
		t.Fatalf("captured %d upstream attempts", len(requests))
	}
	first, second := <-requests, <-requests
	// The trailing and ineligible rules must fall back without overtaking
	// the earlier rule, which has already crossed a user-turn boundary.
	for _, name := range []string{"trailing", "ineligible"} {
		messages := gjson.GetBytes((<-requests).body, "messages").Raw
		late, trailing := strings.Index(messages, "late rule"), strings.Index(messages, "trailing rule")
		if late < 0 || trailing < late {
			t.Errorf("%s fallback overtook an earlier rule: %s", name, messages)
		}
	}
	t.Logf("Claude messages on turn 1: %s\nClaude messages on turn 2: %s", gjson.GetBytes(first.body, "messages").Raw, gjson.GetBytes(second.body, "messages").Raw)
	before, after := gjson.GetBytes(first.body, "messages").Array(), gjson.GetBytes(second.body, "messages").Array()
	if !strings.Contains(second.betas, "mid-conversation-system-2026-04-07") || len(after) != len(before)+3 {
		t.Fatalf("wanted preserved prefix and three new messages: betas=%q first=%s second=%s", second.betas, gjson.GetBytes(first.body, "messages").Raw, gjson.GetBytes(second.body, "messages").Raw)
	}
	for i, msg := range before {
		if msg.Raw != after[i].Raw {
			t.Fatalf("message prefix changed at %d: %s -> %s", i, msg.Raw, after[i].Raw)
		}
	}
	last := after[len(after)-1]
	if last.Get("role").String() != "system" || last.Get("content.0.text").String() != "late rule" || last.Get("content.0.cache_control.type").String() != "ephemeral" || after[len(after)-2].Get("content").String() != "current user" {
		t.Fatalf("late cached instruction is misplaced: %s", gjson.GetBytes(second.body, "messages").Raw)
	}
}
