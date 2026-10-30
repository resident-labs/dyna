package service

import (
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
	"time"
)

// AccountsSnapshot is the account view of the Accounts API, grouped by the
// provider each account signed in to.
type AccountsSnapshot struct {
	// Scope is the room this snapshot was taken for. Empty means the global
	// selection only.
	Scope string `json:"scope,omitempty"`
	// Providers holds one group per provider, in provider order.
	Providers []ProviderAccounts `json:"providers" tstype:"ProviderAccounts[] | null"`
	// Problems names each saved row the list skipped.
	Problems  []runtime.AccountProblem `json:"problems" tstype:"AccountProblem[] | null"`
	CheckedAt time.Time                `json:"checkedAt"`
	Message   string                   `json:"message,omitempty"`
}

// ProviderAccounts is every listed account of one provider.
type ProviderAccounts struct {
	Provider store.Provider `json:"provider" tstype:"Provider | ''"`
	Accounts []ScopeAccount `json:"accounts" tstype:"ScopeAccount[] | null"`
}

// ScopeAccount is one account with its place in the requested scope.
type ScopeAccount struct {
	Account `tstype:",extends"`
	// ActiveHere reports whether the account is the one its provider uses in the
	// snapshot's scope: the room row when the scope is a room, else the global
	// row. Active reports the global row.
	ActiveHere bool `json:"activeHere"`
}
