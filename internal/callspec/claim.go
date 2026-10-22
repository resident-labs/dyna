package callspec

import "context"

// TODO: legacy/shim remove later — carries the old CLI session guard across the engine call in report-only mode; remove this header with caller enforcement (g3419).
const ClaimHeader = "X-Dyna-Claim"

// TODO: legacy/shim remove later — identifies only the session and room used by the old CLI guards, never the recorded author; remove with caller enforcement (g3419).
type Claim struct{ Session, Room string }

// TODO: legacy/shim remove later — keys the temporary report-only guard claim; remove with caller enforcement (g3419).
type claimKey struct{}

// TODO: legacy/shim remove later — carries an unverified local caller's old CLI guard on the request context; remove with caller enforcement (g3419).
func WithClaim(ctx context.Context, claim Claim) context.Context {
	return context.WithValue(ctx, claimKey{}, claim)
}

// TODO: legacy/shim remove later — reads an unverified local caller's old CLI guard from the request context; remove with caller enforcement (g3419).
func ClaimOf(ctx context.Context) Claim {
	claim, _ := ctx.Value(claimKey{}).(Claim)
	return claim
}
