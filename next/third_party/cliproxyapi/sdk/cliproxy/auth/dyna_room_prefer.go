package auth

import (
	"context"
	"encoding/base64"
	"strings"
	"time"
)

type dynaPreferredContextKey struct{}

// dynaAuthIDs decodes the engine's comma-separated, base64url credential IDs.
func dynaAuthIDs(values []string) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, value := range values {
		for _, id := range strings.Split(value, ",") {
			decoded, err := base64.RawURLEncoding.DecodeString(id)
			if err == nil && len(decoded) != 0 {
				ids[string(decoded)] = struct{}{}
			}
		}
	}
	return ids
}

// WithDynaPreferredAuth captures the credentials the session's room prefers from the
// engine's private header, which the engine's route always replaces, so a box cannot set it.
func WithDynaPreferredAuth(ctx context.Context, values []string) context.Context {
	return context.WithValue(ctx, dynaPreferredContextKey{}, dynaAuthIDs(values))
}

// dynaPreferredAuths narrows a pick to the room's preferred credentials that can take
// it: available has already dropped cooled, ineligible and already tried credentials, and
// this drops those spent or past a gate for model. A preference, unlike a pin, keeps the
// gates, so a nearly spent preferred account lets the room spill rather than stall. Nil
// means none is usable, and the pick runs over every candidate as before.
func dynaPreferredAuths(ctx context.Context, available []*Auth, model string, now time.Time) []*Auth {
	if ctx == nil {
		return nil
	}
	ids, _ := ctx.Value(dynaPreferredContextKey{}).(map[string]struct{})
	if len(ids) == 0 {
		return nil
	}
	board := soonestResetBoardFrom(ctx)
	var preferred []*Auth
	for _, auth := range available {
		if auth == nil {
			continue
		}
		if _, ok := ids[auth.ID]; !ok {
			continue
		}
		hint, known := board.hint(auth)
		if rank := rankSoonestReset(auth, hint, known, model, now, soonestResetGates); !rank.spent && !rank.gated {
			preferred = append(preferred, auth)
		}
	}
	return preferred
}
