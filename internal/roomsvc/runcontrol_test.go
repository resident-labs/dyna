package roomsvc

import (
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
)

// A run that a session starts through the engine must stay that session's
// work, as a run started from its own shell does.
func TestRunEnvironmentKeepsTheCallerSession(t *testing.T) {
	t.Setenv(mode.EnvSession, "ses_engine")
	t.Setenv(mode.EnvWorkerRun, "wf_stale")
	rec := room.Record{ID: "room_1", Name: "dyna-agent"}
	from := runEnv(authority.Actor{Role: authority.Session, Room: rec.ID, Session: "ses_caller"}, rec)
	if got := envValue(from, mode.EnvSession); got != "ses_caller" {
		t.Fatalf("session = %q, want the calling session", got)
	}
	if got := envValue(from, mode.EnvMode); got != mode.Subagent {
		t.Fatalf("mode = %q, want %q", got, mode.Subagent)
	}
	if got := envValue(from, mode.EnvRoomID); got != rec.ID {
		t.Fatalf("room id = %q, want %q", got, rec.ID)
	}
	if got := envValue(from, mode.EnvWorkerRun); got != "" {
		t.Fatalf("a stale worker stamp reached the run: %q", got)
	}
	fromOwner := runEnv(authority.Actor{Role: authority.Owner}, rec)
	if got := envValue(fromOwner, mode.EnvSession); got != "" {
		t.Fatalf("an owner run took this process's session stamp: %q", got)
	}
	if got := envValue(fromOwner, mode.EnvMode); got != "" {
		t.Fatalf("an owner run took this process's mode stamp: %q", got)
	}
	if got := envValue(fromOwner, mode.EnvRoom); got != rec.Name {
		t.Fatalf("room = %q, want %q", got, rec.Name)
	}
}

func envValue(env []string, key string) string {
	for _, item := range env {
		if name, value, ok := strings.Cut(item, "="); ok && name == key {
			return value
		}
	}
	return ""
}
