package connect

import (
	"context"
	"time"
)

type EpochStore interface {
	ConsumeConnectNonce(context.Context, int64, string, time.Time, time.Time) error
}

// Verifier validates signed relay requests and atomically consumes their nonce.
type Verifier struct {
	Relay   string
	Machine string
	Keys    KeySet
	Store   EpochStore
}

func (v *Verifier) Verify(ctx context.Context, token, op string, now time.Time) (RelayClaims, error) {
	c, err := VerifyRelay(token, op, v.Relay, v.Machine, v.Keys, now)
	if err != nil {
		return c, err
	}
	err = v.Store.ConsumeConnectNonce(ctx, c.Epoch, c.Nonce, now, time.Unix(c.Exp, 0).Add(time.Minute))
	return c, err
}
