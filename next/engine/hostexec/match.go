// Package hostexec matches host command grants without interpreting shell strings.
package hostexec

import (
	"errors"
	"github.com/resident-labs/dyna/contract"
	"time"
)

var ErrNoGrant = errors.New("no-grant")

func Match(grants []contract.Grant, host contract.HostName, argv []string, now time.Time) (contract.Grant, error) {
	var matched contract.Grant
	if len(argv) == 0 {
		return matched, ErrNoGrant
	}
	for _, grant := range grants {
		if grant.Kind != contract.GrantHostExec || !grant.Revoked.IsZero() || (!grant.Expires.IsZero() && !grant.Expires.After(now)) {
			continue
		}
		hostMatch := false
		for _, h := range grant.Hosts {
			if h == host || h == contract.AllHosts {
				hostMatch = true
				break
			}
		}
		if !hostMatch {
			continue
		}
		for _, rule := range grant.Commands {
			if rule.Program != "*" && rule.Program != argv[0] || len(argv)-1 < len(rule.Args) || !rule.Prefix && len(argv)-1 != len(rule.Args) {
				continue
			}
			ok := true
			for i, arg := range rule.Args {
				if argv[i+1] != arg {
					ok = false
					break
				}
			}
			if ok {
				matched = grant
				break
			}
		}
		if matched.ID != "" && matched.Mode == contract.GrantModePreapproved {
			return matched, nil
		}
	}
	if matched.ID == "" {
		return matched, ErrNoGrant
	}
	return matched, nil
}

func Class(grant contract.Grant) contract.Class {
	if grant.Mode == contract.GrantModePreapproved {
		return contract.ClassAuto
	}
	return contract.ClassOwner
}
