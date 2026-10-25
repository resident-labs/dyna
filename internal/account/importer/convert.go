package importer

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"dyna-agent/internal/account/store"
	_ "modernc.org/sqlite"
)

// The Pi provider IDs and auth entry fields below are Pi 1.4.0's. Its OAuth
// flows live in pi-ai/dist/auth/oauth and its API-key providers in
// pi-ai/dist/providers under the installed Pi agent directory.
const (
	piProviderAnthropic  = "anthropic"
	piProviderKimi       = "kimi-coding"
	piProviderXAI        = "xai"
	piProviderMeta       = "meta"
	piProviderOpenCodeGo = "opencode-go"
	piProviderOpenCode   = "opencode"
	piProviderOpenAI     = "openai"
	piProviderDeepSeek   = "deepseek"
	piProviderZAI        = "zai"
)

// grokSlot names Grok's saved OAuth client. Native Grok keys its auth file by
// issuer::client, and the entry must name this issuer and client to refresh.
const (
	grokIssuer = "https://auth.x.ai"
	grokClient = "b1a00492-073a-47ea-816f-4c329264a828"
)

// piOAuthEntry is Pi's saved OAuth credential. AccountID is only for Codex.
type piOAuthEntry struct {
	Type      string `json:"type"`
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"`
	AccountID string `json:"accountId,omitempty"`
}

// piKeyEntry is Pi's saved API-key credential.
type piKeyEntry struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

// piSignIn is one converted sign-in: the Pi provider entry and account fields.
type piSignIn struct {
	Provider store.Provider
	Kind     store.Kind
	EntryID  string
	Entry    json.RawMessage
	Repos    []string
}

// Importable reports whether the importer has a tested Pi mapping for harness.
// Antigravity is named here so its rows are refused instead of ignored.
func Importable(harness string) bool {
	switch harness {
	case "claude-code", "kimi-code", "grok", "muse", "opencode", "antigravity":
		return true
	}
	return false
}

// convert maps one saved native credential to its Pi provider entry. It never
// returns a partial entry: a sign-in it cannot read is refused with a reason.
func convert(ctx context.Context, source store.Entry, repos []string) (piSignIn, *Refused) {
	refused := func(reason Reason) (piSignIn, *Refused) {
		return piSignIn{}, &Refused{Source: source.ID, Label: source.Label, Reason: reason}
	}
	var login piSignIn
	var err error
	switch source.Harness {
	case "claude-code":
		login, err = convertClaude(source.Credential)
	case "kimi-code":
		login, err = convertKimi(source.Credential)
	case "grok":
		login, err = convertGrok(source.Credential)
	case "muse":
		login, err = convertMuse(source.Credential)
	case "opencode":
		login, err = convertOpenCode(ctx, source.Credential)
	default:
		return refused(ReasonUnsupportedHarness)
	}
	if errors.Is(err, errNoKey) {
		return refused(ReasonNoAPIKey)
	}
	if err != nil {
		return refused(ReasonUnreadable)
	}
	if source.Provider != "" && source.Provider != login.Provider {
		return refused(ReasonUnsupportedHarness)
	}
	if err := checkEntry(login.Entry); err != nil {
		return refused(ReasonUnreadable)
	}
	login.Repos = repos
	return login, nil
}

// convertClaude maps Claude Code's saved OAuth file to Pi's anthropic provider.
// Pi and Claude Code use the same client ID and token endpoint, so the saved
// access and refresh tokens refresh through Pi unchanged.
func convertClaude(c store.Credential) (piSignIn, error) {
	if c.Schema != "claude-code/oauth-file@1" {
		return piSignIn{}, errUnreadable
	}
	var payload struct {
		Credentials map[string]struct {
			AccessToken  string   `json:"accessToken"`
			RefreshToken string   `json:"refreshToken"`
			ExpiresAt    int64    `json:"expiresAt"`
			Scopes       []string `json:"scopes"`
		} `json:"credentials"`
	}
	if json.Unmarshal(c.Payload, &payload) != nil {
		return piSignIn{}, errUnreadable
	}
	tokens, ok := payload.Credentials["claudeAiOauth"]
	if !ok || tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.ExpiresAt <= 0 || !inferenceScope(tokens.Scopes) {
		return piSignIn{}, errUnreadable
	}
	return oauthLogin(store.ProviderAnthropic, piProviderAnthropic, tokens.AccessToken, tokens.RefreshToken, tokens.ExpiresAt, "")
}

