package subagent

import (
	"os"
	"testing"
	"time"

	"dyna-agent/internal/profile"
)

// CompactIdle starts a compact-only turn for an idle session at the context
// floor, and for no other session: a session that ran another turn since the
// timer was armed, went dormant, or holds a small context is left alone.
func TestCompactIdleStartsACompactOnlyTurnOnlyForAnIdleBigSession(t *testing.T) {
	claude := profile.Profile{Name: "claude", Harness: profile.HarnessClaudeCode, Default: true}
	for _, tc := range []struct {
		name    string
		status  string
		context int64
		armed   int
		spawned bool
	}{
		{"idle and big", StatusIdle, profile.IdleCompactMinTokens, 1, true},
		{"small context", StatusIdle, profile.IdleCompactMinTokens - 1, 1, false},
		{"another turn ran since", StatusIdle, profile.IdleCompactMinTokens, 2, false},
		{"dormant", StatusDormant, profile.IdleCompactMinTokens, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, claude)
			var spawned []int
			e.m.Spawner = recordRoomSpawn{start: func(_ *Manager, _ string, n int, f *os.File) error {
				spawned = append(spawned, n)
				return f.Close()
			}}
			id := NewID()
			dir, err := e.m.Store.Dir(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dir+"/turns", 0o700); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			meta := Meta{ID: id, Name: "idle-" + id, RoomID: e.room.ID, Room: e.room.Name, Profile: "claude",
				Dir: t.TempDir(), Depth: 1, Created: now, LastActivity: now, Turns: 1, Resumable: true,
				HarnessSession: "harness-session", ContextTokens: tc.context, Status: tc.status}
			if err := e.m.Store.writeMeta(meta); err != nil {
				t.Fatal(err)
			}
			cutoff, err := e.m.rooms().FileSize(e.room.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.m.Store.writeTurn(id, Turn{N: 1, Status: TurnOK, Profile: "claude", Prompt: "work",
				Created: now, Started: now, Ended: now, InputCutoff: &cutoff}); err != nil {
				t.Fatal(err)
			}
			if err := e.m.CompactIdle(e.ctx, id, tc.armed); err != nil {
				t.Fatal(err)
			}
			if got := len(spawned) == 1; got != tc.spawned {
				t.Fatalf("spawned turns = %v, want a compact turn: %t", spawned, tc.spawned)
			}
			if tc.spawned {
				turn, err := e.m.Store.ReadTurn(id, 2)
				if err != nil || !turn.CompactOnly || turn.Prompt != "" || turn.Wake.Kind != WakeCompact {
					t.Fatalf("turn 2 = %+v, %v; want a compact-only turn with no prompt", turn, err)
				}
			}
		})
	}
}
