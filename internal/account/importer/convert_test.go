package importer

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/account/store"
)

// fakeJWT builds a token whose payload carries exp, as both OAuth providers do.
func fakeJWT(exp int64) string {
	encode := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return encode(struct {
		Alg string `json:"alg"`
	}{Alg: "none"}) + "." + encode(struct {
		Exp int64 `json:"exp"`
	}{Exp: exp}) + ".signature"
}

// codexCredential is Codex's saved ChatGPT login in its native shape.
func codexCredential(exp int64) store.Credential {
	auth, _ := json.Marshal(struct {
		Tokens struct {
			Access    string `json:"access_token"`
			Refresh   string `json:"refresh_token"`
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}{Tokens: struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		AccountID string `json:"account_id"`
	}{Access: fakeJWT(exp), Refresh: "codex-refresh", AccountID: "account-1"}})
	payload, _ := json.Marshal(struct {
		Auth json.RawMessage `json:"auth.json"`
	}{Auth: auth})
	return store.Credential{Schema: "codex/auth-file@1", Payload: payload}
}

// openCodeCredential writes one OpenCode credential row and returns the saved
// reference that names it.
func openCodeCredential(t *testing.T, integration, value string) store.Credential {
	t.Helper()
	database := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", "file:"+database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE credential (id TEXT PRIMARY KEY, integration_id TEXT NOT NULL, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO credential(id, integration_id, value) VALUES ('cred-1', ?, ?)", integration, value); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(struct {
		Ref struct {
			Integration  string `json:"integration"`
			CredentialID string `json:"credentialID"`
			Scope        struct {
				Database string `json:"database"`
			} `json:"scope"`
		} `json:"ref"`
	}{Ref: struct {
		Integration  string `json:"integration"`
		CredentialID string `json:"credentialID"`
		Scope        struct {
			Database string `json:"database"`
		} `json:"scope"`
	}{Integration: integration, CredentialID: "cred-1", Scope: struct {
		Database string `json:"database"`
	}{Database: database}}})
	return store.Credential{Schema: "opencode/v2-ref@1", Payload: payload}
}