// inferenceScope reports whether the saved Claude sign-in may run a model.
func inferenceScope(scopes []string) bool {
	for _, scope := range scopes {
		if scope == "user:inference" {
			return true
		}
	}
	return false
}

// convertKimi maps Kimi Code's saved token to Pi's kimi-coding provider. Kimi
// stores an epoch-seconds expiry, and Pi stores epoch milliseconds.
func convertKimi(c store.Credential) (piSignIn, error) {
	if c.Schema != "kimi-code/native-files@1" {
		return piSignIn{}, errUnreadable
	}
	var files map[string]string
	if json.Unmarshal(c.Payload, &files) != nil {
		return piSignIn{}, errUnreadable
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresAt    int64  `json:"expires_at"`
	}
	found := false
	for name, body := range files {
		if name == "config.toml" || !strings.HasSuffix(name, ".json") {
			continue
		}
		if found || json.Unmarshal([]byte(body), &token) != nil {
			return piSignIn{}, errUnreadable
		}
		found = true
	}
	if !found || token.AccessToken == "" || token.RefreshToken == "" || token.ExpiresAt <= 0 {
		return piSignIn{}, errUnreadable
	}
	return oauthLogin(store.ProviderKimiCode, piProviderKimi, token.AccessToken, token.RefreshToken, token.ExpiresAt*1000, "")
}

// convertGrok maps Grok's saved subscription login to Pi's xai provider. The
// saved entry keys its auth file by issuer::client, so only the entry that
// names Grok's OAuth client is used.
func convertGrok(c store.Credential) (piSignIn, error) {
	if c.Schema != "grok/native-files@1" {
		return piSignIn{}, errUnreadable
	}
	var files struct {
		Auth map[string]struct {
			Key       string    `json:"key"`
			Refresh   string    `json:"refresh_token"`
			ExpiresAt time.Time `json:"expires_at"`
			Mode      string    `json:"auth_mode"`
			Issuer    string    `json:"oidc_issuer"`
			ClientID  string    `json:"oidc_client_id"`
		} `json:"auth.json"`
	}
	if json.Unmarshal(c.Payload, &files) != nil {
		return piSignIn{}, errUnreadable
	}
	session, ok := files.Auth[grokIssuer+"::"+grokClient]
	if !ok || session.Mode != "oidc" || session.Issuer != grokIssuer || session.ClientID != grokClient {
		return piSignIn{}, errUnreadable
	}
	if session.Key == "" || session.Refresh == "" || session.ExpiresAt.IsZero() {
		return piSignIn{}, errUnreadable
	}
	return oauthLogin(store.ProviderXAI, piProviderXAI, session.Key, session.Refresh, session.ExpiresAt.UnixMilli(), "")
}

// convertMuse maps Muse's saved Meta credential to Pi's meta provider. Muse
// saves an API key and no expiry, so the Pi entry is a key and carries none.
func convertMuse(c store.Credential) (piSignIn, error) {
	if c.Schema != "muse/auth-file@1" {
		return piSignIn{}, errUnreadable
	}
	var payload struct {
		Meta struct {
			APIKey string `json:"api_key"`
		} `json:"metaCredential"`
	}
	if json.Unmarshal(c.Payload, &payload) != nil {
		return piSignIn{}, errUnreadable
	}
	if payload.Meta.APIKey == "" {
		return piSignIn{}, errNoKey
	}
	return keyLogin(store.ProviderMetaMuse, piProviderMeta, payload.Meta.APIKey)
}

// errNoKey marks a saved key account whose key is missing.
var errNoKey = errors.New("no saved key")

// errUnreadable marks a saved credential this release cannot decode.
var errUnreadable = errors.New("unreadable credential")

// openCodeIntegrations names the Pi provider that takes each OpenCode key.
// OpenCode Go and OpenCode Zen have separate Pi providers.
var openCodeIntegrations = map[string]struct {
	Provider store.Provider
	EntryID  string
}{
	"opencode-go": {store.ProviderOpenCodeGo, piProviderOpenCodeGo},
	"opencode":    {store.ProviderOpenCodeGo, piProviderOpenCode},
	"":            {store.ProviderOpenCodeGo, piProviderOpenCode},
	"anthropic":   {store.ProviderAnthropic, piProviderAnthropic},
	"openai":      {store.ProviderChatGPT, piProviderOpenAI},
	"xai":         {store.ProviderXAI, piProviderXAI},
	"deepseek":    {store.ProviderDeepSeek, piProviderDeepSeek},
	"zai":         {store.ProviderZAI, piProviderZAI},
}

