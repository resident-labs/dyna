package runtime

import (
	harnessconfig "dyna-agent/internal/harness/config"
	"slices"
	"testing"
)

func TestCatalogUsesRuntimeHarnessIDs(t *testing.T) {
	for _, adapter := range RuntimeAdapters() {
		if !slices.Contains(harnessconfig.ConfigHarnesses, adapter.Harness) {
			t.Errorf("runtime harness %q is missing from catalog IDs", adapter.Harness)
		}
	}
	if slices.Contains(harnessconfig.ConfigHarnesses, "claude") {
		t.Error("catalog exposes a second Claude ID")
	}
	for _, capability := range harnessconfig.ConfigCapabilities() {
		if !slices.Contains(harnessconfig.ConfigHarnesses, capability.Harness) {
			t.Errorf("capability exposes unknown harness %q", capability.Harness)
		}
	}
}
