package harness

import (
	"strings"
	"testing"

	"dyna-agent/internal/profile"
)

func TestCodexWorkersDisableCodeMode(t *testing.T) {
	p := profile.Profile{Name: "c", Harness: profile.HarnessCodex, Model: "m"}
	inv, err := buildInvocation(p, "task")
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []commandSpec{inv.initial, inv.resume("thread", "go on")} {
		if !strings.Contains(strings.Join(spec.argv, " "), "-c "+CodexCodeModeOverride) {
			t.Fatalf("codex argv lacks the code-mode override: %v", spec.argv)
		}
	}
	for _, extra := range [][]string{{"--enable", "code_mode"}, {"-c", "features.code_mode=true"}} {
		p.ExtraArgs = extra
		inv, err := buildInvocation(p, "task")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(strings.Join(inv.initial.argv, " "), "code_mode") != 1 {
			t.Fatalf("explicit %v overridden: %v", extra, inv.initial.argv)
		}
	}
}
