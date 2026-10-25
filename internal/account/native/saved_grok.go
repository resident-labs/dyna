package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"dyna-agent/internal/account/store"
)

const grokOIDCIssuer = "https://auth.x.ai"
const grokOIDCClient = "b1a00492-073a-47ea-816f-4c329264a828"

// ProbeGrokSaved follows Grok CLI 1.0.62's _x.ai/billing request. It reads
// a captured native file; it never runs the CLI or rotates native credentials.
func ProbeGrokSaved(ctx context.Context, c Captured) (store.Observation, error) {
	return probeGrokSaved(ctx, c, nil, time.Now().UTC())
}
func probeGrokSaved(ctx context.Context, c Captured, client claudeHTTPClient, now time.Time) (store.Observation, error) {
	var empty store.Observation
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	login, err := grokSavedSession(c)
	if err != nil {
		return empty, err
	}
	return probeGrokCredits(ctx, client, now, login.Key, login.UserID)
}

// probeGrokCredits follows the Grok CLI billing request with an access token
// and an optional user ID. It takes no harness credential, so any account of
// the provider can use it. It never rotates the token.
func probeGrokCredits(ctx context.Context, client claudeHTTPClient, now time.Time, key, userID string) (store.Observation, error) {
	headers := map[string]string{
		"Authorization": "Bearer " + key, "x-xai-token-auth": "xai-grok-cli",
		"x-grok-client-version": "1.0.62", "x-grok-client-mode": "interactive",
	}
	if userID != "" {
		headers["x-userid"] = userID
	}
	raw, err := directRequest(ctx, client, http.MethodGet, "https://cli-chat-proxy.grok.com/v1/billing?format=credits", "", headers, now)
	if err != nil {
		// A billing rejection does not establish that the refresh grant expired.
		// Only the credential refresh operation can return that sign-in decision.
		if errors.Is(err, ErrInvalidGrant) {
			return store.Observation{}, &UsageHTTPError{Status: http.StatusUnauthorized}
		}
		return store.Observation{}, err
	}
	return grokBillingObservation(raw, now)
}
func grokBillingObservation(raw []byte, now time.Time) (store.Observation, error) {
	var result struct {
		Config struct {
			Unified            *bool    `json:"isUnifiedBillingUser"`
			CreditUsagePercent *float64 `json:"creditUsagePercent"`
			Period             struct {
				Type  string `json:"type"`
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"currentPeriod"`
		} `json:"config"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return store.Observation{}, ErrSchema
	}
	if result.Config.Unified == nil {
		return store.Observation{}, ErrSchema
	}
	// isUnifiedBillingUser is the only reliable plan signal: the tier name
	// says "SuperGrok Plus" on both plans. An older plan bills monthly
	// credits; that schema is unverified, so Dyna names the plan and reads
	// no usage.
	if !*result.Config.Unified {
		return store.Observation{Plan: &store.Plan{ID: "grok-legacy", Label: "Legacy (monthly)"}, ObservedAt: now}, nil
	}
	p := result.Config.Period
	if p.Type != "USAGE_PERIOD_TYPE_WEEKLY" {
		return store.Observation{}, ErrUnsupported
	}
	start, err := time.Parse(time.RFC3339Nano, p.Start)
	if err != nil {
		return store.Observation{}, ErrSchema
	}
	end, err := time.Parse(time.RFC3339Nano, p.End)
	if err != nil || end.Sub(start) != 7*24*time.Hour {
		return store.Observation{}, ErrSchema
	}
	window := store.Window{ID: "grok-weekly", Label: "Weekly usage", Period: "week", PeriodStart: start.UTC().Format(time.RFC3339Nano), ResetsAt: end.UTC().Format(time.RFC3339Nano)}
	if percent := result.Config.CreditUsagePercent; percent != nil {
		if *percent < 0 || *percent > 100 {
			return store.Observation{}, ErrSchema
		}
		// Grok /usage show displays this value directly as percent used.
		window.UsedPercent, window.Unit = percent, "percent"
	} else {
		window.UnavailableReason = "Grok did not return a usage percentage."
	}
	return store.Observation{Plan: &store.Plan{ID: "x-subscription", Label: "X Subscription (weekly)"}, ObservedAt: now, Windows: []store.Window{window}}, nil

}

type grokSession struct {
	Email     string    `json:"email"`
	Key       string    `json:"key"`
	Refresh   string    `json:"refresh_token"`
	ExpiresAt time.Time `json:"expires_at"`
	Mode      string    `json:"auth_mode"`
	UserID    string    `json:"user_id"`
	Issuer    string    `json:"oidc_issuer"`
	ClientID  string    `json:"oidc_client_id"`
}

func (s grokSession) label() string {
	claims, _ := codexClaims(s.Key)
	return identityLabel(s.Email, jsonString(claims["tier"]))
}

func (s grokSession) identity() string {
	sum := sha256.Sum256([]byte(s.Issuer + "\x00" + s.UserID))
	return "grok:" + hex.EncodeToString(sum[:])
}

// GrokNativeIdentity identifies the native user without exposing its raw ID.
// It is not a cross-product seat binding.
func GrokNativeIdentity(c Captured) (string, error) {
	login, err := grokSavedSession(c)
	if err != nil {
		return "", err
	}
	return login.identity(), nil
}

func grokSavedSession(c Captured) (grokSession, error) {
	if c.Integration != "" || c.Credential.Schema != "grok/native-files@1" || len(c.Credential.Payload) > 4<<20 {
		return grokSession{}, ErrSchema
	}
	var files map[string]json.RawMessage
	if json.Unmarshal(c.Credential.Payload, &files) != nil {
		return grokSession{}, ErrSchema
	}
	var auth map[string]grokSession
	if json.Unmarshal(files["auth.json"], &auth) != nil {
		return grokSession{}, ErrSchema
	}
	login, ok := auth[grokOIDCIssuer+"::"+grokOIDCClient]
	if !ok || login.Mode != "oidc" || login.Issuer != grokOIDCIssuer || login.ClientID != grokOIDCClient {
		return grokSession{}, ErrUnsupported
	}
	for _, field := range []string{login.Key, login.UserID} {
		if field == "" || len(field) > 64<<10 || strings.ContainsAny(field, "\r\n\x00") {
			return grokSession{}, ErrSchema
		}
	}

	if c.Identity != "grok:native" && c.Identity != login.identity() {
		return grokSession{}, ErrNoIdentity
	}
	return login, nil
}
