package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const kimiCodeOAuthClientID = "17e5f671-d194-4dfb-9706-5516cb48c098"

// SupportsCredentialRefresh limits renewal to a validated managed OAuth login.
func (*kimiCodeManager) SupportsCredentialRefresh(c Captured) bool {
	_, err := unpackKimiCodeLogin(c.Credential)
	return err == nil
}

// Refresh renews a saved native Kimi grant without changing its selection.
// The caller checkpoints the returned credential before any native write.
func (m *kimiCodeManager) Refresh(ctx context.Context, c Captured) (Captured, error) {
	if err := ctx.Err(); err != nil {
		return c, err
	}
	parsed, err := credentialMetadata(c.Credential)
	if err != nil {
		return Captured{}, err
	}
	if parsed.Identity != c.Identity || parsed.Integration != c.Integration || c.Integration != "" {
		return Captured{}, ErrNoIdentity
	}
	login, err := unpackKimiCodeLogin(c.Credential)
	if err != nil {
		return Captured{}, err
	}
	if login.expiresAt.Sub(directNow(m.now)) > 5*time.Minute {
		return c, nil
	}
	login, err = m.refreshLogin(ctx, login)
	if err != nil {
		return Captured{}, err
	}
	next, err := capturedKimiCode(login)
	if err != nil {
		return Captured{}, err
	}
	next.Label = c.Label
	return next, nil
}

// RefreshActive checkpoints a rotated grant before writing it to the native
// home. activate re-reads the native login under the account lock, so a newer
// peer or checkpointed grant is used instead of a stale refresh token.
func (m *kimiCodeManager) RefreshActive(ctx context.Context, c Captured, checkpoint func(Captured) error) (Captured, error) {
	if checkpoint == nil {
		return c, ErrUnsupported
	}
	parsed, err := credentialMetadata(c.Credential)
	if err != nil {
		return c, err
	}
	if parsed.Identity != c.Identity || parsed.Integration != c.Integration {
		return c, ErrNoIdentity
	}
	r := &activeRefresh{current: c, result: c, checkpoint: checkpoint}
	_, err = m.activate(ctx, c.Integration, c.Credential, r)
	return r.result, err
}

// RecoverCredential reads a newer native grant after a peer refresh or a
// failed native write. It never performs a refresh itself.
func (m *kimiCodeManager) RecoverCredential(ctx context.Context, c Captured) (Captured, error) {
	return recoverNative(ctx, m, c, time.Now(), nil)
}

func (m *kimiCodeManager) refreshLogin(ctx context.Context, login kimiCodeLogin) (kimiCodeLogin, error) {
	host := strings.TrimSuffix(login.oauthHost, "/")
	if host == "" {
		host = "https://auth.kimi.com"
	}
	if host != "https://auth.kimi.com" && host != "https://auth.kimi.ai" {
		return login, ErrUnsupported
	}
	endpoint := m.tokenURL
	if endpoint == "" {
		endpoint = host + "/api/oauth/token"
	}
	form := url.Values{
		"client_id":     {kimiCodeOAuthClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {login.tokenInfo.RefreshToken},
	}.Encode()
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewBufferString(form))
	if err != nil {
		return login, ErrUnsupported
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var client claudeHTTPClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if m.client != nil {
		client = m.client
	}
	resp, err := client.Do(req)
	if err != nil {
		return login, errors.New("Kimi Code sign-in could not be renewed.")
	}
	defer resp.Body.Close()
	now := directNow(m.now)
	if err := directStatusError(resp, now); err != nil {
		return login, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return login, ErrSchema
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest && kimiCodeInvalidRefreshGrant(raw) {
		return login, ErrInvalidGrant
	}
	if resp.StatusCode != http.StatusOK {
		return login, &UsageHTTPError{Status: resp.StatusCode}
	}
	var token kimiCodeToken
	if json.Unmarshal(raw, &token) != nil || token.ExpiresIn <= 0 {
		return login, ErrSchema
	}
	token.ExpiresAt = now.Unix() + token.ExpiresIn
	if token.TokenType == "" {
		token.TokenType = "Bearer"
	}
	rawToken, _ := json.Marshal(token)
	if _, err := parseKimiCodeToken(rawToken); err != nil {
		return login, err
	}
	login.token = rawToken
	login.tokenInfo = token
	login.expiresAt = time.Unix(token.ExpiresAt, 0).UTC()
	return login, nil
}

// Only an explicit grant failure requires a new login. Other 400 responses do
// not prove that the refresh grant expired.
func kimiCodeInvalidRefreshGrant(raw []byte) bool {
	var body struct {
		Error string `json:"error"`
	}
	return json.Unmarshal(raw, &body) == nil && body.Error == "invalid_grant"
}
