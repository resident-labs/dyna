package config

import "testing"

func TestCatalogCapabilities(t *testing.T) {
	got := map[string]HarnessConfigCapabilities{}
	for _, capability := range ConfigCapabilities() {
		got[capability.Harness] = capability
	}
	for _, harness := range ConfigHarnesses {
		if capability, ok := got[harness]; !ok || !capability.MCP || !capability.Skills {
			t.Errorf("missing capabilities for %s", harness)
		}
	}
}
