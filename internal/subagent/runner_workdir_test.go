package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
)

// A removed-directory schema retry rebuilds its prompt from base on the
// non-resumable path. Every attempt must still carry the directory note.
func TestSchemaRetryKeepsDirectoryNote(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "prompt")
	e := newTestEnv(t, profile.Profile{
		Name: "custom", Harness: profile.HarnessCustom, Taste: 5, Intelligence: 5, Cost: 5,
		Command: []string{"/bin/sh", "-c", `
count=0
if [ -f "$COUNT" ]; then count=$(/bin/cat "$COUNT"); fi
count=$((count+1))
printf '%s' "$count" > "$COUNT"
/bin/cat > "$CAPTURE.$count"
if [ "$count" -eq 1 ]; then printf '"invalid"'; else printf '{"n":7}'; fi
`},
		Env: map[string]string{"COUNT": filepath.Join(dir, "count"), "CAPTURE": capture},
	})
	repo := t.TempDir()
	if err := e.m.RoomStore().UpdatePreferences(e.room.ID, func(r *room.Record) error {
		r.Repo = repo
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	// Hold the schema turn before it starts, so the worktree can be removed
	// first and the run is deterministic.
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	info, turn := e.start("Produce the requested number.", func(o *StartOptions) {
		o.Profile = "custom"
		o.Dir = worktree
		o.Schema = `{"type":"object","required":["n"],"properties":{"n":{"type":"number"}}}`
	})
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	if err := e.m.RunTurn(e.ctx, info.ID, turn.N, held.lock); err != nil {
		t.Fatal(err)
	}
	done, err := e.m.Store.ReadTurn(info.ID, turn.N)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != TurnOK {
		t.Fatalf("schema retry failed: %#v", done)
	}
	for _, attempt := range []string{"1", "2"} {
		prompt := readFile(t, capture+"."+attempt)
		if !strings.Contains(prompt, repo) || !strings.Contains(prompt, "no longer exists") {
			t.Fatalf("attempt %s lacks the working-directory note:\n%s", attempt, prompt)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
