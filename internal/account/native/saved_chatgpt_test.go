package native

import (
	"encoding/json"
	"testing"
	"time"

	"dyna-agent/internal/account/store"
)

// piCapturedOf builds the credential of a Pi account that signed in to one
// provider, in the shape the Pi capture writes.
func piCapturedOf(t *testing.T, id string, entry piAuthEntry) Captured {
	t.Helper()
	payload, err := json.Marshal(map[string]json.RawMessage{"auth.json": mustJSON(map[string]piAuthEntry{id: entry})})
	if err != nil {
		t.Fatal(err)
	}
	return Captured{Identity: "pi:" + id, Credential: store.Credential{Schema: piNativeFilesSchema, Payload: payload}}
}

func TestChatGPTTokensReadsAPiAccount(t *testing.T) {
	login := piCapturedOf(t, "openai-codex", piAuthEntry{Type: "oauth", Access: "access-token", Refresh: "refresh-token", Expires: int64(1) << 50, AccountID: "workspace"})
	access, account, err := ChatGPTTokens(login.Credential)
	if err != nil || access != "access-token" || account != "workspace" {
		t.Fatalf("tokens = %q %q %v", access, account, err)
	}
}

func TestChatGPTTokensRejectsAnotherCredential(t *testing.T) {
	for _, c := range []store.Credential{
		{Schema: "muse/auth@1", Payload: json.RawMessage(`{}`)},
		piCapturedOf(t, "zai", piAuthEntry{Type: "api_key", Key: "sk-private"}).Credential,
		{Schema: piNativeFilesSchema, Payload: json.RawMessage(`{"auth.json":{}}`)},
	} {
		if _, _, err := ChatGPTTokens(c); err == nil {
			t.Fatalf("accepted %s", c.Payload)
		}
	}
}

// A Pi account reports its access-token expiry, which Voice reads to decide
// whether to renew the grant.
func TestPiAccountReportsItsAccessTokenExpiry(t *testing.T) {
	expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)
	login := piCapturedOf(t, "openai-codex", piAuthEntry{Type: "oauth", Access: "access-token", Refresh: "refresh-token", Expires: expiry.UnixMilli(), AccountID: "workspace"})
	if got := CredentialExpiry(login.Credential); !got.Equal(expiry) {
		t.Fatalf("expiry = %v, want %v", got, expiry)
	}
}
