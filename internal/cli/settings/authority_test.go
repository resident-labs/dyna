package settings

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store/storetest"
)

// The engine supplies caller power. A cleared stamp or forged command context
// must not let a session write global settings as the owner.
func TestSettingsCLIUsesCallerPower(t *testing.T) {
	for _, role := range []authority.Role{authority.Owner, authority.Root, authority.Session, authority.Worker} {
		t.Run(string(role), func(t *testing.T) {
			storetest.Isolate(t)
			_, called := serveSetSetting(t, authority.Actor{Role: role, Session: "session", Run: "wf_test"}, configsvc.Set)
			allow := role == authority.Owner || role == authority.Root
			for _, asJSON := range []bool{false, true} {
				baseline := settings.Defaults()
				if err := baseline.Save(); err != nil {
					t.Fatal(err)
				}
				cmd := NewCommand()
				var output bytes.Buffer
				cmd.SetOut(&output)
				cmd.SetErr(&output)
				args := []string{"set", settings.KeySubagentMaxDepth, "4"}
				if asJSON {
					args = append(args, "--json")
				}
				cmd.SetArgs(args)
				ctx := t.Context()
				if allow {
					ctx = authority.WithActor(ctx, authority.Actor{Role: authority.Session})
				} else if role == authority.Worker {
					ctx = authority.WithActor(ctx, authority.Actor{Role: authority.Owner})
				}
				err := cmd.ExecuteContext(ctx)
				if allow && err != nil {
					t.Fatalf("allowed engine write: %v", err)
				}
				if !allow && (err == nil || !strings.Contains(err.Error(), "settings set is owner or root only")) {
					t.Errorf("engine setting write as %s: %v", role, err)
				}
				if allow && !asJSON && output.String() != "set subagent.maxDepth = 4\n" {
					t.Errorf("set output = %q", output.String())
				}
				if allow && asJSON {
					var item listItem
					if err := json.Unmarshal(output.Bytes(), &item); err != nil || item.Key != settings.KeySubagentMaxDepth || item.Value != float64(4) || item.Source != settings.SourceFile || item.LiveVsRestart != settings.LiveNextOpen {
						t.Errorf("set JSON = %q, %v", output.String(), err)
					}
				}
				saved, err := settings.Load(settings.DefaultPath())
				if err != nil {
					t.Fatal(err)
				}
				want := settings.DefaultSubagentMaxDepth
				if allow {
					want = 4
				}
				if saved.SubagentMaxDepth != want {
					t.Errorf("stored depth %d, want %d", saved.SubagentMaxDepth, want)
				}
			}
			if called.Load() != 2 {
				t.Fatalf("SetSetting calls = %d, want 2", called.Load())
			}
		})
	}
}
