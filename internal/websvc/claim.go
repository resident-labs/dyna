package websvc

import (
	"context"
	"encoding/json"
	"fmt"

	"dyna-agent/internal/callspec"
)

// TODO: legacy/shim remove later — only an unverified local caller can supply its old CLI guard; verified callers and remote clients ignore the header; remove with caller enforcement (g3419).
func unverifiedClaim(ctx context.Context, caller Caller, raw string) (context.Context, error) {
	if caller.Kind != Unverified || raw == "" {
		return ctx, nil
	}
	var claim callspec.Claim
	if err := json.Unmarshal([]byte(raw), &claim); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", callspec.ClaimHeader, err)
	}
	return callspec.WithClaim(ctx, claim), nil
}
