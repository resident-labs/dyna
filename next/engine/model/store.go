package model

import (
	"context"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/private"
)

// AccountStore is the model route's narrow account persistence boundary.
type AccountStore interface {
	ListProxyAuths(context.Context) ([]private.ProxyAuth, error)
	Accounts(context.Context) ([]private.AccountBinding, error)
	BindAccount(context.Context, contract.AccountID, string, contract.AccountProvider, string) error
	RemoveAccount(context.Context, contract.AccountID) error
	PinnedAccounts(context.Context) ([]contract.AccountID, error)
}

// AuthStore stores proxy-owned opaque records without interpreting them.
type AuthStore interface {
	ListProxyAuths(context.Context) ([]private.ProxyAuth, error)
	SaveProxyAuth(context.Context, private.ProxyAuth, bool) error
	DeleteProxyAuth(context.Context, string) error
	PruneUnboundProxyAuths(context.Context) error
}
