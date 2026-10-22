package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
)

func TestMuseGeneratedConfigAndInstructionsHaveNoPrefixConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// The native feature policy generates run controls. Instructions and the
	// compaction threshold add per-turn dotted paths to the same command.
	raw := `{"run":{"subagent_delegation_mode":"off","workflow_trigger_mode":"off"},"context_compaction":{"hard_threshold":0.9},"mcpServers":{"dyna":{"command":"dyna","args":["mcp","serve"]}}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	adapter, err := harnessruntime.Adapter("muse")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessMuse, ExtraArgs: adapter.ConfigArgs(t.TempDir(), path, ""), RuntimeInstructions: "Use Dyna tools.", Compact: &profile.Compact{Tokens: 95000, Windows: map[string]int64{"m": 200000}}, Model: "m"}, "Reply pong.")
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []commandSpec{inv.initial, inv.resume("session-id", "Reply pong.")} {
		configs := codexConfigArgs(spec.argv)
		var keys []string
		for _, pair := range configs {
			key, _, _ := strings.Cut(pair, "=")
			keys = append(keys, key)
		}
		for i, a := range keys {
			for j, b := range keys {
				if i != j && (a == b || strings.HasPrefix(b, a+".")) {
					t.Fatalf("Muse rejects config paths %q and %q: %v", a, b, spec.argv)
				}
			}
		}
		for _, want := range []string{"run.subagent_delegation_mode", "run.workflow_trigger_mode", "run.developer_prompt", "context_compaction.hard_threshold", "context_compaction.soft_threshold", "mcpServers.dyna"} {
			found := false
			for _, key := range keys {
				if key == want {
					found = true
				}
			}
			if !found {
				t.Errorf("lost %s: %v", want, spec.argv)
			}
		}
	}
}
