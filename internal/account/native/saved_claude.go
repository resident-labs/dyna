package native

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"dyna-agent/internal/account/store"
)

func (m *claudeManager) CachedPlan(c Captured) *store.Plan {
	p, _, id, _, err := claudeDecode(c.Credential)
	if err != nil || id != c.Identity {
		return nil
	}
	var native struct {
		SubscriptionType string `json:"subscriptionType"`
		RateLimitTier    string `json:"rateLimitTier"`
	}
	if json.Unmarshal(p.Credentials["claudeAiOauth"], &native) != nil {
		return nil
	}
	// The tier (max_20x) says what a percentage of this plan is worth; the
	// subscription type is the coarser fallback (max).
	if tier := strings.TrimSpace(native.RateLimitTier); tier != "" {
		return &store.Plan{ID: tier, Label: tier}
	}
	if native.SubscriptionType == "" {
		return nil
	}
	return &store.Plan{ID: native.SubscriptionType, Label: native.SubscriptionType}
}

// ProbeSaved reads the supplied saved grant without refreshing or reading native files.
func (m *claudeManager) ProbeSaved(ctx context.Context, c Captured) (store.Observation, error) {
	var o store.Observation
	if err := ctx.Err(); err != nil {
		return o, err
	}
	_, tok, id, _, err := claudeDecode(c.Credential)
	if err != nil {
		return o, err
	}
	if id != c.Identity {
		return o, ErrNoIdentity
	}
	now := directNow(m.now)
	if !time.UnixMilli(tok.ExpiresAt).After(now.Add(time.Minute)) {
		return o, ErrInvalidGrant
	}
	endpoint := m.usageURL
	if endpoint == "" {
		endpoint = claudeUsageURL
	}
	body, err := directRequest(ctx, m.client, http.MethodGet, endpoint, "", map[string]string{"Authorization": "Bearer " + tok.AccessToken, "anthropic-beta": "oauth-2025-04-20", "Accept": "application/json", "User-Agent": claudeUserAgent}, now)
	if err != nil {
		return o, err
	}
	windows, err := DecodeClaudeUsage(body)
	if err != nil {
		return o, err
	}
	return store.Observation{Plan: m.CachedPlan(c), Windows: windows, ObservedAt: now}, nil
}
