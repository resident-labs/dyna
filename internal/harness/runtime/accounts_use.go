package runtime

import (
	"context"
	"errors"

	"dyna-agent/internal/account/store"
)

// errAccountNoProvider is the refusal of an account that names no provider: a
// selection is stored for the provider, so such an account cannot be selected.
var errAccountNoProvider = errors.New("This account names no provider. Sign in to it again.")

// SelectInScope records that the account's provider uses n in the room roomID.
// A room row changes only that room: the global row and every other room stay,
// and turns of another room keep their own account.
func (a *Accounts) SelectInScope(n NativeAccount, roomID string) error {
	if roomID == "" {
		return errors.New("A room selection needs a room.")
	}
	provider := SelectionProvider(n.Harness, n.Provider)
	if provider == "" {
		return errAccountNoProvider
	}
	return (&store.Store{Root: a.Root}).SetActive(provider, n.Harness, roomID, n.ID)
}

// UseAccount makes an account the one its provider uses in scope. An empty
// scope is the global row: a harness that keeps its credentials in a shared
// service switches at once, and a harness that pins them per account only
// records the choice. Any other scope is a room ID, and needs a harness that
// pins credentials per account, because a room selection is read from the
// account's own home at turn launch.
//
// It returns no snapshot: building one reads vendor metadata over the network,
// and its only caller re-reads the accounts view it shows instead.
func (r *Runtime) UseAccount(ctx context.Context, id, scope string) error {
	n, err := r.Accounts.Find(id)
	if err != nil {
		return err
	}
	if n.Status != "ready" {
		return errors.New("Sign in to this account before using it.")
	}
	if scope != "" {
		if !pinsPerAccount(n.Harness) {
			return errors.New("This harness keeps one shared sign-in, so a room cannot choose its own account. Choose a global account.")
		}
		return r.Accounts.SelectInScope(n, scope)
	}
	b, err := r.Bundles.Resolve(n.Harness, "")
	if err != nil {
		return err
	}
	if err := r.Accounts.SwitchIfIdle(ctx, n.ID, b.Executable); err != nil {
		return err
	}
	return r.Regenerate(n.Harness)
}
