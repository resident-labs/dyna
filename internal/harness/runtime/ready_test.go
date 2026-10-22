package runtime

import (
	"context"
	"testing"
)

// An exact version needs no latest-version metadata: first use installs it.
func TestHarnessReadyAcceptsPinnedVersionWithoutBundle(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	installed, err := HarnessReady(context.Background(), "pi", "1.3.0")
	if installed || err != nil {
		t.Fatalf("HarnessReady() = %v, %v; want installable on first use", installed, err)
	}
}
