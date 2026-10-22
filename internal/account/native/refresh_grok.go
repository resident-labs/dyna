package native

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RefreshGrokSaved renews a validated saved OIDC grant. The caller must persist
// the result before using it, through the shared account refresh transaction.
func RefreshGrokSaved(ctx context.Context, c Captured) (Captured, error) {
	return refreshGrokSaved(ctx, c, nil, time.Now().UTC())
}

func refreshGrokSaved(ctx context.Context, c Captured, client claudeHTTPClient, now time.Time) (Captured, error) {
	login, err := grokSavedSession(c)
	if err != nil {
		return c, err
	}
	if login.Refresh == "" {
		return c, ErrUnsupported
	}
	if !login.ExpiresAt.IsZero() && login.ExpiresAt.After(now.Add(5*time.Minute)) {
		return c, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {login.Refresh}, "client_id": {grokOIDCClient}}
	// Grok 1.0.62 uses OIDC discovery. This endpoint is advertised by the fixed
	// issuer https://auth.x.ai/.well-known/openid-configuration.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, grokOIDCIssuer+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return c, ErrSchema
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return c, ctx.Err()
		}
		return c, errors.New("Grok sign-in could not be refreshed.")
	}
	defer resp.Body.Close()
	if err := directStatusError(resp, now); err != nil {
		return c, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return c, ErrSchema
	}
	var out struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int64  `json:"expires_in"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return c, ErrSchema
	}
	if out.Error == "invalid_grant" {
		return c, ErrInvalidGrant
	}
	if resp.StatusCode != http.StatusOK {
		return c, errors.New("Grok sign-in could not be refreshed.")
	}
	if out.Access == "" || strings.ContainsAny(out.Access, "\r\n\x00") || out.Expires <= 0 || out.Expires > 10*365*86400 {
		return c, ErrSchema
	}
	var files, auth, session map[string]json.RawMessage
	_ = json.Unmarshal(c.Credential.Payload, &files)
	_ = json.Unmarshal(files["auth.json"], &auth)
	slot := grokOIDCIssuer + "::" + grokOIDCClient
	_ = json.Unmarshal(auth[slot], &session)
	set := func(k string, v any) { session[k], _ = json.Marshal(v) }
	set("key", out.Access)
	if out.Refresh != "" {
		set("refresh_token", out.Refresh)
	}
	c.ExpiresAt = now.Add(time.Duration(out.Expires) * time.Second)
	set("expires_at", c.ExpiresAt.Format(time.RFC3339Nano))
	auth[slot], _ = json.Marshal(session)
	files["auth.json"], _ = json.Marshal(auth)
	c.Credential.Payload, _ = json.Marshal(files)
	return c, nil
}

// GrokRefreshSupported applies the same validation as the refresh operation.
func GrokRefreshSupported(c Captured) bool {
	login, err := grokSavedSession(c)
	return err == nil && login.Refresh != "" && !strings.ContainsAny(login.Refresh, "\r\n\x00")
}
