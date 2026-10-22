package engine

import (
	"context"
	"errors"
	"fmt"
	"log"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/websvc"
)

// pin pins a post for its caller (requirements 2 and 3). It keeps the app's
// two arguments: who pinned it comes from the caller, not the request.
func (s services) pin(ctx context.Context, roomRef, msgID string) error {
	caller, local := websvc.CallerFromContext(ctx)
	address := func(id string) (string, error) {
		info, err := s.rooms.Notify.Sessions.Store.Info(id)
		if err != nil {
			return "", err
		}
		if info.Address() == "" {
			return "", errors.New("this session has no address")
		}
		return info.Address(), nil
	}
	by, err := pinnedBy(caller, local, authority.Of(ctx), enforceCallers, log.Printf, address)
	if err != nil {
		return err
	}
	return s.rooms.Pin(roomRef, msgID, by)
}

// pinnedBy is who a pin records for its caller: a session's address, "run
// <id> agent <n>" for a workflow worker, "" (the room owner) for the owner,
// or "unverified" (see recordedCaller).
func pinnedBy(caller websvc.Caller, local bool, actor authority.Actor, enforce bool, logf func(string, ...any), address func(session string) (string, error)) (string, error) {
	as, ok, err := recordedCaller(caller, local, actor, enforce, logf, "pinning a post")
	switch {
	case err != nil:
		return "", err
	case !ok:
		return unverified, nil
	case as.Run != "":
		return workerName(as), nil
	case as.Session != "":
		return address(as.Session)
	}
	return "", nil
}

// unverified is how a record names a caller the engine could not verify.
// Such a caller is never the owner.
const unverified = "unverified"

// recordedCaller is the claim that the caller proves (see verified), for a
// record that names it, such as a pin's pinner or a room's creator. A caller
// the engine could not verify is refused once callers are enforced. Until
// then, recordedCaller logs what it is doing and returns ok false, and the
// record names it unverified.
func recordedCaller(caller websvc.Caller, local bool, actor authority.Actor, enforce bool, logf func(string, ...any), doing string) (as notify.Claim, ok bool, err error) {
	if as, ok = verified(caller, local, actor); ok {
		return as, true, nil
	}
	if enforce {
		return notify.Claim{}, false, fmt.Errorf("the engine could not verify who is %s", doing)
	}
	logf("report-only: an unverified caller is %s; recorded as unverified", doing)
	return notify.Claim{}, false, nil
}

// workerName names workflow worker c in a record.
func workerName(c notify.Claim) string { return fmt.Sprintf("run %s agent %d", c.Run, c.Agent) }
