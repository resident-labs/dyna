package subagent

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/store"
)

func TestUsageFrameShowsBusyActiveAccount(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	root := filepath.Join(store.DataDir(), "accounts")
	n, err := (&runtime.Accounts{Root: root}).Create("codex", "Work")
	if err != nil {
		t.Fatal(err)
	}
	if got := usageFrame("codex"); got != "" {
		t.Fatalf("usageFrame without an active account = %q", got)
	}
	if err := (&accountstore.Store{Root: root}).SetActive(accountstore.ProviderChatGPT, "", n.ID); err != nil {
		t.Fatal(err)
	}
	busy := 80.0
	if err := (&accountstore.Store{Root: root}).Update(func(s *accountstore.State) error {
		s.Entries[0].Observation = &accountstore.Observation{ObservedAt: time.Now(), Windows: []accountstore.Window{{ID: "w", Label: "weekly", UsedPercent: &busy}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := usageFrame("codex"); !strings.HasPrefix(got, "[DYNA USAGE] codex (Work): weekly 80%") {
		t.Fatalf("usageFrame = %q", got)
	}
	if got := usageFrame("claude-code"); got != "" {
		t.Fatalf("usageFrame for another harness = %q", got)
	}
}
