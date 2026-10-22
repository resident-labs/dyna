package mode

import (
	"os"
	"testing"
)

type stampKey struct {
	name       string
	marksAgent bool
}

// Keep the agent markers first, in their existing owner-command guard order.
// The remaining keys also belong to the stamp that test fixtures must clear.
var stampKeys = [...]stampKey{
	{name: EnvSession, marksAgent: true},
	{name: EnvRunID, marksAgent: true},
	{name: EnvAgentID, marksAgent: true},
	{name: EnvWorkerRun, marksAgent: true},
	{name: EnvAddress, marksAgent: true},
	{name: EnvMode},
	{name: EnvWorkerAgent},
	{name: EnvDepth},
	{name: EnvRoomID},
	{name: EnvRoom},
	{name: EnvRunDetached},
}

// A test binary never inherits its caller's stamp, such as the session of
// the agent that runs go test: it would act as that session against the
// live room. A test sets the stamp it needs itself.
func init() {
	if testing.Testing() {
		for _, key := range stampKeys {
			os.Unsetenv(key.name)
		}
	}
}

// StampKeys returns the environment keys that identify Dyna work.
// The returned slice belongs to the caller.
func StampKeys() []string {
	keys := make([]string, 0, len(stampKeys))
	for _, key := range stampKeys {
		keys = append(keys, key.name)
	}
	return keys
}

// AgentStampKeys returns the markers that keep owner commands out of agents.
// Room-only owner stamps keep the existing owner-command behavior.
func AgentStampKeys() []string {
	var keys []string
	for _, key := range stampKeys {
		if key.marksAgent {
			keys = append(keys, key.name)
		}
	}
	return keys
}
