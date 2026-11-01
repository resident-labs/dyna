package auth

import (
	"context"
	"encoding/base64"
	"time"
)

type dynaPinnedContextKey struct{}

// WithDynaPinnedAuth captures the session's chosen credential from the engine's
// private header, which the engine's route always replaces, so a box cannot set it.
func WithDynaPinnedAuth(ctx context.Context, values []string) context.Context {
	if len(values) == 0 {
		return ctx
	}
	decoded, err := base64.RawURLEncoding.DecodeString(values[0])
	if err != nil || len(decoded) == 0 {
		return ctx
	}
	return context.WithValue(ctx, dynaPinnedContextKey{}, string(decoded))
}

// dynaPinnedAuth returns the session's chosen credential when it can serve this
// attempt. available has already dropped cooled, disallowed and already tried
// credentials, so this is a preference: upstream's PinnedAuthMetadataKey filters every
// other candidate out, and a 429 on the pin would then fail the turn. Nil means
// Automatic. A pin outranks gates, which are for automatic picks, but not a spent reading.
func dynaPinnedAuth(ctx context.Context, available []*Auth, model string, now time.Time) *Auth {
	if ctx == nil {
		return nil
	}
	id, _ := ctx.Value(dynaPinnedContextKey{}).(string)
	if id == "" {
		return nil
	}
	board := soonestResetBoardFrom(ctx)
	for _, auth := range available {
		if auth == nil || auth.ID != id {
			continue
		}
		hint, known := board.hint(auth)
		if rankSoonestReset(auth, hint, known, model, now, soonestResetPolicy{}).spent {
			return nil
		}
		return auth
	}
	return nil
}
