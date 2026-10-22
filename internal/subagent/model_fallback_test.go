package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/harness"
	"dyna-agent/internal/profile"
)

func TestRunTurnModelFallbackAndMetadataOverride(t *testing.T) {
	for _, tc := range []struct{ name, override, wantMeta, wantTurn, calls string }{
		{"fallback", "", "a", "b", "a,b"},
		{"member preserves chain", "b", "b", "b", "b"},
		{"explicit override", "custom", "custom", "custom", "custom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			harness.ResetModelCooldownsForTest()
			t.Cleanup(harness.ResetModelCooldownsForTest)
			log := filepath.Join(t.TempDir(), "models")
			e := newTestEnv(t, profile.Profile{
				Name: "chain", Harness: profile.HarnessCustom, Model: "a", Models: []string{"a", "b"},
				Taste: 5, Intelligence: 5, Cost: 5,
				Command: []string{"/bin/sh", "-c", `
/bin/cat >/dev/null
printf '%s\n' "$1" >> "$MODEL_LOG"
if [ "$1" = "a" ]; then printf 'quota exhausted' >&2; exit 1; fi
printf 'served by %s' "$1"
`, "worker", "{{model}}"},
				Env: map[string]string{"MODEL_LOG": log},
			})
			held := &heldTurnSpawner{}
			e.m.Spawner = held
			info, _ := e.start("task", func(o *StartOptions) { o.Profile = "chain" })
			if tc.override != "" {
				if _, err := e.m.Store.UpdateMeta(info.ID, func(mm *Meta) { mm.Model = tc.override }); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.m.RunTurn(e.ctx, info.ID, 1, held.lock); err != nil {
				t.Fatal(err)
			}
			meta, err := e.m.Store.ReadMeta(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			turn, err := e.m.Store.ReadTurn(info.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if meta.Model != tc.wantMeta || turn.Model != tc.wantTurn || turn.Status != TurnOK ||
				turn.Result != "served by "+tc.wantTurn {
				t.Fatalf("model not persisted: meta=%s, turn=%#v", meta.Model, turn)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.ReplaceAll(strings.TrimSpace(string(data)), "\n", ","); got != tc.calls {
				t.Fatalf("models = %q, want %q", got, tc.calls)
			}
		})
	}
}
