package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	harnesshome "dyna-agent/internal/harness/home"
)

// The Accounts page picks the provider once. The helper must carry that choice
// to the Pi process, so the extension does not ask for it again.
func TestPiSignInHelperCarriesTheChosenProvider(t *testing.T) {
	scope := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", scope)
	t.Setenv("HOME", harnesshome.Home(scope))
	fake := filepath.Join(scope, "fake-pi")
	script := "#!/bin/sh\nprintf '%s' \"$PI_DYNA_PROVIDER\" > \"$PI_CODING_AGENT_DIR/provider.txt\"\n"
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := PiSignInCommand(context.Background(), []string{"harness", "accounts", piSignInCommand, "--pi", fake, "--provider", "openai-codex"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(scope, "provider.txt"))
	if err != nil || string(got) != "openai-codex" {
		t.Fatalf("Pi received provider %q, %v", got, err)
	}
}

func TestPiOAuthArgvCarriesTheProvider(t *testing.T) {
	argv := piOAuthArgv("/pi/bin/pi", "kimi-coding")
	if len(argv) < 2 || argv[len(argv)-2] != "--provider" || argv[len(argv)-1] != "kimi-coding" {
		t.Fatalf("Pi OAuth argv = %v", argv)
	}
}