// convertOpenCode reads an OpenCode account's saved key from its own database
// and maps it to the Pi provider that takes it.
func convertOpenCode(ctx context.Context, c store.Credential) (piSignIn, error) {
	if c.Schema != "opencode/v2-ref@1" {
		return piSignIn{}, errUnreadable
	}
	var payload struct {
		Ref struct {
			Integration  string `json:"integration"`
			CredentialID string `json:"credentialID"`
			Scope        struct {
				Database string `json:"database"`
			} `json:"scope"`
		} `json:"ref"`
	}
	if json.Unmarshal(c.Payload, &payload) != nil {
		return piSignIn{}, errUnreadable
	}
	target, ok := openCodeIntegrations[payload.Ref.Integration]
	if !ok || payload.Ref.CredentialID == "" || payload.Ref.Scope.Database == "" {
		return piSignIn{}, errUnreadable
	}
	key, err := readOpenCodeKey(ctx, payload.Ref.Scope.Database, payload.Ref.Integration, payload.Ref.CredentialID)
	if err != nil {
		return piSignIn{}, err
	}
	return keyLogin(target.Provider, target.EntryID, key)
}

// readOpenCodeKey reads one credential value from OpenCode's own database,
// read-only. Only a saved API key moves; an OAuth value is refused, because the
// Pi providers Dyna maps are API-key only.
func readOpenCodeKey(ctx context.Context, path, integration, id string) (string, error) {
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return "", errUnreadable
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var value string
	err = db.QueryRowContext(ctx, "SELECT value FROM credential WHERE id = ? AND integration_id = ?", id, integration).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errNoKey
	}
	if err != nil {
		return "", errUnreadable
	}
	var credential struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if json.Unmarshal([]byte(value), &credential) != nil || credential.Type != "key" || credential.Key == "" {
		return "", errNoKey
	}
	return credential.Key, nil
}

// oauthLogin builds a Pi OAuth entry.
func oauthLogin(provider store.Provider, entryID, access, refresh string, expires int64, accountID string) (piSignIn, error) {
	if access == "" || refresh == "" || expires <= 0 {
		return piSignIn{}, errUnreadable
	}
	entry, err := json.Marshal(piOAuthEntry{Type: "oauth", Access: access, Refresh: refresh, Expires: expires, AccountID: accountID})
	if err != nil {
		return piSignIn{}, errUnreadable
	}
	return piSignIn{Provider: provider, Kind: store.KindOAuth, EntryID: entryID, Entry: entry}, nil
}

// keyLogin builds a Pi API-key entry.
func keyLogin(provider store.Provider, entryID, key string) (piSignIn, error) {
	if key == "" || strings.ContainsAny(key, "\r\n\x00") {
		return piSignIn{}, errNoKey
	}
	entry, err := json.Marshal(piKeyEntry{Type: "api_key", Key: key})
	if err != nil {
		return piSignIn{}, errUnreadable
	}
	return piSignIn{Provider: provider, Kind: store.KindAPIKey, EntryID: entryID, Entry: entry}, nil
}

// jwtExpiryMillis reads the exp claim of a JWT access token as epoch
// milliseconds.
func jwtExpiryMillis(token string) (int64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, errUnreadable
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, errUnreadable
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp <= 0 {
		return 0, errUnreadable
	}
	return claims.Exp * 1000, nil
}

// checkEntry rejects an entry that does not hold the fields Pi needs.
func checkEntry(entry json.RawMessage) error {
	var parsed struct {
		Type    string `json:"type"`
		Access  string `json:"access"`
		Refresh string `json:"refresh"`
		Expires int64  `json:"expires"`
		Key     string `json:"key"`
	}
	if json.Unmarshal(entry, &parsed) != nil {
		return errUnreadable
	}
	switch parsed.Type {
	case "oauth":
		if parsed.Access == "" || parsed.Refresh == "" || parsed.Expires <= 0 {
			return fmt.Errorf("%w: oauth entry is incomplete", errUnreadable)
		}
	case "api_key":
		if parsed.Key == "" {
			return fmt.Errorf("%w: key entry is empty", errUnreadable)
		}
	default:
		return fmt.Errorf("%w: unknown entry type", errUnreadable)
	}
	return nil
}