// TestConvertMapsEachSavedSignIn pins every provider mapping to its exact Pi
// auth entry, because a wrong field name signs the account out.
func TestConvertMapsEachSavedSignIn(t *testing.T) {
	exp := int64(1_800_000_000)
	grokExpiry := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	claude, _ := json.Marshal(struct {
		Credentials map[string]struct {
			AccessToken  string   `json:"accessToken"`
			RefreshToken string   `json:"refreshToken"`
			ExpiresAt    int64    `json:"expiresAt"`
			Scopes       []string `json:"scopes"`
		} `json:"credentials"`
	}{Credentials: map[string]struct {
		AccessToken  string   `json:"accessToken"`
		RefreshToken string   `json:"refreshToken"`
		ExpiresAt    int64    `json:"expiresAt"`
		Scopes       []string `json:"scopes"`
	}{"claudeAiOauth": {AccessToken: "claude-access", RefreshToken: "claude-refresh", ExpiresAt: 1_900_000_000_000, Scopes: []string{"user:inference"}}}})
	kimiToken, _ := json.Marshal(struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int64  `json:"expires_at"`
	}{Access: "kimi-access", Refresh: "kimi-refresh", Expires: exp})
	kimi, _ := json.Marshal(map[string]string{"config.toml": "model = \"kimi\"\n", "credentials/kimi-code.json": string(kimiToken)})
	grokAuth, _ := json.Marshal(struct {
		Slots map[string]struct {
			Key      string `json:"key"`
			Refresh  string `json:"refresh_token"`
			Expires  string `json:"expires_at"`
			Mode     string `json:"auth_mode"`
			Issuer   string `json:"oidc_issuer"`
			ClientID string `json:"oidc_client_id"`
		} `json:"auth.json"`
	}{Slots: map[string]struct {
		Key      string `json:"key"`
		Refresh  string `json:"refresh_token"`
		Expires  string `json:"expires_at"`
		Mode     string `json:"auth_mode"`
		Issuer   string `json:"oidc_issuer"`
		ClientID string `json:"oidc_client_id"`
	}{grokIssuer + "::" + grokClient: {
		Key: "grok-access", Refresh: "grok-refresh", Expires: grokExpiry.Format(time.RFC3339Nano), Mode: "oidc", Issuer: grokIssuer, ClientID: grokClient,
	}}})
	muse, _ := json.Marshal(map[string]json.RawMessage{"metaCredential": json.RawMessage(`{"api_key":"muse-key"}`)})

	cases := []struct {
		name      string
		harness   string
		cred      store.Credential
		provider  store.Provider
		kind      store.Kind
		entryID   string
		wantEntry string
	}{
		{
			name: "codex", harness: "codex", provider: store.ProviderChatGPT, kind: store.KindOAuth, entryID: "openai-codex",
			cred:      codexCredential(exp),
			wantEntry: `{"type":"oauth","access":"` + fakeJWT(exp) + `","refresh":"codex-refresh","expires":1800000000000,"accountId":"account-1"}`,
		},
		{
			name: "kimi-code", harness: "kimi-code", provider: store.ProviderKimiCode, kind: store.KindOAuth, entryID: "kimi-coding",
			cred:      store.Credential{Schema: "kimi-code/native-files@1", Payload: kimi},
			wantEntry: `{"type":"oauth","access":"kimi-access","refresh":"kimi-refresh","expires":1800000000000}`,
		},
		{
			name: "grok", harness: "grok", provider: store.ProviderXAI, kind: store.KindOAuth, entryID: "xai",
			cred:      store.Credential{Schema: "grok/native-files@1", Payload: grokAuth},
			wantEntry: `{"type":"oauth","access":"grok-access","refresh":"grok-refresh","expires":` + jsonInt(grokExpiry.UnixMilli()) + `}`,
		},
		{
			name: "claude-code", harness: "claude-code", provider: store.ProviderAnthropic, kind: store.KindOAuth, entryID: "anthropic",
			cred:      store.Credential{Schema: "claude-code/oauth-file@1", Payload: claude},
			wantEntry: `{"type":"oauth","access":"claude-access","refresh":"claude-refresh","expires":1900000000000}`,
		},
		{
			name: "muse", harness: "muse", provider: store.ProviderMetaMuse, kind: store.KindAPIKey, entryID: "meta",
			cred:      store.Credential{Schema: "muse/auth-file@1", Payload: muse},
			wantEntry: `{"type":"api_key","key":"muse-key"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			login, refusal := convert(context.Background(), store.Entry{Metadata: store.Metadata{ID: "source", Harness: tc.harness, Label: "Saved"}, Credential: tc.cred}, []string{"github.com/org/name"})
			if refusal != nil {
				t.Fatalf("convert refused the sign-in: %+v", *refusal)
			}
			if login.Provider != tc.provider || login.Kind != tc.kind || login.EntryID != tc.entryID {
				t.Fatalf("converted to %s/%s/%s", login.Provider, login.Kind, login.EntryID)
			}
			if !reflect.DeepEqual(login.Repos, []string{"github.com/org/name"}) {
				t.Fatalf("repos = %v", login.Repos)
			}
			var want, got any
			if json.Unmarshal([]byte(tc.wantEntry), &want) != nil || json.Unmarshal(login.Entry, &got) != nil {
				t.Fatalf("entry is not JSON: %s", login.Entry)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("entry = %s, want %s", login.Entry, tc.wantEntry)
			}
		})
	}
}

func jsonInt(v int64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// TestConvertReadsOpenCodeKeys covers the saved key cases that read OpenCode's
// own database instead of the credential payload.
func TestConvertReadsOpenCodeKeys(t *testing.T) {
	cases := []struct {
		name        string
		integration string
		provider    store.Provider
		entryID     string
	}{
		{name: "go", integration: "opencode-go", provider: store.ProviderOpenCodeGo, entryID: "opencode-go"},
		{name: "zen", integration: "opencode", provider: store.ProviderOpenCodeGo, entryID: "opencode"},
		{name: "zai", integration: "zai", provider: store.ProviderZAI, entryID: "zai"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cred := openCodeCredential(t, tc.integration, `{"type":"key","key":"saved-key"}`)
			login, refusal := convert(context.Background(), store.Entry{Metadata: store.Metadata{ID: "source", Harness: "opencode"}, Credential: cred}, nil)
			if refusal != nil {
				t.Fatalf("convert refused the key: %+v", *refusal)
			}
			if login.Provider != tc.provider || login.Kind != store.KindAPIKey || login.EntryID != tc.entryID {
				t.Fatalf("converted to %s/%s/%s", login.Provider, login.Kind, login.EntryID)
			}
			if string(login.Entry) != `{"type":"api_key","key":"saved-key"}` {
				t.Fatalf("entry = %s", login.Entry)
			}
		})
	}
}

// TestConvertRefusesWhatPiCannotTake keeps an incomplete or unsupported saved
// sign-in in place with a typed reason.
func TestConvertRefusesWhatPiCannotTake(t *testing.T) {
	museWithoutKey, _ := json.Marshal(map[string]json.RawMessage{"metaCredential": json.RawMessage(`{"email":"a@example.com"}`)})
	claudeWithoutScope, _ := json.Marshal(struct {
		Credentials map[string]struct {
			AccessToken  string   `json:"accessToken"`
			RefreshToken string   `json:"refreshToken"`
			ExpiresAt    int64    `json:"expiresAt"`
			Scopes       []string `json:"scopes"`
		} `json:"credentials"`
	}{Credentials: map[string]struct {
		AccessToken  string   `json:"accessToken"`
		RefreshToken string   `json:"refreshToken"`
		ExpiresAt    int64    `json:"expiresAt"`
		Scopes       []string `json:"scopes"`
	}{"claudeAiOauth": {AccessToken: "a", RefreshToken: "r", ExpiresAt: 1_900_000_000_000, Scopes: []string{"user:profile"}}}})
	cases := []struct {
		name    string
		harness string
		cred    store.Credential
		reason  Reason
	}{
		{name: "muse without a key", harness: "muse", cred: store.Credential{Schema: "muse/auth-file@1", Payload: museWithoutKey}, reason: ReasonNoAPIKey},
		{name: "unknown key type", harness: "opencode", cred: openCodeCredential(t, "opencode-go", `{"type":"oauth","key":"x"}`), reason: ReasonNoAPIKey},
		{name: "claude without inference", harness: "claude-code", cred: store.Credential{Schema: "claude-code/oauth-file@1", Payload: claudeWithoutScope}, reason: ReasonUnreadable},
		{name: "unreadable codex", harness: "codex", cred: store.Credential{Schema: "codex/auth-file@1", Payload: json.RawMessage(`{}`)}, reason: ReasonUnreadable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, refusal := convert(context.Background(), store.Entry{Metadata: store.Metadata{ID: "source", Harness: tc.harness}, Credential: tc.cred}, nil)
			if refusal == nil || refusal.Reason != tc.reason {
				t.Fatalf("refusal = %+v, want %s", refusal, tc.reason)
			}
		})
	}
}

// TestImportableNamesTheMovedHarnesses keeps an unknown harness from silently
// importing nothing.
func TestImportableNamesTheMovedHarnesses(t *testing.T) {
	for _, harness := range []string{"claude-code", "codex", "kimi-code", "grok", "muse", "opencode", "antigravity"} {
		if !Importable(harness) {
			t.Fatalf("%s is not importable", harness)
		}
	}
	if Importable("antigravity-future") || Importable("pi") {
		t.Fatal("an unknown harness is importable")
	}
}
