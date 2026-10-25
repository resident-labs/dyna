package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/harness/fakeworker"
)

func TestRunTurnModelFallbackAndMetadataOverride(t *testing.T) {
	for _, tc := range []struct{ name, override, wantMeta, wantTurn, calls string }{
		{"fallback", "", "a", "b", "a,b"},
		{"member preserves chain", "b", "b", "b", "b"},
		{"explicit override", "custom", "custom", "custom", "custom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "models")
			p := fakeworker.InstallClaude(t, `
printf '%s\n' "$DYNA_FAKE_MODEL" >> "$MODEL_LOG"
if [ "$DYNA_FAKE_MODEL" = "a" ]; then printf 'quota exhausted' >&2; exit 1; fi
printf 'served by %s' "$DYNA_FAKE_MODEL"
`)
			p.Name, p.Model, p.Models = "chain", "a", []string{"a", "b"}
			p.ExtraArgs = []string{"--no-session-persistence"}
			p = fakeworker.MarkStatus(p, 429)
			p.Env = mergedEnv(p.Env, map[string]string{"MODEL_LOG": log})
			e := newTestEnv(t, p)
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
