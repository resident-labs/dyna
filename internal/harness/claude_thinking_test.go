package harness

import (
	"slices"
	"testing"

	"dyna-agent/internal/profile"
)

// Claude 2.1.302 streams thinking blocks with only a signature unless
// --thinking-display is set; docs/reasoning-and-compaction.md.
func TestClaudeStreamRequestsThinkingSummaries(t *testing.T) {
	inv, err := buildInvocation(profile.Profile{Name: "c", Harness: profile.HarnessClaudeCode, Model: "sonnet"}, "task")
	if err != nil {
		t.Fatal(err)
	}
	for name, argv := range map[string][]string{"initial": inv.initial.argv, "resume": inv.resume("ses", "again").argv} {
		i := slices.Index(argv, "--thinking-display")
		if i < 0 || i+1 >= len(argv) || argv[i+1] != "summarized" {
			t.Fatalf("%s: argv = %v", name, argv)
		}
	}
	inv, err = buildInvocation(profile.Profile{Name: "c", Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--thinking-display", "omitted"}}, "task")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(slices.DeleteFunc(slices.Clone(inv.initial.argv), func(a string) bool { return a != "--thinking-display" })); n != 1 {
		t.Fatalf("profile choice not kept: %v", inv.initial.argv)
	}
}
