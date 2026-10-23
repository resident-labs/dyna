package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	harnesshome "dyna-agent/internal/harness/home"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/profile"
)

type nativeTurnKey struct{}
type accountTurnLease interface {
	io.Closer
	Bind(json.RawMessage) error
}

// retainedAccountLease borrows the account protection held by an idle native
// child. A replacement cannot borrow it: its new reference must obtain a fresh
// account lease on a later turn.
type retainedAccountLease struct{ reference json.RawMessage }

func (l retainedAccountLease) Close() error { return nil }
func (l retainedAccountLease) Bind(reference json.RawMessage) error {
	if !bytes.Equal(reference, l.reference) {
		return errors.New("retained Antigravity process changed; retry the turn")
	}
	return nil
}

func prepareAccountTurnLease(ctx context.Context, p profile.Profile) (accountTurnLease, error) {
	accounts := harnessruntime.NewAccounts()
	if p.Harness == profile.HarnessAntigravity {
		key, _ := ctx.Value(nativeTurnKey{}).(string)
		if key == "" {
			var err error
			key, err = sessionKey(p, ExecutionOptions{})
			if err != nil {
				return nil, err
			}
		}
		scope, err := accounts.HarnessHome(p.Harness)
		if err != nil {
			return nil, err
		}
		home := harnesshome.Home(scope)
		launch := sessionhost.Launch{Harness: p.Harness, Key: key + "\x00" + home + "\x00" + home}
		ref, ok, err := sessionhost.RetainedReference(ctx, nativeSessionHost, launch)
		if err != nil {
			return nil, err
		}
		if ok {
			raw, err := ref.MarshalJSON()
			if err != nil {
				return nil, err
			}
			return retainedAccountLease{reference: raw}, nil
		}
	}
	return accounts.TurnLeaseManaged(ctx, p.Harness)
}
