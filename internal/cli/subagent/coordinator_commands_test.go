package subagent

import (
	"testing"

	"dyna-agent/internal/subagent"
)

func TestCoordinatorCommandsUseTheEngine(t *testing.T) {
	cmd := newCommand(func() *subagent.Manager {
		t.Fatal("command lookup opened a manager")
		return nil
	})
	for _, name := range []string{"_transfer", "_consolidate", "_resume-room"} {
		found, _, err := cmd.Find([]string{name})
		if err == nil && found != cmd && found.Name() == name {
			t.Errorf("retired coordinator command %s is still available", name)
		}
	}
	found, _, err := cmd.Find([]string{"transfer"})
	if err != nil || found == cmd || found.Name() != "transfer" {
		t.Fatalf("public transfer command is unavailable: %v", err)
	}
}
