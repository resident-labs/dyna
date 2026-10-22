package callerid

import (
	"errors"
	"os"
)

// Self resolves this process with the engine's rules, so a launch outside
// the engine records the identity the engine would give its launcher, never
// one from the environment. A refused process cannot launch for anyone.
func Self(dataDir string) (Identity, error) {
	s, err := OpenStore(dataDir)
	if err != nil {
		return Identity{}, err
	}
	return resolveLauncher("/proc", enginePID(), s, os.Getpid())
}

// resolveLauncher applies the engine's rules to a launcher. Without an
// engine PID, rule (c) cannot run, so an owner result is not confirmed and
// is refused; session and worker results come from records and stand.
func resolveLauncher(proc string, engine int, records Records, pid int) (Identity, error) {
	r, err := New(proc, engine, records)
	if err != nil {
		return Identity{}, err
	}
	id := r.Resolve(pid)
	if id.Kind == Owner && engine <= 0 {
		id = refused("the engine is not answering, so this command cannot be confirmed as the owner's; start the engine and try again")
	}
	if id.Kind == Refused {
		return id, errors.New(id.Reason)
	}
	return id, nil
}
