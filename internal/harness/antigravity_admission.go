package harness

import (
	"context"
	"encoding/json"
	"io"

	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
)

type accountTurnLease interface {
	io.Closer
	Bind(json.RawMessage) error
}

// idleAccountLease keeps crash protection while a turn runs, but releases the
// account when normal turn cleanup follows a settled or stopped native child.
// The child's bundle and generation leases still protect its retained process.
type idleAccountLease struct{ *harnessruntime.ResourceLease }

func (l idleAccountLease) Close() error { return l.ReleaseIdle() }

func prepareAccountTurnLease(ctx context.Context, p profile.Profile) (accountTurnLease, error) {
	lease, err := harnessruntime.NewAccounts().TurnLeaseManaged(ctx, p.Harness)
	if err != nil {
		return nil, err
	}
	if p.Harness == profile.HarnessAntigravity {
		return idleAccountLease{lease}, nil
	}
	return lease, nil
}
