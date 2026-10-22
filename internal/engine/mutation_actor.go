package engine

import (
	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/websvc"
)

// mutationActor applies the shared caller policy to authorization and audit identity.
func mutationActor(caller websvc.Caller, local bool, actor authority.Actor, claim callspec.Claim, enforce bool, logf func(string, ...any), doing string) (authority.Actor, string, error) {
	as, ok, err := recordedCaller(caller, local, actor, enforce, logf, doing)
	if err != nil {
		return authority.Actor{}, "", err
	}
	// TODO: legacy/shim remove later — report-only callers use their claimed session for the old CLI guards while records say unverified; remove with caller enforcement (g3419).
	if !ok {
		if claim.Session != "" {
			actor = authority.Actor{Role: authority.Session, Session: claim.Session, Room: claim.Room}
			logf("report-only: an unverified caller uses claimed session %s for authorization while %s", claim.Session, doing)
		}
		return actor, unverified, nil
	}
	if as.Run != "" {
		return actor, workerName(as), nil
	}
	if as.Session != "" {
		return actor, as.Session, nil
	}
	return authority.Actor{Role: authority.Owner}, "owner", nil
}
