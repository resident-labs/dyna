package subagent

import (
	"context"
	"os"
	"sync"
	"syscall"
)

type activeTurn struct {
	root, session string
	turn          int
}

// Embedded runners can be stopped through another Manager over the same
// store. These contexts belong to this process and exist only while a
// runner owns its turn lock; StopAsked in the turn row is the durable intent.
var turnCancels sync.Map // activeTurn -> context.CancelFunc

func (m *Manager) turnContext(ctx context.Context, id string, n int) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	key := activeTurn{m.store().root(), id, n}
	turnCancels.Store(key, cancel)
	return ctx, func() {
		turnCancels.Delete(key)
		cancel()
	}
}

// signalTurn cancels an embedded runner or signals the verified detached
// runner. A queued runner with no pid reads StopAsked at start and again
// when its pid is registered. Never signal the engine process itself.
func (m *Manager) signalTurn(meta Meta, n int, sig syscall.Signal) error {
	if cancel, ok := turnCancels.Load(activeTurn{m.store().root(), meta.ID, n}); ok {
		cancel.(context.CancelFunc)()
		return nil
	}
	if meta.Pid <= 0 || meta.Pid == os.Getpid() {
		return nil
	}
	return signalRunner(meta.ID, n, meta.Pid, sig)
}
