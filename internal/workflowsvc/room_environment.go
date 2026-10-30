package workflowsvc

import (
	"os"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/room"
)

// RoomRunEnvironment builds the stamped, secret-filtered environment used by
// room workflow launches and by recovery when checking their fingerprint.
func RoomRunEnvironment(actor authority.Actor, rec room.Record) []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !strings.HasPrefix(key, "DYNA_") && !processguard.SecretName(key) {
			env = append(env, item)
		}
	}
	env = append(env, mode.EnvRoomID+"="+rec.ID, mode.EnvRoom+"="+rec.Name)
	if actor.Role == authority.Session && actor.Session != "" {
		env = append(env, mode.EnvMode+"="+mode.Subagent, mode.EnvSession+"="+actor.Session)
	}
	return env
}
