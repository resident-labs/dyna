// Package hostexec matches grants to hosts.
package hostexec

import (
	"errors"
	"github.com/resident-labs/dyna/contract"
	"time"
)

var ErrNoGrant = errors.New("no_grant")

func Match(grants []contract.Grant, host contract.HostName, now time.Time) (contract.Grant, error) {
	for _, grant := range grants {
		if grant.Kind != contract.GrantHostExec || !grant.Revoked.IsZero() {
			continue
		}
		for _, h := range grant.Hosts {
			if h == host || h == contract.AllHosts {
				return grant, nil
			}
		}
	}
	return contract.Grant{}, ErrNoGrant
}
