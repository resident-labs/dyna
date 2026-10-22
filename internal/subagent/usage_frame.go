package subagent

import (
	"dyna-agent/internal/account/service"
	"dyna-agent/internal/account/store"
)

// usageFrame is the session's own account usage note. It reads only the cached
// observation, so a turn never waits on the network.
func usageFrame(harness string) string {
	return service.New(service.Options{Store: store.DefaultStore()}).OwnNote(harness)
}
