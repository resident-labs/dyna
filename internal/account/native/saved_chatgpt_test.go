package native

import (
	"encoding/json"
	"testing"

	"dyna-agent/internal/account/store"
)

func TestChatGPTTokensReadsAPiAccount(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"auth.json": map[string]any{"openai-codex": map[string]any{
		"type": "oauth", "access": "access-token", "refresh": "refresh-token", "expires": int64(1) << 50, "accountId": "workspace",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	access, account, err := ChatGPTTokens(store.Credential{Schema: piNativeFilesSchema, Payload: payload})
	if err != nil || access != "access-token" || account != "workspace" {
		t.Fatalf("tokens = %q %q %v", access, account, err)
	}
}

func TestChatGPTTokensRejectsAnotherCredential(t *testing.T) {
	for _, c := range []store.Credential{
		{Schema: "muse/auth@1", Payload: json.RawMessage(`{}`)},
		{Schema: piNativeFilesSchema, Payload: json.RawMessage(`{"auth.json":{"zai":{"type":"api_key","key":"sk-private"}}}`)},
		{Schema: piNativeFilesSchema, Payload: json.RawMessage(`{"auth.json":{}}`)},
	} {
		if _, _, err := ChatGPTTokens(c); err == nil {
			t.Fatalf("accepted %s", c.Payload)
		}
	}
}
