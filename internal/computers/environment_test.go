package computers

import (
	"slices"
	"testing"
)

func TestEnvironmentKeepsTheLaunchCredentials(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	base := []string{"PATH=/usr/bin", "GH_TOKEN=session-token", "ANTHROPIC_API_KEY=harness-key", "HOME=/home/owner"}
	got, err := Environment(Meta{SessionID: "ses_test"}, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GH_TOKEN=session-token", "ANTHROPIC_API_KEY=harness-key"} {
		if !slices.Contains(got, want) {
			t.Fatalf("Environment dropped %q", want)
		}
	}
}

func TestEngineEnvironmentDropsEngineSecrets(t *testing.T) {
	t.Setenv("GH_TOKEN", "must-not-reach-computers")
	t.Setenv("ANTHROPIC_API_KEY", "must-not-reach-computers")
	t.Setenv("DYNA_TEST_KEEP", "kept")
	got := engineEnvironment()
	for _, entry := range got {
		if entry == "GH_TOKEN=must-not-reach-computers" || entry == "ANTHROPIC_API_KEY=must-not-reach-computers" {
			t.Fatalf("engine environment kept %q", entry)
		}
	}
	if !slices.Contains(got, "DYNA_TEST_KEEP=kept") {
		t.Fatal("engine environment dropped a configuration entry")
	}
}
