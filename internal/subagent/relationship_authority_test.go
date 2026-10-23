package subagent

import (
	"context"
	"testing"

	"dyna-agent/internal/mode"
)

func TestSessionRelationshipRefusals(t *testing.T) {
	for _, tc := range []struct{ name, action, caller, target, mode string }{
		{"self promote", "subagent promote", "child", "child", mode.Subagent},
		{"unrelated promote", "subagent promote", "other", "child", mode.Subagent},
		{"unrelated adopt", "subagent adopt", "other", "child", mode.Subagent},
		{"unrelated rename", "subagent rename", "other", "child", mode.Subagent},
		{"worker rename", "subagent rename", "child", "child", mode.Workflow},
		{"worker promote", "subagent promote", "lead", "child", mode.Workflow},
		{"worker adopt", "subagent adopt", "lead", "child", mode.Workflow},
		{"worker close", "subagent close", "lead", "child", mode.Workflow},
		{"self close", "subagent close", "child", "child", mode.Subagent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			m, rooms, _, metas := actorFixture(t)
			m.Rooms = rooms
			target := metas[tc.target]
			// Keep a missing self-close guard from reaching close side effects.
			if tc.action == "subagent close" {
				if _, err := m.Store.UpdateMeta(target.ID, func(meta *Meta) { meta.Consolidating = "guard" }); err != nil {
					t.Fatal(err)
				}
			}
			stampAs(t, metas[tc.caller])
			t.Setenv(mode.EnvMode, tc.mode)
			var err error
			switch tc.action {
			case "subagent promote":
				_, err = m.Promote(context.Background(), target.ID)
			case "subagent adopt":
				_, err = m.Adopt(context.Background(), target.ID, metas["other"].ID)
			case "subagent rename":
				_, err = m.Rename(context.Background(), target.ID, "renamed")
			case "subagent close":
				_, err = m.CloseWithOptions(context.Background(), target.ID, CloseOptions{Force: true})
			}
			want := tc.action + " is owner or root or parent only; ask the session parent or root to move it"
			switch tc.action {
			case "subagent close":
				want = "subagent close is owner or root or another session only; a session becomes dormant when its turn ends, so it does not close itself"
			case "subagent rename":
				want = "subagent rename is owner or root or self or parent only; ask the session or its parent to rename it"
			}
			if err == nil || err.Error() != want {
				t.Fatalf("got %v; want formatted authority refusal for %s", err, tc.action)
			}
		})
	}
}
