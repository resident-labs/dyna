// Package private holds engine-only account storage data. It is not part of the browser API.
package private

import "github.com/resident-labs/dyna/contract"

// AccountBinding joins an owner-visible account to its private proxy identity.
type AccountBinding struct {
	Account contract.Account
	ProxyID string
}

// ProxyAuth is an opaque credential record interpreted only by the proxy.
type ProxyAuth struct {
	ID        string
	Provider  string
	Record    []byte
	UpdatedAt int64
	Bound     bool
}
