package executor

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeResponsesMidSessionSystemWirePolicy(t *testing.T) {
	const model = "claude-sonnet-5"
	const oauth = "sk-ant-oat01-invented"
	for _, tc := range []struct {
		name, model, token, base, cloak        string
		strict, rebuild, strip, count, inPlace bool
	}{
		{name: "first-party OAuth", model: model, token: oauth, inPlace: true},
		{name: "legacy", model: "claude-sonnet-4-6", token: oauth},
		{name: "unmeasured model", model: "claude-fable-5-2", token: oauth},
		{name: "delegated wire", model: model, token: oauth, base: "https://gateway.invalid"},
		{name: "cloaked key", model: model, token: "fixture", cloak: "always"},
		{name: "uncloaked", model: model, token: "fixture", cloak: "never"},
		{name: "strict mode", model: model, token: oauth, strict: true},
		{name: "rebuild", model: model, token: oauth, rebuild: true},
		{name: "beta stripped", model: model, token: oauth, strip: true},
		{name: "upstream count", model: model, token: oauth, count: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			if tc.strip {
				cfg.ClaudeHeaderDefaults.AnthropicBetaStrip = []string{claudeMidConvSystemBeta}
			}
			attrs := map[string]string{"base_url": tc.base, "cloak_mode": tc.cloak}
			if tc.strict {
				attrs["cloak_strict_mode"] = "true"
			}
			if tc.rebuild {
				attrs["rebuild_mid_system_message"] = "true"
			}
			auth := &cliproxyauth.Auth{Attributes: attrs}
			if tc.token == oauth {
				auth.Metadata = map[string]any{"type": "claude", "access_token": oauth, "account_uuid": "invented-uuid"}
			} else {
				attrs["api_key"] = tc.token
			}
			payload := []byte(`{"model":"` + tc.model + `","instructions":"leading rule","input":[{"role":"user","content":"earlier user"},{"role":"assistant","content":"earlier assistant"},{"role":"developer","content":"late rule"},{"role":"user","content":"final user"}]}`)
			ex, upstream := NewClaudeExecutor(cfg), &midSystemUpstream{}
			req := cliproxyexecutor.Request{Model: tc.model, Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
			var err error
			if tc.count {
				_, err = ex.CountTokens(upstream.context(t, nil), auth, req, opts)
			} else {
				_, err = ex.Execute(upstream.context(t, nil), auth, req, opts)
			}
			if err != nil || !upstream.called {
				t.Fatalf("wire not reached: %v", err)
			}
			messages := gjson.GetBytes(upstream.body, "messages").Array()
			inPlace := len(messages) > 1 && messages[len(messages)-1].Get("role").String() == "system" && strings.Contains(messages[len(messages)-1].Raw, "late rule")
			if inPlace != tc.inPlace {
				t.Fatalf("unexpected instruction placement: %s", gjson.GetBytes(upstream.body, "messages").Raw)
			}
			if tc.strict && strings.Contains(string(upstream.body), "late rule") {
				t.Fatal("strict mode retained a caller rule")
			}
			if (tc.strict || tc.strip || tc.count) && inPlace {
				t.Fatal("a gated route placed the late rule in messages")
			}
		})
	}
}
