package subagent

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/harness"
	"dyna-agent/internal/profile"
)

// fallbackChainLimit caps how many profiles one session's automatic moves may
// touch. A chain beyond this is a configuration mistake, not a provider that
// keeps running out.
const fallbackChainLimit = 8

// fallbackAfterLimit moves a session to its profile's fallback when a turn
// hits a usage limit and no account on the current harness can take it. It
// returns true when the durable transfer is saved: the turn then ends and the
// replacement turn continues the work there with the shared handoff packet.
// Chains never loop, because the session records every profile it left.
func (m *Manager) fallbackAfterLimit(ctx context.Context, meta Meta, cause error) bool {
	if cause == nil {
		return false
	}
	text := cause.Error()
	if !harness.IsUsageLimit(text) || harness.IsCapacityFailure(text) {
		return false
	}
	ps, err := m.profiles()
	if err != nil {
		return false
	}
	target, ok := fallbackTarget(meta, ps)
	if !ok {
		return false
	}
	// The move is the engine's own durable work: a stop signal that ends this
	// turn, including the fallback's own stop, must not cancel it.
	ctx = context.WithoutCancel(ctx)
	opts := TransferOptions{
		Profile:     target.Name,
		WaitSeconds: 0, // the saved work is enough; do not ask the old model for a handoff
		Fallback:    true,
		Notice:      fallbackNotice(meta.Address(), meta.ID, meta.Profile, target.Name, text, m.transferNow()),
		LimitReason: "usage limit on " + meta.Profile,
	}
	// A runner process ends with its turn, so the coordinator must live in the
	// engine. Only embedded and test managers request the transfer in process.
	if m.Engine != nil {
		var out TransferState
		err = m.Engine.Call(ctx, "TransferSession", []any{meta.RoomID, meta.ID, opts}, &out)
	} else {
		_, err = m.requestTransfer(ctx, meta.ID, opts)
	}
	if err != nil {
		log.Printf("fallback for %s to %s: %v", meta.Address(), target.Name, err)
		return false
	}
	return true
}

// fallbackTarget resolves the next profile of the chain. It refuses a target
// the session already left, a disabled target, and a chain that is too long.
func fallbackTarget(meta Meta, ps *profile.Store) (profile.Profile, bool) {
	if len(meta.FallbackPath) >= fallbackChainLimit {
		return profile.Profile{}, false
	}
	current, ok := ps.Get(meta.Profile)
	if !ok {
		return profile.Profile{}, false
	}
	name := strings.TrimSpace(current.Fallback)
	if name == "" || name == meta.Profile || slices.Contains(meta.FallbackPath, name) {
		return profile.Profile{}, false
	}
	target, ok := ps.Get(name)
	if !ok || target.Disabled {
		return profile.Profile{}, false
	}
	return *target, true
}

// fallbackNotice is the one session DM that explains an automatic move. It
// names the reset time only when the provider gave one, and it says that the
// move back is not automatic. A very long session name falls back to the
// session ID, so the notice always fits in one post.
func fallbackNotice(address, id, from, to, cause string, now time.Time) string {
	reset := ""
	if at := harness.UsageResetAt(cause, now); !at.IsZero() {
		reset = fmt.Sprintf(", resets %s", at.Format("15:04"))
	}
	handle := address
	if len(handle) > 120 {
		handle = id
	}
	return fmt.Sprintf("Moved from %s to %s: usage limit%s. Dyna will not move it back automatically; `dyna subagent transfer %s --profile %s` moves it back.", from, to, reset, handle, from)
}
